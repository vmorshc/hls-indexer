package app_test

import (
	"context"
	"encoding/xml"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vmorshc/hls-indexer/internal/config"
	"github.com/vmorshc/hls-indexer/internal/testenv"
)

type feed struct {
	Channel struct {
		Response struct {
			Offset int `xml:"offset,attr"`
			Total  int `xml:"total,attr"`
		} `xml:"http://www.newznab.com/DTD/2010/feeds/attributes/ response"`
		Items []feedItem `xml:"item"`
	} `xml:"channel"`
}

type feedItem struct {
	Title   string `xml:"title"`
	GUID    string `xml:"guid"`
	Link    string `xml:"link"`
	PubDate string `xml:"pubDate"`
	Enc     struct {
		URL    string `xml:"url,attr"`
		Length int64  `xml:"length,attr"`
		Type   string `xml:"type,attr"`
	} `xml:"enclosure"`
	Attrs []struct {
		Name  string `xml:"name,attr"`
		Value string `xml:"value,attr"`
	} `xml:"http://www.newznab.com/DTD/2010/feeds/attributes/ attr"`
}

func (i feedItem) attr(name string) string {
	for _, a := range i.Attrs {
		if a.Name == name {
			return a.Value
		}
	}
	return ""
}

type searchEnv struct {
	*testenv.Env
	ua   *testenv.FakeUAKino
	tmdb *testenv.FakeTMDb
}

func startSearch(t *testing.T, o testenv.Options) searchEnv {
	ua, tm := &testenv.FakeUAKino{}, &testenv.FakeTMDb{}
	if o.UAKino == nil {
		o.UAKino = ua
	}
	if o.TMDb == nil {
		o.TMDb = tm
	}
	return searchEnv{testenv.Start(t, o), ua, tm}
}

// search calls /indexer/api with the indexer key and decodes the feed.
func (e searchEnv) search(t *testing.T, query string) feed {
	t.Helper()
	resp, body := e.Get(t, "/indexer/api?"+query+"&apikey="+testenv.IndexerKey)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/rss+xml") {
		t.Fatalf("%s: status %d, %s", query, resp.StatusCode, body)
	}
	var f feed
	decodeXML(t, body, &f)
	if len(f.Channel.Items) > f.Channel.Response.Total {
		t.Fatalf("%d items, total %d", len(f.Channel.Items), f.Channel.Response.Total)
	}
	return f
}

func titles(f feed) []string {
	var out []string
	for _, i := range f.Channel.Items {
		out = append(out, i.Title)
	}
	sort.Strings(out)
	return out
}

func eq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}

// Size of every fixture stream: best BANDWIDTH 2128000 × 5543.698558 s of EXTINF / 8.
const fixtureSize = 1474623816

func TestTVSearchByTVDBID(t *testing.T) {
	e := startSearch(t, testenv.Options{})
	f := e.search(t, "t=tvsearch&tvdbid="+strconv.Itoa(testenv.EuphoriaTVDB)+"&season=2&ep=3&cat=5000")
	eq(t, "titles", titles(f), []string{
		"Euphoria.S02E03.1080p.WEB-DL.UKR-DniproFilm",
		"Euphoria.S02E03.1080p.WEB-DL.UKR-MegogoVoice",
		"Euphoria.S02E03.1080p.WEB-DL.UKR-MovaZhestiv",
	})
	for _, it := range f.Channel.Items {
		if !strings.HasPrefix(it.GUID, "uakino:13059-eyforya-2-sezon:s02e03:") {
			t.Errorf("guid %q", it.GUID)
		}
		if it.PubDate != "Fri, 18 Sep 2026 23:15:02 +0000" {
			t.Errorf("pubDate %q", it.PubDate)
		}
		wantURL := e.API.URL + "/indexer/api?" + url.Values{"t": {"get"}, "id": {it.GUID}, "apikey": {testenv.IndexerKey}}.Encode()
		if it.Enc.URL != wantURL || it.Link != wantURL || it.Enc.Type != "application/x-nzb" || it.Enc.Length != fixtureSize {
			t.Errorf("enclosure %+v link %q, want %s", it.Enc, it.Link, wantURL)
		}
		if it.attr("category") != "5000" || it.attr("size") != strconv.Itoa(fixtureSize) {
			t.Errorf("attrs %+v", it.Attrs)
		}
		if it.attr("tvdbid") != strconv.Itoa(testenv.EuphoriaTVDB) || it.attr("tmdbid") != strconv.Itoa(testenv.EuphoriaTMDb) || it.attr("imdb") != "8772296" {
			t.Errorf("id attrs %+v", it.Attrs)
		}
	}
	// Both TMDb titles were searched.
	for _, q := range []string{"POST /ua/ Euphoria", "POST /ua/ Ейфорія"} {
		if e.ua.Count(q) != 1 {
			t.Errorf("%s: %d requests\n%v", q, e.ua.Count(q), e.ua.Requests())
		}
	}
}

