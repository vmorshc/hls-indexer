package uakino_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vmorshc/hls-indexer/internal/source"
	"github.com/vmorshc/hls-indexer/internal/source/uakino"
	"github.com/vmorshc/hls-indexer/internal/testenv"
)

func TestSampleCancellationDoesNotResolve(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var players atomic.Int64
	e := testenv.Start(t, testenv.Options{Redis: true, UAKino: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/vod/1" {
			players.Add(1)
			fmt.Fprintf(w, `new Playerjs({file:"http://%s/master.m3u8"})`, r.Host)
			return
		}
		cancel()
		<-r.Context().Done()
	})})
	c, err := uakino.New(uakino.Options{BaseURL: e.UAKino.URL, RPS: 1000, Redis: e.Redis, CacheTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ep := source.Episode{Locator: e.UAKino.URL + "/vod/1"}
	if _, err := c.Resolve(ctx, ep, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sample(ctx, ep); !errors.Is(err, context.Canceled) {
		t.Fatalf("sample error: %v", err)
	}
	s, err := c.Resolve(context.Background(), ep, false)
	if err != nil || !s.Cached || players.Load() != 1 {
		t.Fatalf("cancelled sample invalidated or resolved stream: %+v %v", s, err)
	}
}

func TestEpisodeAndStreamCacheExpire(t *testing.T) {
	ua := &testenv.FakeUAKino{}
	e := testenv.Start(t, testenv.Options{UAKino: ua, Redis: true})
	c, err := uakino.New(uakino.Options{BaseURL: e.UAKino.URL, RPS: 1000, Redis: e.Redis, CacheTTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ep := source.Episode{Locator: e.UAKino.URL + "/vod/83766"}
	if _, err := c.Sample(ctx, ep); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.CachedMedia(ctx, ep); !ok {
		t.Fatal("measured media missing")
	}
	s, err := c.Resolve(ctx, ep, false)
	if err != nil || !s.Cached || len(s.Subtitles) == 0 {
		t.Fatalf("cached stream %+v %v", s, err)
	}
	time.Sleep(1100 * time.Millisecond)
	if _, ok := c.CachedMedia(ctx, ep); ok {
		t.Fatal("episode cache did not expire")
	}
	s, err = c.Resolve(ctx, ep, false)
	if err != nil || s.Cached {
		t.Fatalf("stream cache did not expire: %+v %v", s, err)
	}
}

func TestSampleRecoversCachedStream(t *testing.T) {
	var expired atomic.Bool
	var players atomic.Int64
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/vod/1":
			players.Add(1)
			path := "old"
			if expired.Load() {
				path = "new"
			}
			fmt.Fprintf(w, `new Playerjs({file:"http://%s/%s.m3u8"})`, r.Host, path)
		case "/old.m3u8":
			http.Error(w, "expired", 403)
		case "/new.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=1920x1080\nmedia.m3u8\n")
		case "/media.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:10,\nsegment.ts\n#EXT-X-ENDLIST\n")
		default:
			http.NotFound(w, r)
		}
	})
	e := testenv.Start(t, testenv.Options{UAKino: h, Redis: true})
	c, err := uakino.New(uakino.Options{BaseURL: e.UAKino.URL, RPS: 1000, Redis: e.Redis, CacheTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ep := source.Episode{Locator: e.UAKino.URL + "/vod/1"}
	if _, err := c.Resolve(ctx, ep, false); err != nil {
		t.Fatal(err)
	}
	expired.Store(true)
	m, err := c.Sample(ctx, ep)
	if err != nil || m.Size() != 1000000 {
		t.Fatalf("sample %+v: %v", m, err)
	}
	if players.Load() != 2 {
		t.Fatalf("player requests %d", players.Load())
	}
}

func TestStreamInvalidationPreservesReplacement(t *testing.T) {
	var version atomic.Int64
	version.Store(1)
	e := testenv.Start(t, testenv.Options{Redis: true, UAKino: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `new Playerjs({file:"http://%s/%d.m3u8"})`, r.Host, version.Load())
	})})
	c, err := uakino.New(uakino.Options{BaseURL: e.UAKino.URL, RPS: 1000, Redis: e.Redis, CacheTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ep := source.Episode{Locator: e.UAKino.URL + "/vod/1"}
	old, err := c.Resolve(ctx, ep, false)
	if err != nil {
		t.Fatal(err)
	}
	version.Store(2)
	fresh, err := c.Resolve(ctx, ep, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.InvalidateStream(ctx, ep, old); err != nil {
		t.Fatal(err)
	}
	got, err := c.Resolve(ctx, ep, false)
	if err != nil || !got.Cached || got.Master != fresh.Master {
		t.Fatalf("replacement lost: %+v %v", got, err)
	}
	if err := c.InvalidateStream(ctx, ep, got); err != nil {
		t.Fatal(err)
	}
	got, err = c.Resolve(ctx, ep, false)
	if err != nil || got.Cached {
		t.Fatalf("failed stream not deleted: %+v %v", got, err)
	}
}
