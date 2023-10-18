package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"sync"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
)

var (
	bucketNameFlag = flag.String("bucket", "", "bucket name")
	zipFlag        = flag.String("zip", "", "zip is the path where the zip file is stored")

	// rwlock is used to synchronize access to the pot files
	rwlock = sync.RWMutex{}
)

func main() {
	flag.Parse()
	if *bucketNameFlag == "" {
		slog.Error("-bucket=<name> is required, but missing")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	gcs, err := storage.NewClient(ctx)
	if err != nil {
		slog.Error("failed to create storage client: %v", err)
		os.Exit(1)
	}

	bucket := gcs.Bucket(*bucketNameFlag)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			rwlock.RLock()
			get(w, r, bucket)
			rwlock.RUnlock()

		case http.MethodPost:
			rwlock.Lock()
			create(w, r, bucket)
			rwlock.Unlock()

		case http.MethodDelete:
			rwlock.Lock()
			remove(w, r, bucket)
			rwlock.Unlock()

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}

		if *zipFlag != "" && (r.Method == http.MethodPost || r.Method == http.MethodDelete) {
			rwlock.Lock()
			zip(w, r, bucket)
			rwlock.Unlock()
		}
	})

	srv := &http.Server{Addr: ":8080"}
	go func() {
		slog.Info("starting server on :8080")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("failed to start server: %v", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	err = srv.Shutdown(ctx)
	if err != nil {
		slog.Error("failed to shutdown server: %v", err)
		os.Exit(1)
	}
}

func potPath(urlPath string) string {
	return strings.TrimPrefix(path.Join(urlPath, "data.json"), "/")
}

// get returns the content of the object if it exists, otherwise an empty object
func get(w http.ResponseWriter, r *http.Request, bucket *storage.BucketHandle) {
	content := map[string]interface{}{}
	pot := bucket.Object(potPath(r.URL.Path))

	reader, err := pot.NewReader(r.Context())
	// return an error if an unexpected error occurred
	if err != nil && err != storage.ErrObjectNotExist {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// decode the content if the object exists, otherwise the content will be empty
	if err != storage.ErrObjectNotExist {
		defer reader.Close()

		if err := json.NewDecoder(reader).Decode(&content); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// encode the content to the response
	if err := json.NewEncoder(w).Encode(content); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// create creates a new object within the pot provided by the url path. It decodes
// the whole content, adds the new object and encodes the whole content again. It
// uploads the new content to the pot.
func create(w http.ResponseWriter, r *http.Request, bucket *storage.BucketHandle) {
	content := map[string]interface{}{}
	pot := bucket.Object(potPath(r.URL.Path))

	reader, err := pot.NewReader(r.Context())
	// return an error if an unexpected error occurred
	if err != nil && err != storage.ErrObjectNotExist {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// decode the content if the object exists, otherwise the content will be empty
	if err != storage.ErrObjectNotExist {
		defer reader.Close()

		if err := json.NewDecoder(reader).Decode(&content); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// decode the new object so it can be added to the content
	obj := map[string]interface{}{}
	if err := json.NewDecoder(r.Body).Decode(&obj); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
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
	writer := pot.NewWriter(r.Context())
	defer writer.Close()
	if err := json.NewEncoder(writer).Encode(content); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// remove deletes an object from the pot provided by the url. It decodes the
// whole content, deletes the object and encodes the whole content again. It uploads
// the new content to the pot.
// The key of the object to remove is provided by the url query parameter "key".
func remove(w http.ResponseWriter, r *http.Request, bucket *storage.BucketHandle) {
	content := map[string]interface{}{}
	pot := bucket.Object(potPath(r.URL.Path))

	reader, err := pot.NewReader(r.Context())
	// return an error if an unexpected error occurred
	if err != nil && err != storage.ErrObjectNotExist {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// decode the content if the object exists, otherwise the content will be empty
	if err != storage.ErrObjectNotExist {
		defer reader.Close()

		if err := json.NewDecoder(reader).Decode(&content); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// get the key of the object to delete from the url query parameter "key"
	key := r.URL.Query().Get("key")

	// delete the object from the content
	delete(content, key)

	// encode the content to the pot
	writer := pot.NewWriter(r.Context())
	defer writer.Close()
	if err := json.NewEncoder(writer).Encode(content); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// zip creates a zip file from the whole pot provided by the zip flag.
// It creates a pot.tar.gz file on the zip path.
func zip(w http.ResponseWriter, r *http.Request, bucket *storage.BucketHandle) {
	var buf strings.Builder
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	objList := bucket.Objects(r.Context(), &storage.Query{})
	for {
		obj, err := objList.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}

		objReader, err := bucket.Object(obj.Name).NewReader(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		defer objReader.Close()

		hdr := &tar.Header{
			Name: obj.Name,
			Size: obj.Size,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}

		if _, err := io.Copy(tw, objReader); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}

	if err := tw.Close(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	if err := gzw.Close(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}

	dst := bucket.Object(path.Join(*zipFlag, "pot.tar.gz"))
	writer := dst.NewWriter(r.Context())
	defer writer.Close()

	if _, err := io.Copy(writer, strings.NewReader(buf.String())); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
