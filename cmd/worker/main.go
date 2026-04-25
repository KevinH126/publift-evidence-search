package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/kevinhart/semantic-search/internal/config"
	"github.com/kevinhart/semantic-search/internal/service"
	"github.com/kevinhart/semantic-search/internal/store"
	"github.com/kevinhart/semantic-search/internal/worker"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := config.Load()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info("shutting down worker...")
		cancel()
	}()

	// Connect to Postgres
	pg, err := store.NewPostgres(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("failed to connect to postgres", "err", err)
		os.Exit(1)
	}
	defer pg.Close()

	// Connect to Redis
	redis, err := store.NewRedis(cfg.RedisURL)
	if err != nil {
		logger.Error("failed to connect to redis", "err", err)
		os.Exit(1)
	}
	defer redis.Close()

	// Create embedder client
	embedder := service.NewEmbedder(cfg.EmbedderURL)

	// Start worker pool (blocks until ctx cancelled)
	pool := worker.NewPool(cfg.WorkerConcurrency, pg, redis, embedder, logger)
	pool.Start(ctx)
}
