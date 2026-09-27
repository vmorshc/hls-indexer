// Package config merges the embedded defaults, an optional override file and env.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"

	defaults "github.com/vmorshc/hls-indexer/config"
)

// Config holds every setting. Secrets are env-only and never read from YAML.
type Config struct {
	HTTP    HTTP    `yaml:"http"`
	Paths   Paths   `yaml:"paths"`
	Worker  Worker  `yaml:"worker"`
	UAKino  UAKino  `yaml:"uakino"`
	TMDb    TMDb    `yaml:"tmdb"`
	Secrets Secrets `yaml:"-"`
}

type HTTP struct {
	Addr      string `yaml:"addr"`
	PublicURL string `yaml:"public_url"`
}

type Paths struct {
	Incomplete string `yaml:"incomplete"`
	Downloads  string `yaml:"downloads"`
}

type Worker struct {
	Jobs               int `yaml:"jobs"`
	SegmentConcurrency int `yaml:"segment_concurrency"`
}

type UAKino struct {
	BaseURL     string   `yaml:"base_url"`
	RPS         float64  `yaml:"rps"`
	PlayerHosts []string `yaml:"player_hosts"`
}

type TMDb struct {
	BaseURL string `yaml:"base_url"`
}

type Secrets struct {
	IndexerAPIKey    Secret
	DownloaderAPIKey Secret
	TMDbAPIKey       Secret
	RedisURL         Secret
	UAKinoProxyURL   Secret
}

// Secret hides its value from fmt, slog, JSON and text encoding.
type Secret string

const redacted = "[redacted]"

func (s Secret) Reveal() string                { return string(s) }
func (s Secret) String() string                { return s.mask() }
func (s Secret) LogValue() slog.Value          { return slog.StringValue(s.mask()) }
func (s Secret) MarshalText() ([]byte, error)  { return []byte(s.mask()), nil }
func (s Secret) Format(f fmt.State, verb rune) { fmt.Fprint(f, s.mask()) }
func (s Secret) mask() string {
	if s == "" {
		return ""
	}
	return redacted
}

// LogValue logs settings and hides secrets.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("http.addr", c.HTTP.Addr),
		slog.String("http.public_url", c.HTTP.PublicURL),
		slog.String("paths.incomplete", c.Paths.Incomplete),
		slog.String("paths.downloads", c.Paths.Downloads),
		slog.Int("worker.jobs", c.Worker.Jobs),
		slog.Int("worker.segment_concurrency", c.Worker.SegmentConcurrency),
		slog.String("uakino.base_url", c.UAKino.BaseURL),
		slog.Float64("uakino.rps", c.UAKino.RPS),
		slog.String("uakino.player_hosts", strings.Join(c.UAKino.PlayerHosts, ",")),
		slog.String("tmdb.base_url", c.TMDb.BaseURL),
	)
}

// Load reads the embedded defaults, merges the override file when overridePath
// is set, then applies env vars. lookupEnv is usually os.LookupEnv.
func Load(overridePath string, lookupEnv func(string) (string, bool)) (Config, error) {
	var c Config
	if err := decode(defaults.Default, &c); err != nil {
		return Config{}, fmt.Errorf("defaults: %w", err)
	}
	if overridePath != "" {
		data, err := os.ReadFile(overridePath)
		if err != nil {
			return Config{}, fmt.Errorf("override file: %w", err)
		}
		if err := decode(data, &c); err != nil {
			return Config{}, fmt.Errorf("override file %s: %w", overridePath, err)
		}
	}
	if err := applyEnv(&c, lookupEnv); err != nil {
		return Config{}, err
	}
	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func decode(data []byte, c *Config) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return yaml.Load(data, c, yaml.WithKnownFields())
}

func applyEnv(c *Config, lookupEnv func(string) (string, bool)) error {
	strs := map[string]*string{
		"HTTP_ADDR":       &c.HTTP.Addr,
		"PUBLIC_URL":      &c.HTTP.PublicURL,
		"INCOMPLETE_DIR":  &c.Paths.Incomplete,
		"DOWNLOADS_DIR":   &c.Paths.Downloads,
		"UAKINO_BASE_URL": &c.UAKino.BaseURL,
		"TMDB_BASE_URL":   &c.TMDb.BaseURL,
	}
	for k, p := range strs {
		if v, ok := lookupEnv(k); ok {
			*p = v
		}
	}
	ints := map[string]*int{
		"WORKER_JOBS":         &c.Worker.Jobs,
		"SEGMENT_CONCURRENCY": &c.Worker.SegmentConcurrency,
	}
	for k, p := range ints {
		if v, ok := lookupEnv(k); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("env %s: %w", k, err)
			}
			*p = n
		}
	}
	if v, ok := lookupEnv("UAKINO_PLAYER_HOSTS"); ok {
		c.UAKino.PlayerHosts = strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
	}
	if v, ok := lookupEnv("UAKINO_RPS"); ok {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("env UAKINO_RPS: %w", err)
		}
		c.UAKino.RPS = f
	}
	secrets := map[string]*Secret{
		"INDEXER_API_KEY":    &c.Secrets.IndexerAPIKey,
		"DOWNLOADER_API_KEY": &c.Secrets.DownloaderAPIKey,
		"TMDB_API_KEY":       &c.Secrets.TMDbAPIKey,
		"REDIS_URL":          &c.Secrets.RedisURL,
		"UAKINO_PROXY_URL":   &c.Secrets.UAKinoProxyURL,
	}
	for k, p := range secrets {
		if v, ok := lookupEnv(k); ok {
			*p = Secret(v)
		}
	}
	return nil
}

func (c Config) validate() error {
	var errs []error
	if c.Worker.Jobs < 1 {
		errs = append(errs, errors.New("worker.jobs must be at least 1"))
	}
	if c.Worker.SegmentConcurrency < 1 {
		errs = append(errs, errors.New("worker.segment_concurrency must be at least 1"))
	}
	if c.UAKino.RPS <= 0 {
		errs = append(errs, errors.New("uakino.rps must be positive"))
	}
	return errors.Join(errs...)
}
