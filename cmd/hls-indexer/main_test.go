package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vmorshc/hls-indexer/internal/testenv"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func envOf(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

// runRole starts a role, waits until it logs ready, then stops it.
func runRole(t *testing.T, args []string, env map[string]string, ready string) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var out syncBuffer
	done := make(chan error, 1)
	go func() { done <- run(ctx, args, envOf(env), &out) }()

	deadline := time.After(10 * time.Second)
	for !strings.Contains(out.String(), ready) {
		select {
		case err := <-done:
			cancel()
			t.Fatalf("exited early: %v\n%s", err, out.String())
		case <-deadline:
			cancel()
			t.Fatalf("not ready:\n%s", out.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	return out.String()
}

func TestAPIStarts(t *testing.T) {
	testenv.Redis(t)
	override := filepath.Join(t.TempDir(), "override.yaml")
	if err := os.WriteFile(override, []byte("http:\n  public_url: \"http://from-override\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := runRole(t, []string{"api", "--config", override}, map[string]string{
		"HTTP_ADDR":          "127.0.0.1:0",
		"INDEXER_API_KEY":    "idx-secret",
		"DOWNLOADER_API_KEY": "dl-secret",
		"TMDB_API_KEY":       "tmdb-secret",
		"REDIS_URL":          testenv.RedisURL(t),
	}, "api listening")
	if !strings.Contains(out, "http://from-override") {
		t.Errorf("override not applied:\n%s", out)
	}
	for _, s := range []string{"idx-secret", "dl-secret", "tmdb-secret", testenv.RedisURL(t)} {
		if strings.Contains(out, s) {
			t.Errorf("secret %q in logs:\n%s", s, out)
		}
	}
}

func TestWorkerStarts(t *testing.T) {
	testenv.Redis(t)
	out := runRole(t, []string{"worker"}, map[string]string{"REDIS_URL": testenv.RedisURL(t)}, "worker started")
	if strings.Contains(out, testenv.RedisURL(t)) {
		t.Errorf("redis url in logs:\n%s", out)
	}
}

func TestRunErrors(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"nope"},
		{"api", "extra"},
		{"api", "--config", "/nonexistent.yaml"},
		{"api"}, // no API keys
	} {
		var out syncBuffer
		if err := run(context.Background(), args, envOf(nil), &out); err == nil {
			t.Errorf("args %q: want error", args)
		}
	}
}
