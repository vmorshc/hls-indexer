package app_test

import (
	"fmt"
	"github.com/vmorshc/hls-indexer/internal/config"
	"net/http"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vmorshc/hls-indexer/internal/testenv"
)

func TestRetryOverwritesStreamCache(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(err)
		}
	}
	var version atomic.Int64
	version.Store(1)
	ua := &testenv.FakeUAKino{Segments: true}
	ua.Intercept = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasPrefix(r.URL.Path, "/vod/") {
			fmt.Fprintf(w, `new Playerjs({file:"http://%s/version-%d.m3u8"})`, r.Host, version.Load())
			return true
		}
		if r.URL.Path == "/content/stream/segment1.ts" && version.Load() == 1 {
			http.Error(w, "missing", 404)
			return true
		}
		return false
	}
	e := startSearch(t, testenv.Options{UAKino: ua, Redis: true, Configure: func(c *config.Config) { c.Worker.SegmentAttempts = 1 }})
	it := e.search(t, "t=movie&tmdbid=809").Channel.Items[0]
	before := ua.Count("GET /vod/")
	enqueue := func() string {
		return addfile(t, e.Env, []byte(fmtNZB(it.GUID)), it.Title+".nzb", "radarr")["nzo_ids"].([]any)[0].(string)
	}
	id := enqueue()
	e.StartWorker(t)
	if slot := waitHistory(t, e.Env, id); slot["status"] != "Failed" {
		t.Fatalf("job: %v", slot)
	}
	version.Store(2)
	retry := sab(t, e.Env, "mode=retry&value="+id)
	next, ok := retry["nzo_id"].(string)
	if !ok {
		t.Fatalf("retry: %v", retry)
	}
	if slot := waitHistory(t, e.Env, next); slot["status"] != "Completed" {
		t.Fatalf("retry: %v", slot)
	}
	if ua.Count("GET /vod/") != before+1 {
		t.Fatal("retry did not force exactly one fresh resolve")
	}
	if slot := waitHistory(t, e.Env, enqueue()); slot["status"] != "Completed" {
		t.Fatalf("subsequent job: %v", slot)
	}
	if ua.Count("GET /vod/") != before+1 || ua.Count("GET /version-2.m3u8") != 2 {
		t.Fatal("subsequent grab did not reuse retry's replacement")
	}
}

func TestWorkerReusesCachedStream(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(err)
		}
	}
	ua := &testenv.FakeUAKino{Segments: true}
	e := startSearch(t, testenv.Options{UAKino: ua, Redis: true})
	it := e.search(t, "t=movie&tmdbid=809").Channel.Items[0]
	before := ua.Count("GET /vod/")
	id := addfile(t, e.Env, []byte(fmtNZB(it.GUID)), it.Title+".nzb", "radarr")["nzo_ids"].([]any)[0].(string)
	e.StartWorker(t)
	if slot := waitHistory(t, e.Env, id); slot["status"] != "Completed" {
		t.Fatalf("job: %v", slot)
	}
	if ua.Count("GET /vod/") != before {
		t.Fatal("worker fetched cached player again")
	}
}

func TestCachedStreamRecovery(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(err)
		}
	}
	for _, tt := range []struct {
		name, broken string
		freshFails   bool
		cold         bool
	}{
		{"master", "master", false, false},
		{"media", "media", false, false},
		{"replacement fails", "master", true, false},
		{"fresh failure", "master", true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var expired atomic.Bool
			ua := &testenv.FakeUAKino{Segments: true}
			ua.Intercept = func(w http.ResponseWriter, r *http.Request) bool {
				p := r.URL.Path
				if strings.HasPrefix(p, "/vod/") {
					version := "old"
					if expired.Load() {
						version = "new"
					}
					fmt.Fprintf(w, `new Playerjs({file:"http://%s/cache/%s/master.m3u8"})`, r.Host, version)
					return true
				}
				if !strings.HasPrefix(p, "/cache/") {
					return false
				}
				if expired.Load() && (strings.Contains(p, "/old/") && strings.HasSuffix(p, tt.broken+".m3u8") || tt.freshFails && strings.Contains(p, "/new/")) {
					http.Error(w, "expired", 403)
					return true
				}
				if strings.HasSuffix(p, "master.m3u8") {
					fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=854x480\nmedia.m3u8\n")
					return true
				}
				fmt.Fprint(w, "#EXTM3U\n#EXT-X-TARGETDURATION:9\n#EXTINF:6.17,\n/content/stream/segment1.ts\n#EXTINF:5.00,\n/content/stream/segment2.ts\n#EXTINF:4.14,\n/content/stream/segment3.ts\n#EXT-X-ENDLIST\n")
				return true
			}
			e := startSearch(t, testenv.Options{UAKino: ua, Redis: true, Configure: func(c *config.Config) {
				if tt.cold {
					c.UAKino.CacheTTL = time.Millisecond
				}
			}})
			it := e.search(t, "t=movie&tmdbid=809").Channel.Items[0]
			before := ua.Count("GET /vod/")
			expired.Store(true)
			if tt.cold {
				time.Sleep(10 * time.Millisecond)
			}
			id := addfile(t, e.Env, []byte(fmtNZB(it.GUID)), it.Title+".nzb", "radarr")["nzo_ids"].([]any)[0].(string)
			e.StartWorker(t)
			slot := waitHistory(t, e.Env, id)
			want := "Completed"
			if tt.freshFails {
				want = "Failed"
			}
			if slot["status"] != want {
				t.Fatalf("job: %v", slot)
			}
			if got := ua.Count("GET /vod/") - before; got != 1 {
				t.Fatalf("fresh resolves %d, want 1", got)
			}
			if got := ua.Count("GET /cache/new/master.m3u8"); got != 1 {
				t.Fatalf("replacement playlist attempts %d", got)
			}
			if !tt.cold && ua.Count("GET /cache/old/master.m3u8") < before+1 {
				t.Fatal("worker did not try cached URL")
			}
		})
	}
}
