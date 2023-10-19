package pot

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strings"
	"sync"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
)

type Client struct {
	bucket *storage.BucketHandle

	rwlock sync.RWMutex
}

func NewClient(ctx context.Context, bucketName string) (*Client, error) {
	gcs, err := storage.NewClient(ctx)
	if err != nil {
		return nil, err
	}

	return &Client{
		bucket: gcs.Bucket(bucketName),
	}, nil
}

func (c *Client) potPath(urlPath string) string {
	return strings.TrimPrefix(path.Join(urlPath, "data.json"), "/")
}

func (c *Client) Create(ctx context.Context, dir string, r io.Reader) error {
	c.rwlock.Lock()
	defer c.rwlock.Unlock()

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

	// decode the new object so it can be added to the content
	obj := map[string]interface{}{}
	if err := json.NewDecoder(r).Decode(&obj); err != nil {
		return err
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
	content[key] = obj

	// encode the content to the pot
	writer := pot.NewWriter(ctx)
	defer writer.Close()
	if err := json.NewEncoder(writer).Encode(content); err != nil {
		return err
	}

	return nil
}

func (c *Client) Get(ctx context.Context, dir string) (map[string]interface{}, error) {
	c.rwlock.RLock()
	defer c.rwlock.RUnlock()

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

func (c *Client) Remove(ctx context.Context, dir, key string) error {
	c.rwlock.Lock()
	defer c.rwlock.Unlock()

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
	delete(content, key)

	// encode the content to the pot
	writer := pot.NewWriter(ctx)
	defer writer.Close()
	if err := json.NewEncoder(writer).Encode(content); err != nil {
		return err
	}

	return nil
}

func (c *Client) Zip(ctx context.Context, dir string) error {
	c.rwlock.Lock()
	defer c.rwlock.Unlock()

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
