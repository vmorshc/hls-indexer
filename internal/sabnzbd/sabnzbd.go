// Package sabnzbd serves the SABnzbd download client API at /downloader/api.
package sabnzbd

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/vmorshc/hls-indexer/internal/jobs"
)

// Version is the emulated SABnzbd API version.
const Version = "4.0.0"

// Categories are the job categories *arr may use.
var Categories = []string{"sonarr", "radarr"}

type Handler struct {
	apiKey      string
	completeDir string
	jobs        *jobs.Store
	hasSource   func(name string) bool
	log         *slog.Logger
}

// New builds the handler. completeDir is paths.downloads. hasSource tells
// whether a release ID prefix names a registered source.
func New(apiKey, completeDir string, store *jobs.Store, hasSource func(string) bool, log *slog.Logger) *Handler {
	return &Handler{apiKey: apiKey, completeDir: completeDir, jobs: store, hasSource: hasSource, log: log}
}

// maxRequest caps every request body. An HLS Indexer NZB is a few hundred bytes.
const maxRequest = 1 << 20

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequest)
	if err := r.ParseMultipartForm(maxRequest); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		writeError(w, "Unknown release")
		return
	}
	key := r.FormValue("apikey")
	if key == "" {
		writeError(w, "API Key Required")
		return
	}
	if subtle.ConstantTimeCompare([]byte(key), []byte(h.apiKey)) != 1 {
		writeError(w, "API Key Incorrect")
		return
	}
	switch r.FormValue("mode") {
	case "version":
		writeJSON(w, map[string]string{"version": Version})
	case "get_config":
		writeJSON(w, map[string]any{"config": h.config()})
	case "fullstatus":
		writeJSON(w, map[string]any{"status": map[string]string{"completedir": h.completeDir}})
	case "get_cats":
		writeJSON(w, map[string]any{"categories": Categories})
	case "addfile":
		h.addfile(w, r)
	case "queue":
		h.queue(w, r)
	case "history":
		h.history(w, r)
	case "retry":
		h.retry(w, r)
	default:
		writeError(w, "not implemented")
	}
}

type category struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
}

type misc struct {
	CompleteDir            string   `json:"complete_dir"`
	PreCheck               bool     `json:"pre_check"`
	EnableTVSorting        bool     `json:"enable_tv_sorting"`
	TVCategories           []string `json:"tv_categories"`
	EnableMovieSorting     bool     `json:"enable_movie_sorting"`
	MovieCategories        []string `json:"movie_categories"`
	EnableDateSorting      bool     `json:"enable_date_sorting"`
	DateCategories         []string `json:"date_categories"`
	HistoryRetentionOption string   `json:"history_retention_option"`
	HistoryRetentionNumber int      `json:"history_retention_number"`
	HistoryRetention       string   `json:"history_retention"`
}

type config struct {
	Misc       misc       `json:"misc"`
	Categories []category `json:"categories"`
	Servers    []any      `json:"servers"`
	Sorters    []any      `json:"sorters"`
}

// config reports sorting off, categories without their own dir and history kept forever.
func (h *Handler) config() config {
	cats := make([]category, len(Categories))
	for i, c := range Categories {
		cats[i] = category{Name: c}
	}
	return config{
		Misc: misc{
			CompleteDir:            h.completeDir,
			TVCategories:           []string{},
			MovieCategories:        []string{},
			DateCategories:         []string{},
			HistoryRetentionOption: "all",
			HistoryRetention:       "0",
		},
		Categories: cats,
		Servers:    []any{},
		Sorters:    []any{},
	}
}

// writeError sends a logical error: HTTP 200 with status false.
func writeError(w http.ResponseWriter, msg string) {
	writeJSON(w, map[string]any{"status": false, "error": msg})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