func TestTVSearchSeasonSamplesOncePerVoice(t *testing.T) {
	e := startSearch(t, testenv.Options{})
	f := e.search(t, "t=tvsearch&tvdbid="+strconv.Itoa(testenv.EuphoriaTVDB)+"&season=2")
	if f.Channel.Response.Total != 24 || len(f.Channel.Items) != 24 {
		t.Fatalf("total %d, items %d; want 3 voices × 8", f.Channel.Response.Total, len(f.Channel.Items))
	}
	if n := e.ua.Count("GET /vod/"); n != 3 {
		t.Errorf("%d player requests, want one per voice", n)
	}
	for _, it := range f.Channel.Items {
		if it.attr("size") != strconv.Itoa(fixtureSize) || !strings.Contains(it.Title, ".1080p.") {
			t.Errorf("%s size %s", it.Title, it.attr("size"))
		}
	}
}

func TestTVSearchByTitle(t *testing.T) {
	e := startSearch(t, testenv.Options{})
	f := e.search(t, "t=tvsearch&q=Euphoria&season=2&ep=3")
	eq(t, "titles", titles(f), []string{
		"Euphoria.S02E03.1080p.WEB-DL.UKR-DniproFilm",
		"Euphoria.S02E03.1080p.WEB-DL.UKR-MegogoVoice",
		"Euphoria.S02E03.1080p.WEB-DL.UKR-MovaZhestiv",
	})
	for _, it := range f.Channel.Items {
		if it.attr("tvdbid") != "" || it.attr("tmdbid") != "" || it.attr("imdb") != "" {
			t.Errorf("title search got ID attrs: %+v", it.Attrs)
		}
	}
	if e.tmdb.Count() != 0 {
		t.Errorf("title search asked TMDb %d times", e.tmdb.Count())
	}
}

func TestTVSearchFollowsSiblingSeasonLink(t *testing.T) {
	ua := &testenv.FakeUAKino{Hide: []string{"9828-eyforya-1-sezon", "33384-eiforiia-3-sezon"}}
	e := startSearch(t, testenv.Options{UAKino: ua})
	e.ua = ua
	f := e.search(t, "t=tvsearch&tvdbid="+strconv.Itoa(testenv.EuphoriaTVDB)+"&season=1&ep=2")
	if len(f.Channel.Items) == 0 {
		t.Fatalf("no releases; requests %v", ua.Requests())
	}
	for _, it := range f.Channel.Items {
		if !strings.HasPrefix(it.GUID, "uakino:9828-eyforya-1-sezon:s01e02:") || !strings.HasPrefix(it.Title, "Euphoria.S01E02.") {
			t.Errorf("%s %s", it.GUID, it.Title)
		}
	}
}

func TestTVSearchByDate(t *testing.T) {
	e := startSearch(t, testenv.Options{})
	// Season 2 aired weekly from 2022-01-09, so 2022-01-23 is episode 3.
	f := e.search(t, "t=tvsearch&tvdbid="+strconv.Itoa(testenv.EuphoriaTVDB)+"&season=2022&ep=01/23")
	if len(f.Channel.Items) != 3 {
		t.Fatalf("%d items", len(f.Channel.Items))
	}
	for _, it := range f.Channel.Items {
		if !strings.HasPrefix(it.Title, "Euphoria.S02E03.") {
			t.Errorf("title %s", it.Title)
		}
	}
}

