// Package app wires the api and worker roles.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/vmorshc/hls-indexer/internal/config"
	"github.com/vmorshc/hls-indexer/internal/newznab"
	"github.com/vmorshc/hls-indexer/internal/sabnzbd"
)

// WorkerClientName is the Redis client name of the worker connection.
const WorkerClientName = "hls-indexer-worker"

// NewAPI builds the handler for /indexer/api and /downloader/api.
func NewAPI(cfg config.Config) (http.Handler, error) {
	var errs []error
	if cfg.Secrets.IndexerAPIKey == "" {
		errs = append(errs, errors.New("INDEXER_API_KEY is required"))
	}
	if cfg.Secrets.DownloaderAPIKey == "" {
		errs = append(errs, errors.New("DOWNLOADER_API_KEY is required"))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/indexer/api", newznab.New(cfg.Secrets.IndexerAPIKey.Reveal(), cfg.HTTP.PublicURL))
	mux.Handle("/downloader/api", sabnzbd.New(cfg.Secrets.DownloaderAPIKey.Reveal(), cfg.Paths.Downloads))
	return mux, nil
}

// RunAPI serves the API on http.addr until ctx ends.
func RunAPI(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	h, err := NewAPI(cfg)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.HTTP.Addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("api listening", "addr", cfg.HTTP.Addr)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

// RunWorker connects to Redis and idles until ctx ends. Job processing comes later.
func RunWorker(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	rdb, err := Redis(ctx, cfg, WorkerClientName)
	if err != nil {
		return err
	}
	defer rdb.Close()
	log.Info("worker idle", "jobs", cfg.Worker.Jobs)
	<-ctx.Done()
	log.Info("worker stopped")
	return nil
}

// Redis connects to REDIS_URL and pings it. Errors never include the URL.
func Redis(ctx context.Context, cfg config.Config, clientName string) (*redis.Client, error) {
	if cfg.Secrets.RedisURL == "" {
		return nil, errors.New("REDIS_URL is required")
	}
	opts, err := redis.ParseURL(cfg.Secrets.RedisURL.Reveal())
	if err != nil {
		return nil, errors.New("REDIS_URL is invalid")
	}
	opts.ClientName = clientName
	rdb := redis.NewClient(opts)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		rdb.Close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	return rdb, nil
}
