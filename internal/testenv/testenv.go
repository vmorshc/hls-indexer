// Package testenv is the S1 harness: the API and worker run in-process, UAKino
// and TMDb are local fake servers, Redis is the separate test Redis.
package testenv

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/vmorshc/hls-indexer/internal/app"
	"github.com/vmorshc/hls-indexer/internal/config"
)

// RedisURLEnv names the env var with the test Redis URL.
const RedisURLEnv = "TEST_REDIS_URL"

const (
	IndexerKey    = "test-indexer-key"
	DownloaderKey = "test-downloader-key"
	TMDbKey       = "test-tmdb-key"
)

// Options plugs fakes into the environment. A nil fake fails the test on any request.
type Options struct {
	UAKino http.Handler
	TMDb   http.Handler
	// Worker starts the worker role in-process. It needs the test Redis.
	Worker bool
	// Redis gives the API the test Redis (TMDb cache). Without it the API runs
	// with no cache. Worker implies Redis.
	Redis bool
	// Configure edits the config before the roles start.
	Configure func(*config.Config)
}

type Env struct {
	Config config.Config
	API    *httptest.Server
	UAKino *httptest.Server
	TMDb   *httptest.Server
	// Redis is set when the test Redis is in use: Options.Redis or Options.Worker.
	Redis *redis.Client
}

// RedisURL returns the test Redis URL or skips the test when it is unset.
// It does not flush. Call Redis first when the test touches data.
func RedisURL(t testing.TB) string {
	t.Helper()
	u := os.Getenv(RedisURLEnv)
	if u == "" {
		t.Skipf("%s is unset: run `docker compose up -d redis-test` and export %s=redis://localhost:6380/0", RedisURLEnv, RedisURLEnv)
	}
	return u
}

// Redis connects to the test Redis and flushes it. It skips the test when
// TEST_REDIS_URL is unset.
func Redis(t testing.TB) *redis.Client {
	t.Helper()
	opts, err := redis.ParseURL(RedisURL(t))
	if err != nil {
		t.Fatalf("%s: %v", RedisURLEnv, err)
	}
	c := redis.NewClient(opts)
	t.Cleanup(func() { c.Close() })
	if err := c.FlushDB(context.Background()).Err(); err != nil {
		t.Fatalf("flush test redis: %v", err)
	}
	return c
}

// Start builds the config, starts the fakes, the API and optionally the worker.
// Everything stops when the test ends.
func Start(t testing.TB, o Options) *Env {
	t.Helper()
	e := &Env{
		UAKino: fake(t, "UAKino", o.UAKino),
		TMDb:   fake(t, "TMDb", o.TMDb),
	}

	cfg, err := config.Load("", func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	cfg.Paths.Incomplete = filepath.Join(data, "incomplete")
	cfg.Paths.Downloads = filepath.Join(data, "downloads")
	cfg.UAKino.BaseURL = e.UAKino.URL
	cfg.UAKino.RPS = 1000
	cfg.UAKino.PlayerHosts = []string{e.UAKino.Listener.Addr().(*net.TCPAddr).IP.String()}
	cfg.TMDb.BaseURL = e.TMDb.URL
	cfg.Secrets = config.Secrets{
		IndexerAPIKey:    IndexerKey,
		DownloaderAPIKey: DownloaderKey,
		TMDbAPIKey:       TMDbKey,
	}
	if o.Worker || o.Redis {
		e.Redis = Redis(t)
		cfg.Secrets.RedisURL = config.Secret(RedisURL(t))
	}
	if o.Configure != nil {
		o.Configure(&cfg)
	}

	e.API = httptest.NewUnstartedServer(nil)
	cfg.HTTP.PublicURL = "http://" + e.API.Listener.Addr().String()
	api, err := app.NewAPI(cfg, e.Redis, Logger(t))
	if err != nil {
		e.API.Close()
		t.Fatal(err)
	}
	e.API.Config.Handler = api
	e.API.Start()
	t.Cleanup(e.API.Close)
	e.Config = cfg

	if o.Worker {
		e.StartWorker(t)
	}
	return e
}

// StartWorker starts the worker role on the test Redis. Tests with
// Options.Redis use it to start the worker late, e.g. to test a restart.
func (e *Env) StartWorker(t testing.TB) {
	cfg := e.Config
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.RunWorker(ctx, cfg, Logger(t)) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("worker: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("worker did not stop")
		}
	})
}

func fake(t testing.TB, name string, h http.Handler) *httptest.Server {
	if h == nil {
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("unexpected request to fake %s: %s %s", name, r.Method, r.URL.Path)
			http.Error(w, "no fake configured", http.StatusNotImplemented)
		})
	}
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

// Logger writes to the test log. Set TEST_LOG=1 to see it with -v.
func Logger(t testing.TB) *slog.Logger {
	if os.Getenv("TEST_LOG") == "" {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return slog.New(slog.NewTextHandler(testWriter{t}, nil))
}

type testWriter struct{ t testing.TB }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}

// Get calls the API and returns the response with its body read.
func (e *Env) Get(t testing.TB, path string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(e.API.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}
