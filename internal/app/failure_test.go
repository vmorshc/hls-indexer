package app_test

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vmorshc/hls-indexer/internal/config"
	"github.com/vmorshc/hls-indexer/internal/testenv"
)

// breakage replaces the fake UAKino response for matching requests.
type breakage func(w http.ResponseWriter, r *http.Request) bool

func segmentFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "source", "uakino", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// waitHistory polls history until the job shows up there.
func waitHistory(t *testing.T, e *testenv.Env, id string) map[string]any {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; {
		for _, s := range slots(t, e, "history", "") {
			if s["nzo_id"] == id {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s not in history, queue %v", id, slots(t, e, "queue", ""))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// S1: a failure after addfile shows as history Failed with fail_message and
// leaves no files. retry starts a new job that resolves again and completes.
func TestFailedJobAndRetry(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not in PATH", bin)
		}
	}
	var broken atomic.Pointer[breakage]
	ua := &testenv.FakeUAKino{Segments: true, Hide: []string{"13405-shrek-privid-lorda-farkuada"}}
	ua.Intercept = func(w http.ResponseWriter, r *http.Request) bool {
		b := broken.Load()
		return b != nil && (*b)(w, r)
	}
	e := startSearch(t, testenv.Options{UAKino: ua, Worker: true, Configure: func(c *config.Config) {
		c.Worker.SegmentAttempts = 2
		c.Worker.SegmentBackoff = time.Millisecond
	}})
	it := e.search(t, "t=movie&tmdbid="+strconv.Itoa(testenv.ShrekTMDb)).Channel.Items[0]
	resp, err := http.Get(it.Enc.URL)
	if err != nil {
		t.Fatal(err)
	}
	nzb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	segment := func(file string, write func(w http.ResponseWriter, data []byte)) breakage {
		data := segmentFixture(t, file)
		return func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path != "/content/stream/"+file {
				return false
			}
			write(w, data)
			return true
		}
	}
	tests := []struct {
		name   string
		break_ breakage
		want   string
	}{
		{"source outage", func(w http.ResponseWriter, r *http.Request) bool {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return true
		}, "503"},
		{"missing segment", segment("segment2.ts", func(w http.ResponseWriter, _ []byte) {
			http.Error(w, "gone", http.StatusNotFound)
		}), "segment 2: 2 attempts: status 404"},
		{"truncated stream", segment("segment3.ts", func(w http.ResponseWriter, data []byte) {
			w.(http.Flusher).Flush() // no Content-Length: the cut is invisible to the download
			w.Write(data[:188*100])
		}), "validate: video duration"},
		{"codec error", segment("segment1.ts", func(w http.ResponseWriter, _ []byte) {
			w.Write([]byte("<html>not a segment</html>"))
		}), "codec probe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			broken.Store(&tt.break_)
			res := addfile(t, e.Env, nzb, it.Title+".nzb", "radarr")
			id := res["nzo_ids"].([]any)[0].(string)
			slot := waitHistory(t, e.Env, id)
			msg, _ := slot["fail_message"].(string)
			if slot["status"] != "Failed" || !strings.Contains(msg, tt.want) {
				t.Fatalf("history %v, want fail_message with %q", slot, tt.want)
			}
			for _, dir := range []string{e.Config.Paths.Incomplete, e.Config.Paths.Downloads} {
				if _, err := os.Stat(filepath.Join(dir, id)); !os.IsNotExist(err) {
					t.Errorf("%s/%s left: %v", dir, id, err)
				}
			}

			broken.Store(nil)
			retry := sab(t, e.Env, "mode=retry&value="+id)
			newID, _ := retry["nzo_id"].(string)
			if retry["status"] != true || newID == "" || newID == id {
				t.Fatalf("retry %v", retry)
			}
			slot = waitHistory(t, e.Env, newID)
			if slot["status"] != "Completed" || slot["category"] != "radarr" || slot["name"] != it.Title {
				t.Fatalf("retried job %v", slot)
			}
			for _, s := range slots(t, e.Env, "history", "") {
				if s["nzo_id"] == id {
					t.Errorf("failed job still in history: %v", s)
				}
			}
		})
	}
}

func TestRetryUnknownJob(t *testing.T) {
	e := testenv.Start(t, testenv.Options{Redis: true})
	queued := addfile(t, e, []byte(fmtNZB("uakino:312-shrek-2:movie:ab12cd34")), "x.nzb", "radarr")["nzo_ids"].([]any)[0].(string)
	for _, id := range []string{"", "hls_missing", queued} {
		got := sab(t, e, "mode=retry&value="+id)
		if got["status"] != false || got["error"] != "Unknown job" {
			t.Errorf("%q: got %v", id, got)
		}
	}
}