func TestAnimeAbsoluteEpisode(t *testing.T) {
	e := startSearch(t, testenv.Options{})
	want := []string{
		"Chainsaw.Man.S01E12.1080p.WEB-DL.UKR-Amanogawa",
		"Chainsaw.Man.S01E12.1080p.WEB-DL.UKR-ChikiDUB",
		"Chainsaw.Man.S01E12.1080p.WEB-DL.UKR-Dzuski",
		"Chainsaw.Man.S01E12.1080p.WEB-DL.UKR-EspadaStudio",
		"Chainsaw.Man.S01E12.1080p.WEB-DL.UKR-FanWoxUA",
		"Chainsaw.Man.S01E12.1080p.WEB-DL.UKR-GweanMaslinka",
		"Chainsaw.Man.S01E12.1080p.WEB-DL.UKR-Lifecycle",
		"Chainsaw.Man.S01E12.1080p.WEB-DL.UKR-UKutochkuVTaverni",
		"Chainsaw.Man.S01E12.1080p.WEB-DL.UKR-Unimay",
	}
	t.Run("tvsearch q=12 with tvdbid", func(t *testing.T) {
		f := e.search(t, "t=tvsearch&tvdbid="+strconv.Itoa(testenv.ChainsawTVDB)+"&q=12")
		eq(t, "titles", titles(f), want)
	})
	t.Run("search q=Title 12", func(t *testing.T) {
		f := e.search(t, "t=search&q="+url.QueryEscape("Chainsaw Man 12")+"&cat=5000")
		eq(t, "titles", titles(f), want)
		for _, it := range f.Channel.Items {
			if it.attr("tvdbid") != "" {
				t.Errorf("title search got tvdbid")
			}
		}
	})
	t.Run("absolute past the last episode", func(t *testing.T) {
		f := e.search(t, "t=tvsearch&tvdbid="+strconv.Itoa(testenv.ChainsawTVDB)+"&q=13")
		if f.Channel.Response.Total != 0 {
			t.Errorf("total %d", f.Channel.Response.Total)
		}
	})
}

func TestUnequalEpisodeCountsPerVoice(t *testing.T) {
	e := startSearch(t, testenv.Options{})
	f := e.search(t, "t=tvsearch&tvdbid="+strconv.Itoa(testenv.ChainsawTVDB)+"&season=1&ep=3")
	if len(f.Channel.Items) != 10 {
		t.Errorf("episode 3: %d voices, want 10", len(f.Channel.Items))
	}
	f = e.search(t, "t=tvsearch&tvdbid="+strconv.Itoa(testenv.ChainsawTVDB)+"&season=1")
	if f.Channel.Response.Total != 9*12+3 {
		t.Errorf("season total %d, want 111", f.Channel.Response.Total)
	}
}

func TestMovieSearch(t *testing.T) {
	// A 2003 short that the Radarr fallback would load too. It has no fixture.
	ua := &testenv.FakeUAKino{Hide: []string{"13405-shrek-privid-lorda-farkuada"}}
	e := startSearch(t, testenv.Options{UAKino: ua})
	e.ua = ua
	t.Run("tmdbid, AJAX voices", func(t *testing.T) {
		f := e.search(t, "t=movie&tmdbid="+strconv.Itoa(testenv.ShrekTMDb)+"&cat=2000")
		eq(t, "titles", titles(f), []string{
			"Shrek.2.2004.1080p.WEB-DL.UKR-1Plus1",
			"Shrek.2.2004.1080p.WEB-DL.UKR-CinePlus",
			"Shrek.2.2004.1080p.WEB-DL.UKR-NovyiKanal",
			"Shrek.2.2004.1080p.WEB-DL.UKR-TakTrebaProdakshn",
		})
		for _, it := range f.Channel.Items {
			if !strings.HasPrefix(it.GUID, "uakino:312-shrek-2:movie:") || it.attr("category") != "2000" ||
				it.attr("tmdbid") != strconv.Itoa(testenv.ShrekTMDb) || it.attr("imdb") != "0298148" || it.attr("tvdbid") != "" {
				t.Errorf("%s %+v", it.GUID, it.Attrs)
			}
		}
	})
	t.Run("imdbid without tt, inline player", func(t *testing.T) {
		f := e.search(t, "t=movie&imdbid="+strings.TrimPrefix(testenv.CrownAffairIMDb, "tt"))
		eq(t, "titles", titles(f), []string{"The.Thomas.Crown.Affair.1999.1080p.WEB-DL.UKR-TakTrebaProdaksh"})
	})
	t.Run("q", func(t *testing.T) {
		f := e.search(t, "t=movie&q="+url.QueryEscape("The Thomas Crown Affair"))
		eq(t, "titles", titles(f), []string{"The.Thomas.Crown.Affair.1999.1080p.WEB-DL.UKR-TakTrebaProdaksh"})
	})
	t.Run("Radarr fallback q=Title Year", func(t *testing.T) {
		f := e.search(t, "t=search&q="+url.QueryEscape("Shrek 2 2004")+"&cat=2000")
		if len(f.Channel.Items) != 4 {
			t.Fatalf("titles %v", titles(f))
		}
		for _, it := range f.Channel.Items {
			if !strings.HasPrefix(it.Title, "Shrek.2.2004.1080p.") || it.attr("tmdbid") != "" {
				t.Errorf("%s %+v", it.Title, it.Attrs)
			}
		}
	})
	t.Run("unknown tmdbid", func(t *testing.T) {
		if f := e.search(t, "t=movie&tmdbid=1"); f.Channel.Response.Total != 0 {
			t.Errorf("total %d", f.Channel.Response.Total)
		}
	})
}

