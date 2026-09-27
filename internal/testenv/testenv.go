// Package testenv is the S1 harness: the API and worker run in-process, UAKino
// and TMDb are local fake servers, Redis is the separate test Redis.
package testenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
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

// RedisURL returns the URL of this test process's database on the test Redis
// or skips the test when TEST_REDIS_URL is unset. It does not flush. Call Redis
// first when the test touches data.
func RedisURL(t testing.TB) string {
	t.Helper()
	base := os.Getenv(RedisURLEnv)
	if base == "" {
		t.Skipf("%s is unset: run `docker compose up -d redis-test` and export %s=redis://localhost:6380/0", RedisURLEnv, RedisURLEnv)
	}
	claim.once.Do(func() { claim.url, claim.err = claimDB(base) })
	if claim.err != nil {
		t.Fatalf("%s: %v", RedisURLEnv, claim.err)
	}
	return claim.url
}

// Redis connects to this test process's database and flushes it. It skips the
// test when TEST_REDIS_URL is unset.
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

// Each test binary (one per package under `go test ./...`) claims its own
// database, so packages run in parallel without sharing keys. The claims live
// in the database from TEST_REDIS_URL, which tests never use for data. A claim
// expires lockTTL after its process exits.
var claim struct {
	once sync.Once
	url  string
	err  error
}

const (
	lockPrefix  = "hls-indexer-test:db:"
	lockTTL     = 10 * time.Second
	lockRefresh = 2 * time.Second
)

func claimDB(base string) (string, error) {
	opts, err := redis.ParseURL(base)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	c := redis.NewClient(opts)
	ctx := context.Background()
	n := 16
	if v, err := c.ConfigGet(ctx, "databases").Result(); err == nil {
		if d, err := strconv.Atoi(v["databases"]); err == nil {
			n = d
		}
	}
	token := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	for db := 0; db < n; db++ {
		if db == opts.DB {
			continue
		}
		key := lockPrefix + strconv.Itoa(db)
		ok, err := c.SetNX(ctx, key, token, lockTTL).Result()
		if err != nil {
			c.Close()
			return "", fmt.Errorf("claim database: %w", err)
		}
		if !ok {
			continue
		}
		go func() {
			for range time.Tick(lockRefresh) {
				c.Expire(ctx, key, lockTTL)
			}
		}()
		u.Path = "/" + strconv.Itoa(db)
		return u.String(), nil
	}
	c.Close()
	return "", fmt.Errorf("all %d databases are claimed by other test processes", n-1)
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
