package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/alecthomas/kong"
	"github.com/petomalina/pot"
)

var cli struct {
	LogLevel        string `help:"debug | info | warn | error" env:"LOG_LEVEL" default:"info"`
	Bucket          string `help:"bucket name" env:"BUCKET" required:"true" short:"b"`
	Zip             string `help:"zip is the path where the zip file is stored" env:"ZIP"`
	DistributedLock bool   `help:"distributed-lock enables distributed locking of the pot" env:"DISTRIBUTED_LOCK"`
	Tracing         bool   `help:"tracing enables tracing" env:"TRACING"`
	Metrics         bool   `help:"metrics enables metrics" env:"METRICS"`
}

func main() {
	_ = kong.Parse(&cli)

	loglevel := new(slog.Level)
	err := loglevel.UnmarshalText([]byte(cli.LogLevel))
	if err != nil {
		slog.Error("failed to parse log level: %v", err)
		os.Exit(1)
	}

	h := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: *loglevel})
	slog.SetDefault(slog.New(h))

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
	if cli.DistributedLock {
		slog.Debug("distributed lock enabled")
		opts = append(opts, pot.WithDistributedLock())
	}

	if cli.Zip != "" {
		slog.Info("zip file enabled")
		opts = append(opts, pot.WithZip(cli.Zip))
	}

	if cli.Metrics {
		slog.Info("metrics enabled")
		opts = append(opts, pot.WithMetrics())
	}

	if cli.Tracing {
		slog.Info("tracing enabled")
		opts = append(opts, pot.WithTracing())
	}

	server, err := pot.NewServer(ctx, cli.Bucket, opts...)
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	if err != nil {
		slog.Error("failed to shutdown server: %v", err)
		os.Exit(1)
	}
}
