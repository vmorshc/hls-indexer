package app_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vmorshc/hls-indexer/internal/app"
	"github.com/vmorshc/hls-indexer/internal/config"
	"github.com/vmorshc/hls-indexer/internal/testenv"
)

func TestWorkerConnectsToRedisAndIdles(t *testing.T) {
	e := testenv.Start(t, testenv.Options{Worker: true})
	deadline := time.Now().Add(5 * time.Second)
	for {
		clients, err := e.Redis.ClientList(context.Background()).Result()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(clients, "name="+app.WorkerClientName) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker client not connected:\n%s", clients)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestWorkerStopsOnCancel(t *testing.T) {
	testenv.Redis(t)
	cfg := config.Config{Secrets: config.Secrets{RedisURL: config.Secret(testenv.RedisURL(t))}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.RunWorker(ctx, cfg, testenv.Logger(t)) }()
	select {
	case err := <-done:
		t.Fatalf("worker exited early: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("worker: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop")
	}
}

func TestWorkerFailsWithoutRedis(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"unset", ""},
		{"bad url", "not-a-url"},
		{"unreachable", "redis://127.0.0.1:1/0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{Secrets: config.Secrets{RedisURL: config.Secret(tt.url)}}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := app.RunWorker(ctx, cfg, testenv.Logger(t)); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestAPIRequiresKeys(t *testing.T) {
	for _, s := range []config.Secrets{
		{DownloaderAPIKey: "d"},
		{IndexerAPIKey: "i"},
	} {
		if _, err := app.NewAPI(config.Config{Secrets: s}, nil, testenv.Logger(t)); err == nil {
			t.Errorf("secrets %v: want error", s)
		}
	}
}

func TestHarnessPlugsFakes(t *testing.T) {
	e := testenv.Start(t, testenv.Options{
		UAKino: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "uakino") }),
		TMDb:   http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "tmdb") }),
	})
	for base, want := range map[string]string{e.Config.UAKino.BaseURL: "uakino", e.Config.TMDb.BaseURL: "tmdb"} {
		resp, err := http.Get(base + "/")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(b) != want {
			t.Errorf("%s: got %q, want %q", base, b, want)
		}
	}
}

func TestHarnessFlushesRedis(t *testing.T) {
	r := testenv.Redis(t)
	if err := r.Set(context.Background(), "hls-indexer:leftover", "1", 0).Err(); err != nil {
		t.Fatal(err)
	}
	r = testenv.Redis(t)
	if n, err := r.Exists(context.Background(), "hls-indexer:leftover").Result(); err != nil || n != 0 {
		t.Fatalf("exists=%d err=%v, want flushed", n, err)
	}
}
