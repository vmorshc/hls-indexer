package testenv

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// FakeUAKino serves the UAKino fixtures from internal/source/uakino/testdata:
// site search, title pages, playlists, player pages, HLS playlists and VTT. Player
// and CDN links point back to the fake itself.
type FakeUAKino struct {
	mu       sync.Mutex
	requests []string
	// Hide drops search hits whose link contains one of these title IDs.
	Hide []string
	// Segments serves every media playlist as the 3 real 480p fixture segments,
	// so the worker can download and mux a stream.
	Segments bool
}

// shortMedia lists the fixture segments with their probed media durations.
// Their real EXTINF values differ by up to 2 s per segment.
const shortMedia = `#EXTM3U
#EXT-X-TARGETDURATION:9
#EXT-X-PLAYLIST-TYPE:VOD
#EXTINF:6.17,
/content/stream/segment1.ts
#EXTINF:5.00,
/content/stream/segment2.ts
#EXTINF:4.14,
/content/stream/segment3.ts
#EXT-X-ENDLIST
`

var segmentRe = regexp.MustCompile(`^/content/stream/(segment[1-3]\.ts)$`)

// fakeSearch maps a search query to its fixture. Other queries find nothing.
var fakeSearch = map[string]string{
	"Ейфорія":                 "search-eiforiia.html",
	"Euphoria":                "search-euphoria.html",
	"Людина-бензопила":        "search-liudyna-benzopyla.html",
	"Chainsaw Man":            "search-chainsaw-man.html",
	"Шрек 2":                  "search-shrek-2-uk.html",
	"Shrek 2":                 "search-shrek-2.html",
	"Афера Томаса Крауна":     "search-afera-tomasa-krauna.html",
	"The Thomas Crown Affair": "search-thomas-crown-affair.html",
}

func uakinoFixtures() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "source", "uakino", "testdata")
}

// Requests returns "METHOD path" for every request so far, search requests as "POST /ua/ <story>".
func (f *FakeUAKino) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

// Count returns how many requests start with prefix, e.g. "GET /vod/".
func (f *FakeUAKino) Count(prefix string) int {
	n := 0
	for _, r := range f.Requests() {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

var (
	ashdiRe   = regexp.MustCompile(`([a-z0-9]+\.)*ashdi\.vip`)
	titleIDRe = regexp.MustCompile(`^/(?:[a-z_]+/)*(\d+)-[a-z0-9_-]*\.html$`)
	vodRe     = regexp.MustCompile(`^/vod/(\d+)$`)
	vttRe     = regexp.MustCompile(`^/player/subtitle/([\w-]+)\.vtt$`)
)

func (f *FakeUAKino) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := r.Method + " " + r.URL.Path
	if r.URL.Path == "/ua/" {
		rec += " " + r.FormValue("story")
	}
	f.mu.Lock()
	f.requests = append(f.requests, rec)
	f.mu.Unlock()

	p := r.URL.Path
	switch {
	case r.Method == http.MethodPost && p == "/ua/":
		file, ok := fakeSearch[r.FormValue("story")]
		if !ok {
			file = "search-empty.html"
		}
		f.serve(w, r, file, f.hide)
	case strings.HasPrefix(p, "/engine/ajax/playlists.php"):
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			w.Write([]byte("error"))
			return
		}
		f.serve(w, r, "playlist-"+r.URL.Query().Get("news_id")+".json", nil)
	case titleIDRe.MatchString(p):
		// Short URLs redirect to a category path like the real site.
		if strings.Count(p, "/") == 1 {
			http.Redirect(w, r, "/category"+p, http.StatusMovedPermanently)
			return
		}
		f.serve(w, r, "title-"+strings.TrimSuffix(filepath.Base(p), ".html")+".html", nil)
	case vodRe.MatchString(p):
		file := "player-" + vodRe.FindStringSubmatch(p)[1] + ".html"
		if _, err := os.Stat(filepath.Join(uakinoFixtures(), file)); err != nil {
			file = "player-51968.html"
		}
		f.serve(w, r, file, nil)
	case vttRe.MatchString(p):
		file := "subtitle-" + vttRe.FindStringSubmatch(p)[1] + ".vtt"
		if _, err := os.Stat(filepath.Join(uakinoFixtures(), file)); err != nil {
			file = "subtitle-83766_ua.vtt"
		}
		http.ServeFile(w, r, filepath.Join(uakinoFixtures(), file))
	case strings.HasSuffix(p, ".m3u8") && regexp.MustCompile(`/hls/\d+/`).MatchString(p):
		if f.Segments {
			w.Write([]byte(shortMedia))
			return
		}
		f.serve(w, r, "media-83766.m3u8", nil)
	case f.Segments && segmentRe.MatchString(p):
		http.ServeFile(w, r, filepath.Join(uakinoFixtures(), segmentRe.FindStringSubmatch(p)[1]))
	case strings.HasSuffix(p, ".m3u8"):
		f.serve(w, r, "master-83766.m3u8", nil)
	default:
		http.NotFound(w, r)
	}
}

// serve writes a fixture with player links rewritten to this server.
func (f *FakeUAKino) serve(w http.ResponseWriter, r *http.Request, file string, edit func(string) string) {
	b, err := os.ReadFile(filepath.Join(uakinoFixtures(), file))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	body := ashdiRe.ReplaceAllString(string(b), r.Host)
	body = strings.NewReplacer("https://"+r.Host, "http://"+r.Host, `https:\/\/`+r.Host, `http:\/\/`+r.Host).Replace(body)
	if edit != nil {
		body = edit(body)
	}
	if strings.HasSuffix(file, ".json") {
		w.Header().Set("Content-Type", "application/json")
		if !json.Valid([]byte(body)) {
			http.Error(w, "bad fixture", http.StatusInternalServerError)
			return
		}
	}
	w.Write([]byte(body))
}

// hide drops search items that link to hidden title IDs.
func (f *FakeUAKino) hide(body string) string {
	const item = `<div class="movie-item short-item"`
	parts := strings.Split(body, item)
	out := parts[0]
	for _, p := range parts[1:] {
		drop := false
		for _, id := range f.Hide {
			if strings.Contains(p, "/"+id+".html") {
				drop = true
			}
		}
		if !drop {
			out += item + p
		}
	}
	return out
}
