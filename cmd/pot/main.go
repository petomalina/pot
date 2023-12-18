package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"

	"github.com/petomalina/pot"
)

var (
	logLevelFlag        = flag.String("log-level", "info", "debug | info | warn | error")
	bucketNameFlag      = flag.String("bucket", "", "bucket name")
	zipFlag             = flag.String("zip", "", "zip is the path where the zip file is stored")
	distributedLockFlag = flag.Bool("distributed-lock", false, "distributed-lock enables distributed locking of the pot")

	tracing = flag.Bool("tracing", false, "tracing enables tracing")
	metrics = flag.Bool("metrics", false, "metrics enables metrics")
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

	shutdownOtel, err := pot.BootstrapOTEL(ctx)
	if err != nil {
		slog.Error("failed to bootstrap OTEL: %v", err)
		os.Exit(1)
	}
	defer func() {
		if err := shutdownOtel(ctx); err != nil {
			slog.Error("failed to shutdown OTEL: %v", err)
			os.Exit(1)
		}
	}()

	opts := []pot.Option{}
	if *distributedLockFlag {
		slog.Debug("distributed lock enabled")
		opts = append(opts, pot.WithDistributedLock())
	}

	if *zipFlag != "" {
		slog.Debug("zip file enabled")
		opts = append(opts, pot.WithZip(*zipFlag))
	}

	if *metrics {
		slog.Debug("metrics enabled")
		opts = append(opts, pot.WithMetrics())
	}

	if *tracing {
		slog.Debug("tracing enabled")
		opts = append(opts, pot.WithTracing())
	}

	opts = append(opts)

	server, err := pot.NewServer(ctx, *bucketNameFlag, opts...)
	if err != nil {
		slog.Error("failed to create pot client: %v", err)
		os.Exit(1)
	}

	// register pot handler
	handler := server.Routes()

	srv := &http.Server{Addr: ":8080", Handler: handler}
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
