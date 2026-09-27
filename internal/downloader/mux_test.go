package downloader

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vmorshc/hls-indexer/internal/hls"
	"github.com/vmorshc/hls-indexer/internal/source"
)

var update = flag.Bool("update", false, "rewrite golden files")

// Real 480p segments of Shrek 2 (UAKino fixture). The real EXTINF values
// (6.38, 5.01, 6.30) differ from the media inside by up to 2 s per segment.
// Whole streams even this out (see downloader.md), three segments do not, so
// the test playlist carries the probed media durations.
var segments = []struct {
	file     string
	duration float64
}{
	{"segment1.ts", 6.17},
	{"segment2.ts", 5.00},
	{"segment3.ts", 4.14},
}

func segmentDir() string { return filepath.Join("..", "source", "uakino", "testdata") }

func needFFmpeg(t *testing.T) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not in PATH", bin)
		}
	}
}

func testPipeline(srv *httptest.Server) Pipeline {
	return Pipeline{HTTP: srv.Client(), Concurrency: 3, Attempts: 3, Backoff: time.Millisecond, FFmpeg: "ffmpeg", FFprobe: "ffprobe"}
}

// Short WebVTT fixtures with cues inside the 3 segments.
var vtts = []source.Subtitle{
	{Label: "Українські", Language: "ukr", URL: "/subtitle-83766_ua.vtt"},
	{Label: "Англійські", Language: "eng", URL: "/subtitle-83766_en.vtt"},
}

// segmentServer serves the fixture segments and VTT files. fail makes the first n requests of each segment fail.
func segmentServer(t *testing.T, failFirst int) (*httptest.Server, hls.Media) {
	var hits [3]atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".vtt") {
			http.ServeFile(w, r, filepath.Join(segmentDir(), filepath.Base(r.URL.Path)))
			return
		}
		for i, s := range segments {
			if r.URL.Path == "/"+s.file {
				if int(hits[i].Add(1)) <= failFirst {
					http.Error(w, "flaky", http.StatusBadGateway)
					return
				}
				http.ServeFile(w, r, filepath.Join(segmentDir(), s.file))
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	m := hls.Media{Ended: true}
	for _, s := range segments {
		m.Segments = append(m.Segments, hls.Segment{URL: srv.URL + "/" + s.file, Duration: s.duration})
	}
	return srv, m
}

func serverSubs(srv *httptest.Server) []source.Subtitle {
	var subs []source.Subtitle
	for _, s := range vtts {
		s.URL = srv.URL + s.URL
		subs = append(subs, s)
	}
	return subs
}

// S4: 3 real segments and 2 VTT tracks → MKV → ffprobe summary matches the golden file.
func TestMuxGolden(t *testing.T) {
	needFFmpeg(t)
	srv, m := segmentServer(t, 1)
	subs, err := testPipeline(srv).Subtitles(context.Background(), serverSubs(srv))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "Shrek.2.2004.480p.WEB-DL.UKR-Test.mkv")
	var done int
	var bytes int64
	err = testPipeline(srv).Mux(context.Background(), m, subs, out, func(d int, n int64) { done, bytes = d, bytes+n })
	if err != nil {
		t.Fatal(err)
	}
	if done != 3 || bytes != 614196+369044+513240 {
		t.Fatalf("progress %d segments %d bytes", done, bytes)
	}
	pr, err := testPipeline(srv).Probe(context.Background(), out)
	if err != nil {
		t.Fatal(err)
	}
	got := summary(pr)
	golden := filepath.Join("testdata", "mux.golden.json")
	if *update {
		os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/downloader -update)", err)
	}
	if got != string(want) {
		t.Fatalf("ffprobe summary\ngot  %s\nwant %s", got, want)
	}
}

// summary rounds durations to 0.1 s so the golden survives ffmpeg rounding.
func summary(pr Probe) string {
	type track struct {
		Type, Codec, Language, Title string
		Duration                     string
		Packets                      int
	}
	var ts []track
	for _, t := range pr.Tracks {
		ts = append(ts, track{t.Type, t.Codec, t.Language, t.Title, fmt.Sprintf("%.1f", t.Duration), t.Packets})
	}
	b, _ := json.MarshalIndent(map[string]any{"duration": fmt.Sprintf("%.1f", pr.Duration), "tracks": ts}, "", "  ")
	return string(b) + "\n"
}

func TestMuxFailsWhenSegmentRetriesRunOut(t *testing.T) {
	needFFmpeg(t)
	srv, m := segmentServer(t, 3)
	err := testPipeline(srv).Mux(context.Background(), m, nil, filepath.Join(t.TempDir(), "x.mkv"), func(int, int64) {})
	if err == nil || !strings.Contains(err.Error(), "segment 1") {
		t.Fatalf("err %v", err)
	}
}

func TestMuxFailsOnDurationMismatch(t *testing.T) {
	needFFmpeg(t)
	srv, m := segmentServer(t, 0)
	m.Segments[2].Duration += DurationTolerance + 1
	err := testPipeline(srv).Mux(context.Background(), m, nil, filepath.Join(t.TempDir(), "x.mkv"), func(int, int64) {})
	if err == nil || !strings.Contains(err.Error(), "duration") {
		t.Fatalf("err %v", err)
	}
}

func TestMuxFailsOnMissingSubtitle(t *testing.T) {
	needFFmpeg(t)
	srv, _ := segmentServer(t, 0)
	subs := serverSubs(srv)
	subs[1].URL = srv.URL + "/missing.vtt"
	_, err := testPipeline(srv).Subtitles(context.Background(), subs)
	if err == nil || !strings.Contains(err.Error(), "Англійські") {
		t.Fatalf("err %v", err)
	}
}

func TestValidate(t *testing.T) {
	v := Track{Type: "video", Duration: 100}
	a := Track{Type: "audio", Duration: 100.5}
	s := Track{Type: "subtitle", Duration: 40}
	tests := []struct {
		name   string
		tracks []Track
		subs   int
		ok     bool
	}{
		{"video and audio", []Track{v, a}, 0, true},
		{"no audio", []Track{v}, 0, false},
		{"two audio", []Track{v, a, a}, 0, false},
		{"short audio", []Track{v, {Type: "audio", Duration: 97}}, 0, false},
		{"two subtitles", []Track{v, a, s, s}, 2, true},
		{"missing subtitle", []Track{v, a, s}, 2, false},
		{"extra subtitle", []Track{v, a, s}, 0, false},
	}
	for _, tt := range tests {
		if err := (Probe{Tracks: tt.tracks}).Validate(100, tt.subs); (err == nil) != tt.ok {
			t.Errorf("%s: err %v", tt.name, err)
		}
	}
}
