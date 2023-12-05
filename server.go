package pot

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"log/slog"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
)

var (
	ErrNoRewriteViolated = errors.New("no-rewrite rule was violated")
)

type Server struct {
	bucket *storage.BucketHandle

	// pathLocks is a map of paths and their dedicated locks. This is used to prevent
	// multiple processes from writing to the same path at the same time or unnecessarily
	// colliding during distributed lock acquisition.
	pathLocks map[string]*sync.RWMutex

	// pathLocksMux is a mutex that protects the pathLocks map
	pathLocksMux sync.Mutex

	// DistributedLock indicates whether the client should use distributed locking
	// to prevent multiple processes from writing to the same path at the same time.
	// While this option prevents multi-process race, it also slows down the process
	// as two objects need to be written to the bucket instead of one.
	distributedLock bool
}

func NewServer(ctx context.Context, bucketName string, opts ...Option) (*Server, error) {
	gcs, err := storage.NewClient(ctx)
	if err != nil {
		return nil, err
	}

	c := &Server{
		bucket:    gcs.Bucket(bucketName),
		pathLocks: map[string]*sync.RWMutex{},
	}

	for _, opt := range opts {
		opt(c)
	}

	return c, nil
}

// Option is a functional option for the Client. It allows to
// configure the client via its constructor.
type Option func(*Server)

// WithDistributedLock enables distributed locking on the Client.
// This slows down the process of writing, however, it prevents
// multiple processes from writing to the same pot at the same time.
func WithDistributedLock() Option {
	return func(c *Server) {
		c.distributedLock = true
	}
}

func (c *Server) potPath(urlPath string) string {
	return path.Join(urlPath, "data.json")
}

// CallOpts is a set of options that can be passed to the client methods.
type CallOpts struct {
	batch               bool
	norewrite           bool
	norewriteDuration   time.Duration
	lastKnownGeneration int64
}

// CallOpt is a functional option for the client methods. It allows to
// configure the client methods.
type CallOpt func(*CallOpts)

// WithBatch enables batch requests on the client methods.
func WithBatch() CallOpt {
	return func(o *CallOpts) {
		o.batch = true
	}
}

// WithNoRewrite disables rewriting of keys that already exist in data and only
// enables the write if either of these conditions is met:
//   - the key doesn't exist in data.
//   - the key exists in data, but the last modification of the data is older than
//     the provided duration.
//   - the last read generation is the last known generation for this path
//     (cached by the client).
//
// This option makes the whole request fail if any of the keys fail.
func WithNoRewrite(deadline time.Duration) CallOpt {
	return func(o *CallOpts) {
		o.norewrite = true
		o.norewriteDuration = deadline
	}
}

// WithRewriteGeneration sets the last known generation for the given path. This is used
// in conjunction with the no-rewrite option to assert whether the last modification
// of the pot is the same as the last known generation.
func WithRewriteGeneration(gen int64) CallOpt {
	return func(o *CallOpts) {
		o.lastKnownGeneration = gen
	}
}

// canRewrite checks whether the last modification of the pot is older than the
// provided duration.
func canRewrite(lastModification, now time.Time, duration time.Duration) bool {
	return lastModification.Add(duration).Before(now)
}

// CreateResponse is the response returned by the Create method.
type CreateResponse struct {
	Content    map[string]any `json:"content"`
	Generation int64          `json:"generation"`
}