func TestSearchCategories(t *testing.T) {
	e := startSearch(t, testenv.Options{})
	if f := e.search(t, "t=tvsearch&q=Euphoria&season=2&cat=2000"); f.Channel.Response.Total != 0 {
		t.Errorf("tv search in movie category: total %d", f.Channel.Response.Total)
	}
	f := e.search(t, "t=search&q="+url.QueryEscape("Людина-бензопила")+"&cat=5000,5070")
	if f.Channel.Response.Total != 111 {
		t.Errorf("series category: total %d", f.Channel.Response.Total)
	}
	for _, it := range f.Channel.Items {
		if it.attr("category") != "5000" {
			t.Errorf("category %s", it.attr("category"))
		}
	}
}

func TestSearchPagingAndOrder(t *testing.T) {
	e := startSearch(t, testenv.Options{})
	base := "t=tvsearch&tvdbid=" + strconv.Itoa(testenv.ChainsawTVDB) + "&season=1"
	first := e.search(t, base)
	if first.Channel.Response.Total != 111 || len(first.Channel.Items) != 100 {
		t.Fatalf("default limit: total %d items %d", first.Channel.Response.Total, len(first.Channel.Items))
	}
	var all []string
	for off := 0; off < 111; off += 40 {
		f := e.search(t, base+"&limit=40&offset="+strconv.Itoa(off))
		if f.Channel.Response.Offset != off || f.Channel.Response.Total != 111 {
			t.Fatalf("offset %d: response %+v", off, f.Channel.Response)
		}
		for _, it := range f.Channel.Items {
			all = append(all, it.GUID)
		}
	}
	if len(all) != 111 {
		t.Fatalf("paged %d items", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1] >= all[i] {
			t.Fatalf("same pubDate, not sorted by release ID at %d: %s >= %s", i, all[i-1], all[i])
		}
	}
	for i, it := range first.Channel.Items {
		if it.GUID != all[i] {
			t.Fatalf("page 1 and paged order differ at %d", i)
		}
	}
	if f := e.search(t, base+"&limit=500"); len(f.Channel.Items) != 100 {
		t.Errorf("limit above max: %d items", len(f.Channel.Items))
	}
	if f := e.search(t, base+"&offset=200"); len(f.Channel.Items) != 0 || f.Channel.Response.Total != 111 {
		t.Errorf("offset past end: %+v", f.Channel.Response)
	}
}

func TestSearchOrdersByPubDate(t *testing.T) {
	// Seasons 1 (updated 2026-01-27 or earlier) and 2 (2026-09-18) of one series.
	ua := &testenv.FakeUAKino{Hide: []string{"33384-eiforiia-3-sezon", "6054-eyforya", "35267-eiforiia-ozyrauchys-nazad"}}
	e := startSearch(t, testenv.Options{UAKino: ua})
	f := e.search(t, "t=tvsearch&q=Euphoria&ep=1")
	var dates []time.Time
	for _, it := range f.Channel.Items {
		d, err := time.Parse(time.RFC1123Z, it.PubDate)
		if err != nil {
			t.Fatal(err)
		}
		dates = append(dates, d)
	}
	if len(dates) < 4 || dates[0].Equal(dates[len(dates)-1]) {
		t.Fatalf("want releases with two pubDates, got %v", titles(f))
	}
	for i := 1; i < len(dates); i++ {
		if dates[i].After(dates[i-1]) {
			t.Fatalf("pubDate not descending at %d: %v", i, dates)
		}
	}
}

