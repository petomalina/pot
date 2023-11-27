package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"

	"github.com/petomalina/pot"
)

var (
	logLevelFlag        = flag.String("log-level", "info", "debug | info | warn | error")
	bucketNameFlag      = flag.String("bucket", "", "bucket name")
	zipFlag             = flag.String("zip", "", "zip is the path where the zip file is stored")
	distributedLockFlag = flag.Bool("distributed-lock", false, "distributed-lock enables distributed locking of the pot")
)

func main() {
	flag.Parse()

	loglevel := new(slog.Level)
	err := loglevel.UnmarshalText([]byte(*logLevelFlag))
	if err != nil {
		slog.Error("failed to parse log level: %v", err)
		os.Exit(1)
	}

	h := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: *loglevel})
	slog.SetDefault(slog.New(h))

	if *bucketNameFlag == "" {
		slog.Error("-bucket=<name> is required, but missing")
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	opts := []pot.Option{}
	if *distributedLockFlag {
		slog.Debug("distributed lock enabled")
		opts = append(opts, pot.WithDistributedLock())
	}

	potClient, err := pot.NewClient(ctx, *bucketNameFlag, opts...)
	if err != nil {
		slog.Error("failed to create pot client: %v", err)
		os.Exit(1)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var err error
		content := map[string]interface{}{}

		// trim the leading slash as bucket paths are relative
		relPath := strings.TrimPrefix(r.URL.Path, "/")

		callOpts := []pot.CallOpt{}
		if r.URL.Query().Has("batch") {
			callOpts = append(callOpts, pot.WithBatch())
		}

		if r.URL.Query().Has("norewrite") {
			callOpts = append(callOpts, pot.WithNoRewrite())
		}

		switch r.Method {
		case http.MethodGet:
			content, err = potClient.Get(r.Context(), relPath)

		case http.MethodPost:
			content, err = potClient.Create(r.Context(), relPath, r.Body, callOpts...)
			if err == nil {
				w.WriteHeader(http.StatusCreated)
			}

		case http.MethodDelete:
			err = potClient.Remove(r.Context(), relPath, r.URL.Query()["key"]...)

		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}

		if err != nil {
			// norewrite violation returns
			if errors.Is(err, pot.ErrNoRewriteViolated) {
				w.WriteHeader(http.StatusLocked)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
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
