// Command hls-indexer runs one role: api or worker.
//
//	hls-indexer api [--config override.yaml]
//	hls-indexer worker [--config override.yaml]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/vmorshc/hls-indexer/internal/app"
	"github.com/vmorshc/hls-indexer/internal/config"
)

const usage = "usage: hls-indexer api|worker [--config <path>]"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.LookupEnv, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "hls-indexer:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, lookupEnv func(string) (string, bool), stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	role := args[0]
	if role != "api" && role != "worker" {
		return fmt.Errorf("unknown role %q: %s", role, usage)
	}
	fs := flag.NewFlagSet(role, flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "override YAML merged over the defaults")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return errors.New(usage)
	}

	log := slog.New(slog.NewJSONHandler(stderr, nil)).With("role", role)
	cfg, err := config.Load(*configPath, lookupEnv)
	if err != nil {
		return err
	}
	log.Info("config loaded", "config", cfg)

	if role == "api" {
		return app.RunAPI(ctx, cfg, log)
	}
	return app.RunWorker(ctx, cfg, log)
}
