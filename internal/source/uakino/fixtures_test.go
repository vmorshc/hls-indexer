package uakino

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vmorshc/hls-indexer/internal/hls"
)

// Fixtures for the test titles in docs/uakino/rules.md. UAKINO_LIVE=1 refetches
// them from the live site before the parser tests run and overwrites testdata/.

var searchFixtures = map[string]string{
	"Ейфорія":                 "search-eiforiia.html",
	"Euphoria":                "search-euphoria.html",
	"Людина-бензопила":        "search-liudyna-benzopyla.html",
	"Chainsaw Man":            "search-chainsaw-man.html",
	"Шрек 2":                  "search-shrek-2-uk.html",
	"Shrek 2":                 "search-shrek-2.html",
	"Афера Томаса Крауна":     "search-afera-tomasa-krauna.html",
	"The Thomas Crown Affair": "search-thomas-crown-affair.html",
	"qqzzxxnothing":           "search-empty.html",
}

var titleFixtures = []string{
	"13059-eyforya-2-sezon",
	"9828-eyforya-1-sezon",
	"15577-lyudina-benzopila-1-sezon",
	"312-shrek-2",
	"2403-afera-tomasa-krauna",
}

var playerFixtures = []string{"51968", "83766", "128413"}

const liveBase = "https://uakino.best"

func TestMain(m *testing.M) {
	if os.Getenv("UAKINO_LIVE") == "1" {
		if err := refreshFixtures(); err != nil {
			fmt.Fprintln(os.Stderr, "refresh fixtures:", err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

func refreshFixtures() error {
	c, err := New(Options{BaseURL: liveBase, RPS: 1, ProxyURL: os.Getenv("UAKINO_PROXY_URL")})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	save := func(name, body string) error {
		return os.WriteFile(filepath.Join("testdata", name), []byte(body), 0o644)
	}
	for q, file := range searchFixtures {
		body, _, err := c.postSite(ctx, "/ua/", url.Values{"do": {"search"}, "subaction": {"search"}, "story": {q}})
		if err != nil {
			return err
		}
		if err := save(file, body); err != nil {
			return err
		}
	}
	for _, id := range titleFixtures {
		body, _, err := c.getSite(ctx, "/"+id+".html", nil)
		if err != nil {
			return err
		}
		if err := save("title-"+id+".html", body); err != nil {
			return err
		}
		key := strings.SplitN(id, "-", 2)[0]
		body, _, err = c.getSite(ctx, "/engine/ajax/playlists.php?news_id="+key+"&xfield=playlist",
			http.Header{"X-Requested-With": {"XMLHttpRequest"}})
		if err != nil {
			return err
		}
		if err := save("playlist-"+key+".json", body); err != nil {
			return err
		}
	}
	for _, id := range playerFixtures {
		body, _, err := c.getPlayer(ctx, "https://ashdi.vip/vod/"+id)
		if err != nil {
			return err
		}
		if err := save("player-"+id+".html", body); err != nil {
			return err
		}
	}
	// Master and best-variant media playlist of the Shrek 2 voice with subtitles.
	p, err := parsePlayer(readFixture(nil, "player-83766.html"))
	if err != nil {
		return err
	}
	body, final, err := c.getPlayer(ctx, p.File)
	if err != nil {
		return err
	}
	if err := save("master-83766.m3u8", body); err != nil {
		return err
	}
	vs, err := hls.ParseMaster(body, final)
	if err != nil {
		return err
	}
	body, _, err = c.getPlayer(ctx, hls.Best(vs).URL)
	if err != nil {
		return err
	}
	return save("media-83766.m3u8", body)
}

func readFixture(t testing.TB, name string) string {
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		if t == nil {
			panic(err)
		}
		t.Fatal(err)
	}
	return string(b)
}