func (c *Server) Create(ctx context.Context, dir string, r io.Reader, callOpts ...CallOpt) (*CreateResponse, error) {
	slog.Debug("acquiring lock", slog.String("dir", dir), slog.String("method", "create"))
	defer slog.Debug("releasing lock", slog.String("dir", dir), slog.String("method", "create"))

	opts := &CallOpts{}
	for _, opt := range callOpts {
		opt(opts)
	}

	// acquire the lock for the given path on the current client and defer the release
	c.localLock(dir).Lock()
	defer c.localLock(dir).Unlock()

	// if distributed locking is enabled, try to acquire the lock
	// and defer the release of the lock
	if c.distributedLock {
		slog.Debug("acquiring distributed lock", slog.String("dir", dir), slog.String("method", "create"))
		defer slog.Debug("removing distributed lock", slog.String("dir", dir), slog.String("method", "create"))

		id, err := c.lockSharedPath(ctx, dir)
		if err != nil {
			return nil, err
		}
		defer func(id string) {
			err := c.unlockSharedPath(ctx, dir, id)
			if err != nil {
				slog.Error("failed to unlock path", slog.String("dir", dir), slog.String("method", "create"), slog.String("error", err.Error()))
			}
		}(id)
	}

	content := map[string]any{}
	pot := c.bucket.Object(c.potPath(dir))

	reader, err := pot.NewReader(ctx)
	// return an error if an unexpected error occurred
	if err != nil && err != storage.ErrObjectNotExist {
		return nil, err
	}

	// decode the content if the object exists, otherwise the content will be empty
	if err != storage.ErrObjectNotExist {
		defer reader.Close()

		if err := json.NewDecoder(reader).Decode(&content); err != nil {
			return nil, err
		}
	}

	objs := map[string]any{}
	// if the batch option is set, decode the content as a batch request
	if opts.batch {
		objs, err = decodeBatchContent(r)
		if err != nil {
			return nil, err
		}
	} else {
		// decode the new object so it can be added to the content
		obj := map[string]any{}
		if err := json.NewDecoder(r).Decode(&obj); err != nil {
			return nil, err
		}

		// check whether either "id" or "name" is set and use the value as key
		var key string
		if name, ok := obj["name"]; ok {
			key = name.(string)
		}
		if id, ok := obj["id"]; ok {
			key = id.(string)
		}

		// add the new object to the content
		objs[key] = obj
	}

	// assert whether rewrites of existing keys are allowed. By default, clients
	// can overwrite any keys at any time. However if the no-rewrite option is
	// set, the client will only allow the write if the key doesn't exist, is owned
	// by the current client or the last modification of the key is older than the
	// provided duration.
	allowRewrite := true

	// if the reader is nil, it means that the pot doesn't exist yet and therefore
	// the no-rewrite rule doesn't apply
	if reader != nil {
		// check whether the no-rewrite rule contains duration and if so, check whether
		// the duration has passed since the last modification of the pot
		if opts.norewrite {
			if opts.norewriteDuration > 0 && !canRewrite(reader.Attrs.LastModified, time.Now(), opts.norewriteDuration) {
				allowRewrite = false
			}

			// check if the last cached generation doesn't correspond to the current one
			// and if so, enable the rewrite anyway
			if reader.Attrs.Generation == opts.lastKnownGeneration {
				allowRewrite = true
			}
		}
	}

	for k, v := range objs {
		if _, ok := content[k]; ok {
			if !allowRewrite {
				return nil, fmt.Errorf("%w: %s", ErrNoRewriteViolated, k)
			}
		}

		content[k] = v
	}

	// encode the content to the pot
	writer := pot.NewWriter(ctx)
	if err := json.NewEncoder(writer).Encode(content); err != nil {
		return nil, err
	}
	writer.Close()

	return &CreateResponse{
		Content:    objs,
		Generation: writer.Attrs().Generation,
	}, nil
}

// decodeBatchContent decodes the content of a batch request. The batch request
// is a 2-level map instead of a single-level map like with non-batch requests.
func decodeBatchContent(r io.Reader) (map[string]any, error) {
	batch := map[string]map[string]any{}
	if err := json.NewDecoder(r).Decode(&batch); err != nil {
		return nil, err
	}

	content := map[string]any{}
	for k, obj := range batch {
		content[k] = obj
	}

	return content, nil
}

func (c *Server) Get(ctx context.Context, dir string) (map[string]interface{}, error) {
	c.localLock(dir).RLock()
	defer c.localLock(dir).RUnlock()

	content := map[string]interface{}{}
	pot := c.bucket.Object(c.potPath(dir))

	reader, err := pot.NewReader(ctx)
	// return an error if an unexpected error occurred
	if err != nil && err != storage.ErrObjectNotExist {
		return nil, err
	}

	// decode the content if the object exists, otherwise the content will be empty
	if err != storage.ErrObjectNotExist {
		defer reader.Close()

		if err := json.NewDecoder(reader).Decode(&content); err != nil {
			return nil, err
		}
	}

	return content, nil
}

