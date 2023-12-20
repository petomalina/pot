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
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/api/iterator"
)

var (
	ErrNoRewriteViolated = errors.New("no-rewrite rule was violated")
)

// IsNoRewriteViolated checks whether the given error is the no-rewrite rule violation error.
func IsNoRewriteViolated(err error) bool {
	return errors.Is(err, ErrNoRewriteViolated)
}

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

	// zip is the path where the zip file is stored on the bucket. If this is empty,
	// the zip functionality is disabled.
	zip string

	// MetricsOptions is the options for metrics reporting
	MetricsOptions ServerMetricsOptions

	// TracingOptions is the options for tracing reporting
	TracingOptions ServerTracingOptions
}

type ServerMetricsOptions struct {
	Enabled bool `json:"enabled"`

	// AvgLocalLockDuration is the average duration of the local lock
	AvgLocalLockDuration metric.Float64Histogram

	// PotWrites is the number of writes to the pot
	PotWrites metric.Int64Counter

	// PotReads is the number of reads from the pot
	PotReads metric.Int64Counter

	// PotLists is the number of lists from the pot
	PotLists metric.Int64Counter

	// PotRemoves is the number of removes from the pot
	PotRemoves metric.Int64Counter
}

type ServerTracingOptions struct {
	Enabled bool `json:"enabled"`

	tracer trace.Tracer
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

	if c.MetricsOptions.Enabled {
		avgLocalLockDuration, err := otel.
			GetMeterProvider().
			Meter("pot-server").
			Float64Histogram(
				"avg_local_lock_duration",
				metric.WithDescription("avg_local_lock_duration is the average duration of the local lock"),
				metric.WithUnit("ms"),
			)
		if err != nil {
			return nil, err
		}
		c.MetricsOptions.AvgLocalLockDuration = avgLocalLockDuration

		potWrites, err := otel.
			GetMeterProvider().
			Meter("pot-server").
			Int64Counter(
				"pot_writes",
				metric.WithDescription("pot_writes is the number of writes to the pot"),
				metric.WithUnit("{call}"),
			)
		if err != nil {
			return nil, err
		}
		c.MetricsOptions.PotWrites = potWrites

		potReads, err := otel.
			GetMeterProvider().
			Meter("pot-server").
			Int64Counter(
				"pot_reads",
				metric.WithDescription("pot_reads is the number of reads from the pot"),
				metric.WithUnit("{call}"),
			)
		if err != nil {
			return nil, err
		}
		c.MetricsOptions.PotReads = potReads

		potLists, err := otel.
			GetMeterProvider().
			Meter("pot-server").
			Int64Counter(
				"pot_lists",
				metric.WithDescription("pot_lists is the number of lists from the pot"),
				metric.WithUnit("{call}"),
			)
		if err != nil {
			return nil, err
		}
		c.MetricsOptions.PotLists = potLists

		potRemoves, err := otel.
			GetMeterProvider().
			Meter("pot-server").
			Int64Counter(
				"pot_removes",
				metric.WithDescription("pot_removes is the number of removes from the pot"),
				metric.WithUnit("{call}"),
			)
		if err != nil {
			return nil, err
		}
		c.MetricsOptions.PotRemoves = potRemoves
	}

	return c, nil
}

// Option is a functional option for the server. It allows to
// configure the server via its constructor.
type Option func(*Server)

// WithDistributedLock enables distributed locking on the server.
// This slows down the process of writing, however, it prevents
// multiple processes from writing to the same pot at the same time.
func WithDistributedLock() Option {
	return func(c *Server) {
		c.distributedLock = true
	}
}

// WithZip enables the zip functionality on the server. This will
// create a tar.gz file on the bucket with all the objects in the
// pot.
func WithZip(zip string) Option {
	return func(c *Server) {
		c.zip = zip
	}
}

// WithMetrics enables metrics reporting on the server.
func WithMetrics() Option {
	return func(c *Server) {
		c.MetricsOptions.Enabled = true
	}
}

// WithTracing enables traces reporting on the server.
func WithTracing() Option {
	return func(c *Server) {
		c.TracingOptions.Enabled = true
		c.TracingOptions.tracer = otel.Tracer("pot-server")
	}
}

// potPath returns the path to the pot on the bucket path. Pot is the file
// that contains the actual data.
func (c *Server) potPath(urlPath string) string {
	return path.Join(urlPath, "data.json")
}

// CallOpts is a set of options that can be passed to the server methods.
type CallOpts struct {
	batch               bool
	norewrite           bool
	norewriteDuration   time.Duration
	lastKnownGeneration int64
}

// CallOpt is a functional option for the server methods. It allows to
// configure the server methods.
type CallOpt func(*CallOpts)

// WithBatch enables batch requests on the server methods.
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
//     (cached by the server).
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