func TestGetReturnsNZB(t *testing.T) {
	e := startSearch(t, testenv.Options{})
	id := "uakino:13059-eyforya-2-sezon:s02e03:0a3a8c0e"
	resp, body := e.Get(t, "/indexer/api?t=get&apikey="+testenv.IndexerKey+"&id="+url.QueryEscape(id))
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-nzb" {
		t.Fatalf("status %d type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var nzb struct {
		XMLName xml.Name
		Files   []struct {
			Subject string   `xml:"subject,attr"`
			Inner   []string `xml:",any"`
		} `xml:"file"`
	}
	decodeXML(t, body, &nzb)
	if nzb.XMLName.Space != "http://www.newzbin.com/DTD/2003/nzb" || nzb.XMLName.Local != "nzb" ||
		len(nzb.Files) != 1 || nzb.Files[0].Subject != "hls-indexer:"+id || len(nzb.Files[0].Inner) != 0 {
		t.Errorf("nzb %s", body)
	}
	if strings.Contains(body, "http") && !strings.Contains(body, "newzbin.com") {
		t.Errorf("nzb carries a URL: %s", body)
	}
	if len(e.ua.Requests()) != 0 || e.tmdb.Count() != 0 {
		t.Errorf("t=get called a source")
	}

	for _, bad := range []string{"nope", "other:312-shrek-2:movie:ab12cd34", "uakino:312-shrek-2:movie:xyz"} {
		_, body := e.Get(t, "/indexer/api?t=get&apikey="+testenv.IndexerKey+"&id="+url.QueryEscape(bad))
		var er nzError
		decodeXML(t, body, &er)
		if er.XMLName.Local != "error" || er.Code != "300" {
			t.Errorf("%s: %s", bad, body)
		}
	}
}

func TestSearchFromEnclosureToNZB(t *testing.T) {
	e := startSearch(t, testenv.Options{})
	f := e.search(t, "t=movie&tmdbid="+strconv.Itoa(testenv.ShrekTMDb))
	it := f.Channel.Items[0]
	resp, err := http.Get(it.Enc.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `subject="hls-indexer:`+it.GUID+`"`) {
		t.Errorf("enclosure body %s", b)
	}
}

func TestSearchSourceErrorsAre503(t *testing.T) {
	challenge := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, "<!DOCTYPE html><html><head><title>Just a moment...</title></head></html>")
	})
	dropConn := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	})
	tmdbDown := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	})
	tests := []struct {
		name  string
		o     testenv.Options
		query string
	}{
		{"uakino challenge", testenv.Options{UAKino: challenge}, "t=tvsearch&q=Euphoria"},
		{"uakino network error", testenv.Options{UAKino: dropConn}, "t=movie&q=Shrek"},
		{"uakino down during ID search", testenv.Options{UAKino: challenge}, "t=movie&tmdbid=" + strconv.Itoa(testenv.ShrekTMDb)},
		{"tmdb down", testenv.Options{TMDb: tmdbDown}, "t=tvsearch&tvdbid=" + strconv.Itoa(testenv.EuphoriaTVDB)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := startSearch(t, tt.o)
			resp, body := e.Get(t, "/indexer/api?"+tt.query+"&apikey="+testenv.IndexerKey)
			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Errorf("status %d: %s", resp.StatusCode, body)
			}
		})
	}
	t.Run("uakino unreachable", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		dead := "http://" + l.Addr().String()
		l.Close()
		e := startSearch(t, testenv.Options{Configure: func(c *config.Config) { c.UAKino.BaseURL = dead }})
		resp, _ := e.Get(t, "/indexer/api?t=search&q=Shrek&apikey="+testenv.IndexerKey)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("status %d", resp.StatusCode)
		}
	})
}

func TestTMDbCachedInRedis(t *testing.T) {
	e := startSearch(t, testenv.Options{Redis: true})
	q := "t=tvsearch&tvdbid=" + strconv.Itoa(testenv.EuphoriaTVDB) + "&season=2&ep=3"
	first := e.search(t, q)
	n := e.tmdb.Count()
	if n == 0 {
		t.Fatal("no TMDb requests")
	}
	second := e.search(t, q)
	if e.tmdb.Count() != n {
		t.Errorf("second search made %d TMDb requests", e.tmdb.Count()-n)
	}
	eq(t, "cached result", titles(second), titles(first))

	ctx := context.Background()
	keys, err := e.Redis.Keys(ctx, "*").Result()
	if err != nil || len(keys) == 0 {
		t.Fatalf("keys %v %v", keys, err)
	}
	for _, k := range keys {
		// Search stores only TMDb data: no jobs, no releases.
		if !strings.HasPrefix(k, "hls-indexer:tmdb:") {
			t.Errorf("unexpected key %s", k)
		}
		ttl := e.Redis.TTL(ctx, k).Val()
		if ttl < 29*24*time.Hour || ttl > 31*24*time.Hour {
			t.Errorf("%s ttl %v, want 1 month", k, ttl)
		}
	}
}
