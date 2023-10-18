package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"

	"cloud.google.com/go/storage"
)

var (
	bucketName = flag.String("bucket", "", "bucket name")
)

func main() {
	flag.Parse()
	if *bucketName == "" {
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

	bucket := gcs.Bucket(*bucketName)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			get(w, r, bucket)
		case http.MethodPost:
			create(w, r, bucket)
		case http.MethodDelete:
			remove(w, r, bucket)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	return strings.TrimPrefix(path.Join(urlPath, "pot.json"), "/")
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
