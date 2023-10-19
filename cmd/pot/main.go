package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"

	"github.com/petomalina/pot"
)

var (
	bucketNameFlag = flag.String("bucket", "", "bucket name")
	zipFlag        = flag.String("zip", "", "zip is the path where the zip file is stored")
)

func main() {
	flag.Parse()
	if *bucketNameFlag == "" {
		slog.Error("-bucket=<name> is required, but missing")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	potClient, err := pot.NewClient(ctx, *bucketNameFlag)
	if err != nil {
		slog.Error("failed to create pot client: %v", err)
		os.Exit(1)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var err error
		content := map[string]interface{}{}

		switch r.Method {
		case http.MethodGet:
			content, err = potClient.Get(r.Context(), r.URL.Path)

		case http.MethodPost:
			err = potClient.Create(r.Context(), r.URL.Path, r.Body)

		case http.MethodDelete:
			err = potClient.Remove(r.Context(), r.URL.Path, r.URL.Query().Get("key"))

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}

		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// encode the content to the response
		if err := json.NewEncoder(w).Encode(content); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}

		if *zipFlag != "" && (r.Method == http.MethodPost || r.Method == http.MethodDelete) {
			if err := potClient.Zip(r.Context(), *zipFlag); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
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
