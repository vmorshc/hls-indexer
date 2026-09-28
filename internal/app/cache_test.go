package app_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vmorshc/hls-indexer/internal/config"

	"github.com/vmorshc/hls-indexer/internal/testenv"
)

func TestTitleCachePolicy(t *testing.T) {
	for _, tt := range []struct {
		name, query string
		cached      bool
	}{
		{"movie", "t=movie&tmdbid=809", true},
		{"inline movie", "t=movie&tmdbid=912", true},
		{"finished season", "t=tvsearch&tvdbid=360295&season=2", true},
		{"unequal voices", "t=tvsearch&tvdbid=397934&season=1", false},
		{"extra episodes", "t=tvsearch&tvdbid=360295&season=1", false},
		{"unknown metadata", "t=tvsearch&q=Euphoria&season=2", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ua := &testenv.FakeUAKino{Hide: []string{"13405-shrek-privid-lorda-farkuada"}}
			e := startSearch(t, testenv.Options{UAKino: ua, Redis: true})
			first := e.search(t, tt.query)
			before := ua.Count("GET /engine/ajax/playlists.php")
			pages := ua.Count("GET /category")
			searches := ua.Count("POST /ua/")
			second := e.search(t, tt.query)
			if len(first.Channel.Items) == 0 || len(second.Channel.Items) != len(first.Channel.Items) {
				t.Fatal("missing releases")
			}
			if got := ua.Count("GET /engine/ajax/playlists.php"); (got == before) != tt.cached {
				t.Fatalf("playlist requests before %d after %d, cached=%v", before, got, tt.cached)
			}
			if (ua.Count("GET /category") == pages) != tt.cached {
				t.Fatal("title page cache policy differs from playlist")
			}
			if ua.Count("POST /ua/") <= searches {
				t.Fatal("search results cached")
			}
		})
	}
}

func TestSeasonMetadataFailureDoesNotFailSearch(t *testing.T) {
	tm := &testenv.FakeTMDb{}
	ua := &testenv.FakeUAKino{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/season/2") {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		tm.ServeHTTP(w, r)
	})
	e := startSearch(t, testenv.Options{UAKino: ua, TMDb: handler, Redis: true})
	q := "t=tvsearch&tvdbid=360295&season=2"
	if len(e.search(t, q).Channel.Items) == 0 {
		t.Fatal("missing releases")
	}
	before := ua.Count("GET /engine/ajax/playlists.php")
	e.search(t, q)
	if ua.Count("GET /engine/ajax/playlists.php") == before {
		t.Fatal("cached without air-date confirmation")
	}
}

func TestTitleCacheExpires(t *testing.T) {
	ua := &testenv.FakeUAKino{Hide: []string{"13405-shrek-privid-lorda-farkuada"}}
	e := startSearch(t, testenv.Options{UAKino: ua, Redis: true, Configure: func(c *config.Config) { c.UAKino.CacheTTL = time.Second }})
	e.search(t, "t=movie&tmdbid=809")
	before := ua.Count("GET /engine/ajax/playlists.php")
	e.search(t, "t=movie&tmdbid=809")
	if ua.Count("GET /engine/ajax/playlists.php") != before {
		t.Fatal("cache miss before expiry")
	}
	time.Sleep(1100 * time.Millisecond)
	e.search(t, "t=movie&tmdbid=809")
	if ua.Count("GET /engine/ajax/playlists.php") != before+1 {
		t.Fatal("cache did not expire")
	}
}

func TestFinishedSiblingSeasonCached(t *testing.T) {
	ua := &testenv.FakeUAKino{Hide: []string{"13059-eyforya-2-sezon", "33384-eiforiia-3-sezon", "6054-eyforya", "35267-eiforiia-ozyrauchys-nazad"}}
	e := startSearch(t, testenv.Options{UAKino: ua, Redis: true})
	q := "t=tvsearch&tvdbid=360295&season=2"
	first := e.search(t, q)
	before := ua.Count("GET /category/13059-")
	if before == 0 || len(first.Channel.Items) == 0 {
		t.Fatal("sibling was not loaded")
	}
	e.search(t, q)
	if ua.Count("GET /category/13059-") != before {
		t.Fatal("finished sibling was not cached")
	}
}

func TestSeasonFinalAirDateCache(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	for _, date := range []string{"2999-01-01", "", today} {
		t.Run(fmt.Sprintf("date=%s", date), func(t *testing.T) {
			tm := &testenv.FakeTMDb{}
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/season/2") {
					fmt.Fprintf(w, `{"episodes":[{"season_number":2,"episode_number":8,"air_date":%q}]}`, date)
					return
				}
				tm.ServeHTTP(w, r)
			})
			ua := &testenv.FakeUAKino{}
			e := startSearch(t, testenv.Options{UAKino: ua, TMDb: handler, Redis: true})
			e.search(t, "t=tvsearch&tvdbid=360295&season=2")
			before := ua.Count("GET /engine/ajax/playlists.php")
			e.search(t, "t=tvsearch&tvdbid=360295&season=2")
			if (ua.Count("GET /engine/ajax/playlists.php") == before) != (date == today) {
				t.Fatal("incorrect final air date cache policy")
			}
		})
	}
}
