package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "override.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load("", env(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		HTTP:   HTTP{Addr: ":8080", PublicURL: "http://localhost:8080"},
		Paths:  Paths{Incomplete: "/data/incomplete", Downloads: "/data/downloads"},
		Worker: Worker{Jobs: 1, SegmentConcurrency: 8, X264Preset: "veryfast", X264CRF: 20, AACBitrate: "192k"},
		UAKino: UAKino{BaseURL: "https://uakino.best", RPS: 1, PlayerHosts: []string{"ashdi.vip"}},
		TMDb:   TMDb{BaseURL: "https://api.themoviedb.org/3"},
	}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("got %+v\nwant %+v", c, want)
	}
}

func TestLoadLayers(t *testing.T) {
	override := writeFile(t, "http:\n  addr: \":9000\"\nworker:\n  jobs: 3\n")
	tests := []struct {
		name     string
		override string
		env      map[string]string
		check    func(t *testing.T, c Config)
	}{
		{
			name:     "override file merges over defaults",
			override: override,
			check: func(t *testing.T, c Config) {
				if c.HTTP.Addr != ":9000" || c.Worker.Jobs != 3 {
					t.Fatalf("override not applied: %+v", c)
				}
				if c.HTTP.PublicURL != "http://localhost:8080" || c.Worker.SegmentConcurrency != 8 {
					t.Fatalf("defaults lost: %+v", c)
				}
			},
		},
		{
			name:     "env wins over override file",
			override: override,
			env:      map[string]string{"HTTP_ADDR": ":7000", "WORKER_JOBS": "5"},
			check: func(t *testing.T, c Config) {
				if c.HTTP.Addr != ":7000" || c.Worker.Jobs != 5 {
					t.Fatalf("env not applied: %+v", c)
				}
			},
		},
		{
			name: "every setting has an env var",
			env: map[string]string{
				"HTTP_ADDR":           ":1",
				"PUBLIC_URL":          "http://pub",
				"INCOMPLETE_DIR":      "/i",
				"DOWNLOADS_DIR":       "/d",
				"WORKER_JOBS":         "2",
				"SEGMENT_CONCURRENCY": "4",
				"X264_PRESET":         "slow",
				"X264_CRF":            "23",
				"AAC_BITRATE":         "128k",
				"UAKINO_BASE_URL":     "http://ua",
				"UAKINO_RPS":          "0.5",
				"UAKINO_PLAYER_HOSTS": "a.test, b.test",
				"TMDB_BASE_URL":       "http://tmdb",
			},
			check: func(t *testing.T, c Config) {
				want := Config{
					HTTP:   HTTP{Addr: ":1", PublicURL: "http://pub"},
					Paths:  Paths{Incomplete: "/i", Downloads: "/d"},
					Worker: Worker{Jobs: 2, SegmentConcurrency: 4, X264Preset: "slow", X264CRF: 23, AACBitrate: "128k"},
					UAKino: UAKino{BaseURL: "http://ua", RPS: 0.5, PlayerHosts: []string{"a.test", "b.test"}},
					TMDb:   TMDb{BaseURL: "http://tmdb"},
				}
				if !reflect.DeepEqual(c, want) {
					t.Fatalf("got %+v\nwant %+v", c, want)
				}
			},
		},
		{
			name: "secrets come from env",
			env: map[string]string{
				"INDEXER_API_KEY":    "ik",
				"DOWNLOADER_API_KEY": "dk",
				"TMDB_API_KEY":       "tk",
				"REDIS_URL":          "redis://r:6379/0",
				"UAKINO_PROXY_URL":   "http://u:p@proxy:3128",
			},
			check: func(t *testing.T, c Config) {
				s := c.Secrets
				if s.IndexerAPIKey.Reveal() != "ik" || s.DownloaderAPIKey.Reveal() != "dk" ||
					s.TMDbAPIKey.Reveal() != "tk" || s.RedisURL.Reveal() != "redis://r:6379/0" ||
					s.UAKinoProxyURL.Reveal() != "http://u:p@proxy:3128" {
					t.Fatalf("secrets not loaded: %#v", s)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Load(tt.override, env(tt.env))
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, c)
		})
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name     string
		override string
		env      map[string]string
	}{
		{name: "missing override file", override: "/nonexistent/override.yaml"},
		{name: "secret in override file", override: writeFile(t, "indexer_api_key: x\n")},
		{name: "unknown key in override file", override: writeFile(t, "http:\n  port: 1\n")},
		{name: "bad int env", env: map[string]string{"WORKER_JOBS": "many"}},
		{name: "zero jobs", env: map[string]string{"WORKER_JOBS": "0"}},
		{name: "bad rps", env: map[string]string{"UAKINO_RPS": "0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Load(tt.override, env(tt.env)); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestSecretsStayOutOfLogs(t *testing.T) {
	secrets := map[string]string{
		"INDEXER_API_KEY":    "idx-secret-1",
		"DOWNLOADER_API_KEY": "dl-secret-2",
		"TMDB_API_KEY":       "tmdb-secret-3",
		"REDIS_URL":          "redis://:redis-secret-4@r:6379/0",
		"UAKINO_PROXY_URL":   "http://u:proxy-secret-5@proxy:3128",
	}
	c, err := Load("", env(secrets))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	for _, h := range []slog.Handler{slog.NewJSONHandler(&buf, nil), slog.NewTextHandler(&buf, nil)} {
		l := slog.New(h)
		l.Info("config", "config", c)
		l.Info("secrets", "secrets", c.Secrets, "key", c.Secrets.IndexerAPIKey)
	}
	fmt.Fprintf(&buf, "%v %+v %s", c, c, c.Secrets.RedisURL)
	out := buf.String()
	for _, v := range []string{"idx-secret-1", "dl-secret-2", "tmdb-secret-3", "redis-secret-4", "proxy-secret-5"} {
		if strings.Contains(out, v) {
			t.Fatalf("secret %q leaked:\n%s", v, out)
		}
	}
	if !strings.Contains(out, "http://localhost:8080") {
		t.Fatalf("settings missing from log:\n%s", out)
	}
}