// Remove removes the provided keys from the pot on the given directory path.
func (c *Server) Remove(ctx context.Context, dir string, keys ...string) error {
	slog.Debug("acquiring lock", slog.String("dir", dir), slog.String("method", "remove"))
	defer slog.Debug("releasing lock", slog.String("dir", dir), slog.String("method", "remove"))

	c.localLock(dir).Lock()
	defer c.localLock(dir).Unlock()

	if c.distributedLock {
		slog.Debug("acquiring distributed lock", slog.String("dir", dir), slog.String("method", "remove"))
		defer slog.Debug("removing distributed lock", slog.String("dir", dir), slog.String("method", "remove"))

		id, err := c.lockSharedPath(ctx, dir)
		if err != nil {
			return err
		}
		defer func(id string) {
			err := c.unlockSharedPath(ctx, dir, id)
			if err != nil {
				slog.Error("failed to unlock path", slog.String("dir", dir), slog.String("method", "remove"), slog.String("error", err.Error()))
			}
		}(id)
	}

	content := map[string]interface{}{}
	pot := c.bucket.Object(c.potPath(dir))

	reader, err := pot.NewReader(ctx)
	// return an error if an unexpected error occurred
	if err != nil && err != storage.ErrObjectNotExist {
		return err
	}

	// decode the content if the object exists, otherwise the content will be empty
	if err != storage.ErrObjectNotExist {
		defer reader.Close()

		if err := json.NewDecoder(reader).Decode(&content); err != nil {
			return err
		}
	}

	// delete the object from the content
	for _, key := range keys {
		delete(content, key)
	}

	// encode the content to the pot
	writer := pot.NewWriter(ctx)
	defer writer.Close()
	if err := json.NewEncoder(writer).Encode(content); err != nil {
		return err
	}

	return nil
}

func (c *Server) Zip(ctx context.Context, dir string) error {
	c.localLock(dir).Lock()
	defer c.localLock(dir).Unlock()

	var buf strings.Builder
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	objList := c.bucket.Objects(ctx, &storage.Query{})
	for {
		obj, err := objList.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return err
		}

		// ignore objects that are in the directory where the zip is stored
		if strings.HasPrefix(obj.Name, dir) {
			continue
		}

		// ignore the .potlock file
		if strings.HasSuffix(obj.Name, ".potlock") {
			continue
		}

		objReader, err := c.bucket.Object(obj.Name).NewReader(ctx)
		if err != nil {
			return err
		}
		defer objReader.Close()

		hdr := &tar.Header{
			Name: obj.Name,
			Size: obj.Size,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}

		if _, err := io.Copy(tw, objReader); err != nil {
			return err
		}
	}

	if err := tw.Close(); err != nil {
		return err
	}

	if err := gzw.Close(); err != nil {
		return err
	}

	dst := c.bucket.Object(path.Join(dir, "bundle.tar.gz"))
	writer := dst.NewWriter(ctx)
	defer writer.Close()

	if _, err := io.Copy(writer, strings.NewReader(buf.String())); err != nil {
		return err
	}

	return nil
}

// localLock returns the lock for the given path. If the lock doesn't exist, it
// will be created and returned.
func (c *Server) localLock(dir string) *sync.RWMutex {
	c.pathLocksMux.Lock()
	defer c.pathLocksMux.Unlock()

	lock, ok := c.pathLocks[dir]
	if ok {
		return lock
	}
	c.pathLocks[dir] = &sync.RWMutex{}
	return c.pathLocks[dir]
}

// lockSharedPath creates a .potlock file on the given path to prevent other processes
// from modifying the pot.
//
// The process is as following:
// 1. try to create the .potlock file if it doesn't exist
// 2. if the file succeeds to create, the path is locked by this process
// 3. if the file fails to create on the precondition, the path is locked by another process
func (c *Server) lockSharedPath(ctx context.Context, dir string) (string, error) {
	lock := c.bucket.Object(path.Join(dir, ".potlock"))

	tstamp := strconv.Itoa(int(time.Now().Unix()))

	// try to create the lock file
	w := lock.If(storage.Conditions{DoesNotExist: true}).NewWriter(ctx)
	err := func() error {
		if _, err := io.WriteString(w, tstamp); err != nil {
			return err
		}

		return w.Close()
	}()
	if err != nil {
		return "", err
	}

	return strconv.FormatInt(w.Attrs().Generation, 10), nil
}

// unlockSharedPath removes the .potlock file from the given path.
func (c *Server) unlockSharedPath(ctx context.Context, dir, id string) error {
	gen, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return err
	}

	return c.bucket.
		Object(path.Join(dir, ".potlock")).
		If(storage.Conditions{GenerationMatch: gen}).
		Delete(ctx)
}
