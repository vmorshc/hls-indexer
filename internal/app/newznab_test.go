package app_test

import (
	"encoding/xml"
	"net/url"
	"strings"
	"testing"

	"github.com/vmorshc/hls-indexer/internal/testenv"
)

type caps struct {
	Limits struct {
		Default string `xml:"default,attr"`
		Max     string `xml:"max,attr"`
	} `xml:"limits"`
	Searching struct {
		Search      capsMode `xml:"search"`
		TVSearch    capsMode `xml:"tv-search"`
		MovieSearch capsMode `xml:"movie-search"`
	} `xml:"searching"`
	Categories []struct {
		ID   string `xml:"id,attr"`
		Name string `xml:"name,attr"`
	} `xml:"categories>category"`
}

type capsMode struct {
	Available       string `xml:"available,attr"`
	SupportedParams string `xml:"supportedParams,attr"`
}

type nzError struct {
	XMLName     xml.Name
	Code        string `xml:"code,attr"`
	Description string `xml:"description,attr"`
}

type rss struct {
	XMLName xml.Name
	Channel struct {
		Response struct {
			Offset string `xml:"offset,attr"`
			Total  string `xml:"total,attr"`
		} `xml:"http://www.newznab.com/DTD/2010/feeds/attributes/ response"`
		Items []struct{} `xml:"item"`
	} `xml:"channel"`
}

func decodeXML(t *testing.T, body string, v any) {
	t.Helper()
	if err := xml.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
}

func TestNewznabCapsWithoutKey(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	resp, body := e.Get(t, "/indexer/api?t=caps")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
		t.Fatalf("content type %q", ct)
	}
	var c caps
	decodeXML(t, body, &c)
	if c.Limits.Default != "100" || c.Limits.Max != "100" {
		t.Errorf("limits %+v", c.Limits)
	}
	modes := map[string]capsMode{
		"q":                  c.Searching.Search,
		"q,tvdbid,season,ep": c.Searching.TVSearch,
		"q,tmdbid,imdbid":    c.Searching.MovieSearch,
	}
	for want, m := range modes {
		if m.Available != "yes" || m.SupportedParams != want {
			t.Errorf("mode %+v, want params %q", m, want)
		}
	}
	cats := map[string]string{}
	for _, cat := range c.Categories {
		cats[cat.ID] = cat.Name
	}
	if len(cats) != 2 || cats["2000"] != "Movies" || cats["5000"] != "TV" {
		t.Errorf("categories %+v", c.Categories)
	}
}

func TestNewznabErrors(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	key := "&apikey=" + testenv.IndexerKey
	tests := []struct {
		name  string
		query string
		code  string
	}{
		{"missing key", "t=search&q=x", "100"},
		{"wrong key", "t=search&q=x&apikey=wrong", "100"},
		{"wrong key before unknown t", "t=nope&apikey=wrong", "100"},
		{"missing t", "x=1" + key, "200"},
		{"get without id", "t=get" + key, "200"},
		{"unknown t", "t=nope" + key, "202"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := e.Get(t, "/indexer/api?"+tt.query)
			if resp.StatusCode != 200 {
				t.Fatalf("status %d", resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/xml") {
				t.Fatalf("content type %q", ct)
			}
			var er nzError
			decodeXML(t, body, &er)
			if er.XMLName.Local != "error" || er.Code != tt.code || er.Description == "" {
				t.Fatalf("got %+v, want error code %s", er, tt.code)
			}
		})
	}
}

func TestNewznabRSSReturnsEmptyChannel(t *testing.T) {
	e := testenv.Start(t, testenv.Options{})
	for _, q := range []string{
		"t=search",
		"t=search&q=",
		"t=tvsearch&cat=5000",
		"t=movie&cat=2000&extended=1",
		"t=tvsearch&season=1&ep=2",
	} {
		t.Run(q, func(t *testing.T) {
			resp, body := e.Get(t, "/indexer/api?"+q+"&apikey="+url.QueryEscape(testenv.IndexerKey))
			if resp.StatusCode != 200 {
				t.Fatalf("status %d", resp.StatusCode)
			}
			if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/rss+xml") {
				t.Fatalf("content type %q", ct)
			}
			var r rss
			decodeXML(t, body, &r)
			if r.XMLName.Local != "rss" || r.Channel.Response.Total != "0" || r.Channel.Response.Offset != "0" || len(r.Channel.Items) != 0 {
				t.Fatalf("got %+v from %s", r, body)
			}
		})
	}
}
