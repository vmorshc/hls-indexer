package app_test

import (
	"fmt"
	"github.com/vmorshc/hls-indexer/internal/testenv"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCachedEpisodeOverridesVoiceEstimate(t *testing.T) {
	var duration atomic.Int64
	duration.Store(10)
	ua := &testenv.FakeUAKino{}
	ua.Intercept = func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasPrefix(r.URL.Path, "/vod/") {
			fmt.Fprintf(w, `new Playerjs({file:"http://%s/cache/master.m3u8"})`, r.Host)
			return true
		}
		if r.URL.Path == "/cache/master.m3u8" {
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=1920x1080\nmedia.m3u8\n")
			return true
		}
		if r.URL.Path == "/cache/media.m3u8" {
			fmt.Fprintf(w, "#EXTM3U\n#EXTINF:%d,\nsegment.ts\n#EXT-X-ENDLIST\n", duration.Load())
			return true
		}
		return false
	}
	e := startSearch(t, testenv.Options{UAKino: ua, Redis: true})
	e.search(t, "t=tvsearch&tvdbid=360295&season=2&ep=3")
	duration.Store(20)
	all := e.search(t, "t=tvsearch&tvdbid=360295&season=2")
	for _, it := range all.Channel.Items {
		want := int64(2000000)
		if strings.Contains(it.GUID, ":s02e03:") {
			want = 1000000
		}
		if it.Enc.Length != want {
			t.Errorf("%s size %d, want %d", it.GUID, it.Enc.Length, want)
		}
	}
	// Voice estimates for other episodes must not become measured cache entries.
	duration.Store(30)
	fifth := e.search(t, "t=tvsearch&tvdbid=360295&season=2&ep=5")
	for _, it := range fifth.Channel.Items {
		if it.Enc.Length != 3000000 {
			t.Errorf("voice estimate cached as episode data: %d", it.Enc.Length)
		}
	}
}

func TestSearchUsesCachedEpisodeMedia(t *testing.T) {
	ua := &testenv.FakeUAKino{}
	e := startSearch(t, testenv.Options{UAKino: ua, Redis: true})
	q := "t=tvsearch&tvdbid=360295&season=2&ep=3"
	first := e.search(t, q)
	before := ua.Count("GET /vod/")
	hls := ua.Count("GET /hls/")
	second := e.search(t, q)
	eq(t, "cached releases", titles(second), titles(first))
	if ua.Count("GET /vod/") != before || ua.Count("GET /hls/") != hls {
		t.Fatal("cached episode was sampled again")
	}
}