func (s *Server) Create(ctx context.Context, dir string, r io.Reader, callOpts ...CallOpt) (*CreateResponse, error) {
	ctx, fullEnd := s.trace(ctx, "create", attribute.String("path", dir))
	defer fullEnd()

	slog.Debug("acquiring lock", slog.String("dir", dir), slog.String("method", "create"))
	defer slog.Debug("releasing lock", slog.String("dir", dir), slog.String("method", "create"))

	opts := &CallOpts{}
	for _, opt := range callOpts {
		opt(opts)
	}

	// acquire the lock for the given path on the current server and defer the release
	ctx, end := s.trace(ctx, "local-lock", attribute.String("path", dir))
	s.localLock(ctx, dir)
	defer s.localUnlock(dir)
	end()

	// if distributed locking is enabled, try to acquire the lock
	// and defer the release of the lock
	if s.distributedLock {
		slog.Debug("acquiring distributed lock", slog.String("dir", dir), slog.String("method", "create"))
		defer slog.Debug("removing distributed lock", slog.String("dir", dir), slog.String("method", "create"))

		ctx, end = s.trace(ctx, "distributed-lock", attribute.String("path", dir))

		id, err := s.lockSharedPath(ctx, dir)
		if err != nil {
			end()
			return nil, err
		}
		defer func(id string) {
			err := s.unlockSharedPath(ctx, dir, id)
			if err != nil {
				slog.Error("failed to unlock path", slog.String("dir", dir), slog.String("method", "create"), slog.String("error", err.Error()))
			}
		}(id)

		end()
	}

	ctx, end = s.trace(ctx, "read-write", attribute.String("path", dir))

	content := map[string]any{}
	pot := s.bucket.Object(s.potPath(dir))

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
	// set, the server will only allow the write if the key doesn't exist, is owned
	// by the current server or the last modification of the key is older than the
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
	end()

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

type ListPathsResponse struct {
	Paths []string `json:"paths"`
}

// ListPaths returns a list of available pot paths stored on the bucket. Each path
// is stored on gcs as a directory with a data.json file inside. This method returns
// a list of paths without the data.json suffix.
func (c *Server) ListPaths(ctx context.Context, subdir string) (*ListPathsResponse, error) {
	res := &ListPathsResponse{
		Paths: []string{},
	}

	objList := c.bucket.Objects(ctx, &storage.Query{
		Prefix: subdir,
	})
	for {
		obj, err := objList.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, err
		}

		// ignore objects that are not directories
		if !strings.HasSuffix(obj.Name, "/data.json") {
			continue
		}

		// trim the data.json suffix
		relPath := strings.TrimSuffix(obj.Name, "/data.json")

		// ignore the .potlock file
		if strings.HasSuffix(relPath, ".potlock") {
			continue
		}

		res.Paths = append(res.Paths, relPath)
	}

	return res, nil
}

func (c *Server) Get(ctx context.Context, dir string) (map[string]interface{}, error) {
	c.localRLock(ctx, dir)
	defer c.localRUnlock(dir)

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

	c.localLock(ctx, dir)
	defer c.localUnlock(dir)

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
	c.localLock(ctx, dir)
	defer c.localUnlock(dir)

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

// localLock locks the given path on the current server.
func (s *Server) localLock(ctx context.Context, dir string) {
	if s.MetricsOptions.Enabled {
		start := time.Now()
		defer func() {
			elapsed := time.Since(start)
			s.MetricsOptions.AvgLocalLockDuration.Record(ctx, float64(elapsed.Milliseconds()))
		}()
	}

	s.getOrCreateLocalLock(dir).Lock()
}

// localUnlock unlocks the given path on the current server.
func (s *Server) localUnlock(dir string) {
	s.getOrCreateLocalLock(dir).Unlock()
}

func (s *Server) localRLock(ctx context.Context, dir string) {
	if s.MetricsOptions.Enabled {
		start := time.Now()
		defer func() {
			elapsed := time.Since(start)
			s.MetricsOptions.AvgLocalLockDuration.Record(ctx, float64(elapsed.Milliseconds()))
		}()
	}

	s.getOrCreateLocalLock(dir).RLock()
}

func (s *Server) localRUnlock(dir string) {
	s.getOrCreateLocalLock(dir).RUnlock()
}

// getOrCreateLocalLock returns the lock for the given path. If the lock doesn't
func (s *Server) getOrCreateLocalLock(dir string) *sync.RWMutex {
	s.pathLocksMux.Lock()
	defer s.pathLocksMux.Unlock()

	lock, ok := s.pathLocks[dir]
	if ok {
		return lock
	}
	s.pathLocks[dir] = &sync.RWMutex{}
	return s.pathLocks[dir]
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
		return "", fmt.Errorf("failed to create lock file: %w", err)
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

func (s *Server) trace(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, func(opts ...trace.SpanEndOption)) {
	if !s.TracingOptions.Enabled {
		return ctx, func(opts ...trace.SpanEndOption) {}
	}

	ctx, span := s.TracingOptions.tracer.Start(ctx, name, trace.WithAttributes(attrs...))

	return ctx, span.End
}
