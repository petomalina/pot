package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/petomalina/pot"
)

type Leader struct {
	ID string `json:"id"`
}

func (l Leader) Key() string {
	return "leader"
}

func main() {
	id := os.Getenv("ID")
	if id == "" {
		panic("ID is required")
	}

	ctx, done := signal.NotifyContext(context.Background(), os.Interrupt)
	defer done()

	client := pot.NewClient[Leader]("http://localhost:8080")

	primary := false

	// cleanup if the server goes down and we are the primary
	defer func() {
		if primary {
			slog.Info("releasing primary")
			err := client.Remove("test/election", "leader")
			if err != nil {
				slog.Error("failed to release", slog.String("err", err.Error()))
			}
		}
	}()

	// attempt to become the primary or renew the lease
	for {
		slog.Info("attempting election", slog.String("id", id), slog.Bool("primary", primary))
		res, err := client.Create("test/election", []Leader{{ID: id}}, pot.WithNoRewrite(time.Second*10))
		if err != nil {
			if errors.Is(err, pot.ErrNoRewriteViolated) {
				primary = false
			} else {
				slog.Error("failed", slog.String("err", err.Error()))
			}
		}

		if !primary && err == nil {
			primary = true
			slog.Info("became primary", slog.String("id", id), slog.Int64("generation", res.Generation))
		}

		select {
		case <-ctx.Done():
			slog.Info("shutting down")
			return
		case <-time.After(time.Second * 5):
		}
	}
}
