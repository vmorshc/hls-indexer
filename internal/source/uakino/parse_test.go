package uakino

import (
	"errors"
	"math"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/vmorshc/hls-indexer/internal/hls"
	"github.com/vmorshc/hls-indexer/internal/source"
)

var siteBase, _ = url.Parse(liveBase)

// The expectations hold for the fixtures saved on 2026-09-27. A live refresh
// that breaks them means the site changed.

func TestParseSearch(t *testing.T) {
	got, err := parseSearch(readFixture(t, "search-eiforiia.html"), siteBase)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]source.Candidate{
		"13059": {ID: "13059-eyforya-2-sezon", Key: "13059", Title: "Ейфорія", Year: 2022, Season: 2, Kind: source.Series},
		"9828":  {ID: "9828-eyforya-1-sezon", Key: "9828", Title: "Ейфорія", Year: 2019, Season: 1, Kind: source.Series},
		"6054":  {ID: "6054-eyforya", Key: "6054", Title: "Ейфорія", Year: 2017, Kind: source.Movie},
	}
	byKey := map[string]source.Candidate{}
	for _, c := range got {
		byKey[c.Key] = c
	}
	for k, w := range want {
		if byKey[k] != w {
			t.Errorf("candidate %s = %+v, want %+v", k, byKey[k], w)
		}
	}
}

func TestParseSearchSkipsNewsPosts(t *testing.T) {
	got, err := parseSearch(readFixture(t, "search-liudyna-benzopyla.html"), siteBase)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("no candidates")
	}
	for _, c := range got {
		if strings.Contains(c.ID, "ljudina") || strings.Contains(c.ID, "liudyna-benzopyla-film") {
			t.Errorf("news post kept: %+v", c)
		}
	}
	if got[0].ID != "15577-lyudina-benzopila-1-sezon" || got[0].Season != 1 || got[0].Kind != source.Series {
		t.Errorf("first = %+v", got[0])
	}
}

func TestParseSearchEmptyAndForeignPage(t *testing.T) {
	got, err := parseSearch(readFixture(t, "search-empty.html"), siteBase)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty search: %v, %v", got, err)
	}
	if _, err := parseSearch("<html><title>Just a moment...</title></html>", siteBase); err == nil {
		t.Fatal("foreign page parsed as empty result")
	}
}

func pageURL(id string) *url.URL {
	u, _ := url.Parse(liveBase + "/some/category/" + id + ".html")
	return u
}

func ashdi(u *url.URL) bool {
	return u.Hostname() == "ashdi.vip" || strings.HasSuffix(u.Hostname(), ".ashdi.vip")
}

func TestParseTitleSerial(t *testing.T) {
	p, err := parseTitle(readFixture(t, "title-13059-eyforya-2-sezon.html"), pageURL("13059-eyforya-2-sezon"), ashdi)
	if err != nil {
		t.Fatal(err)
	}
	got := p.title
	if got.ID != "13059-eyforya-2-sezon" || got.Key != "13059" || got.Title != "Ейфорія" || got.Original != "Euphoria" ||
		got.Year != 2022 || got.Season != 2 || got.Kind != source.Series {
		t.Errorf("title = %+v", got)
	}
	if !got.Updated.Equal(time.Date(2026, 9, 18, 23, 15, 2, 0, time.UTC)) {
		t.Errorf("updated = %v", got.Updated)
	}
	wantSeasons := []source.SeasonLink{{Season: 1, TitleID: "9828-eyforya-1-sezon"}, {Season: 3, TitleID: "33384-eiforiia-3-sezon"}}
	if len(got.Seasons) != 2 || got.Seasons[0] != wantSeasons[0] || got.Seasons[1] != wantSeasons[1] {
		t.Errorf("seasons = %+v", got.Seasons)
	}
	if p.inlineFile != "" {
		t.Errorf("YouTube trailer taken as player: %s", p.inlineFile)
	}
}

func TestParseTitleAnimeWithoutSeasonList(t *testing.T) {
	p, err := parseTitle(readFixture(t, "title-15577-lyudina-benzopila-1-sezon.html"), pageURL("15577-lyudina-benzopila-1-sezon"), ashdi)
	if err != nil {
		t.Fatal(err)
	}
	if p.title.Title != "Людина-бензопила" || p.title.Original != "Chainsaw Man" || p.title.Season != 1 || p.title.Kind != source.Series {
		t.Errorf("title = %+v", p.title)
	}
}

func TestParseTitleMovies(t *testing.T) {
	p, err := parseTitle(readFixture(t, "title-312-shrek-2.html"), pageURL("312-shrek-2"), ashdi)
	if err != nil {
		t.Fatal(err)
	}
	if p.title.Title != "Шрек 2" || p.title.Original != "Shrek 2" || p.title.Year != 2004 || p.title.Kind != source.Movie || p.inlineFile != "" {
		t.Errorf("shrek = %+v inline %q", p.title, p.inlineFile)
	}

	p, err = parseTitle(readFixture(t, "title-2403-afera-tomasa-krauna.html"), pageURL("2403-afera-tomasa-krauna"), ashdi)
	if err != nil {
		t.Fatal(err)
	}
	if p.title.Original != "The Thomas Crown Affair" || p.title.Year != 1999 || p.title.Kind != source.Movie {
		t.Errorf("afera = %+v", p.title)
	}
	if p.inlineFile != "https://ashdi.vip/vod/128413" {
		t.Errorf("inline player = %q", p.inlineFile)
	}
	if v := inlineVoice(p.dubbing); v != "ТакТребаПродакш" {
		t.Errorf("inline voice = %q from %q", v, p.dubbing)
	}
}

func TestParseTitleRejectsForeignPage(t *testing.T) {
	if _, err := parseTitle("<html><body>error</body></html>", pageURL("1-x"), ashdi); err == nil {
		t.Fatal("want error")
	}
}

func TestParsePlaylistSerial(t *testing.T) {
	voices, serial, err := parsePlaylist(readFixture(t, "playlist-13059.json"), siteBase)
	if err != nil || !serial {
		t.Fatalf("serial=%v err=%v", serial, err)
	}
	names := []string{}
	for _, v := range voices {
		names = append(names, v.Name)
		if len(v.Episodes) != 8 {
			t.Errorf("%s: %d episodes", v.Name, len(v.Episodes))
		}
	}
	if strings.Join(names, "|") != "DniproFilm|Megogo Voice|Мова жестів" {
		t.Errorf("voices %v", names)
	}
	if e := voices[0].Episodes[0]; e.Number != 1 || e.Locator != "https://ashdi.vip/vod/51968" {
		t.Errorf("first episode %+v", e)
	}
}

func TestParsePlaylistUnequalEpisodeCounts(t *testing.T) {
	voices, _, err := parsePlaylist(readFixture(t, "playlist-15577.json"), siteBase)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, v := range voices {
		counts[v.Name] = len(v.Episodes)
	}
	if len(voices) != 10 || counts["Flame Studio"] != 3 || counts["Amanogawa"] != 12 || counts["Gwean & Maslinka"] != 12 {
		t.Errorf("counts %v", counts)
	}
}

func TestParsePlaylistMovie(t *testing.T) {
	voices, serial, err := parsePlaylist(readFixture(t, "playlist-312.json"), siteBase)
	if err != nil || serial {
		t.Fatalf("serial=%v err=%v", serial, err)
	}
	if len(voices) != 4 || voices[1].Name != "ТакТребаПродакшн" || len(voices[1].Episodes) != 1 ||
		voices[1].Episodes[0] != (source.Episode{Locator: "https://ashdi.vip/vod/83766"}) {
		t.Errorf("voices %+v", voices)
	}
}

func TestParsePlaylistNotData(t *testing.T) {
	if _, _, err := parsePlaylist(readFixture(t, "playlist-2403.json"), siteBase); !errors.Is(err, errNotData) {
		t.Fatalf("err = %v, want ERR_NOT_DATA", err)
	}
	if _, _, err := parsePlaylist("error", siteBase); err == nil {
		t.Fatal("plain `error` body parsed")
	}
	if _, _, err := parsePlaylist(`{"success":true,"response":"<div>maintenance</div>"}`, siteBase); err == nil {
		t.Fatal("playlist without items parsed as no voices")
	}
}

func TestInlineVoice(t *testing.T) {
	for in, want := range map[string]string{
		"ТакТребаПродакш (укр.)": "ТакТребаПродакш",
		"1+1":          "1+1",
		"Cine+ (укр.)": "Cine+",
		"":             "UAKino",
		"багатоголосий закадровий | DniproFilm, Megogo Voice": "UAKino",
	} {
		if got := inlineVoice(in); got != want {
			t.Errorf("inlineVoice(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParsePlayer(t *testing.T) {
	p, err := parsePlayer(readFixture(t, "player-83766.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.File, "https://ashdi.vip/") || !strings.HasSuffix(p.File, "/index.m3u8") {
		t.Errorf("file %q", p.File)
	}
	want := []subtitle{
		{"Українські", "https://ashdi.vip/player/subtitle/83766_ua.vtt"},
		{"Англійські", "https://ashdi.vip/player/subtitle/83766_en.vtt"},
	}
	if len(p.Subtitles) != 2 || p.Subtitles[0] != want[0] || p.Subtitles[1] != want[1] {
		t.Errorf("subtitles %+v", p.Subtitles)
	}
	p, err = parsePlayer(readFixture(t, "player-128413.html"))
	if err != nil || p.File == "" || len(p.Subtitles) != 0 {
		t.Errorf("inline player %+v %v", p, err)
	}
}

func TestParseHLS(t *testing.T) {
	base, _ := url.Parse("https://ashdi.vip/x/hls/token/index.m3u8")
	vs, err := hls.ParseMaster(readFixture(t, "master-83766.m3u8"), base)
	if err != nil {
		t.Fatal(err)
	}
	best := hls.Best(vs)
	if len(vs) != 3 || best.Width != 1920 || best.Height != 1080 || best.Bandwidth != 2128000 || !strings.Contains(best.URL, "/1080/") {
		t.Errorf("variants %+v", vs)
	}
	m, err := hls.ParseMedia(readFixture(t, "media-83766.m3u8"), base)
	if err != nil {
		t.Fatal(err)
	}
	// Sum of EXTINF computed with awk over the fixture.
	if !m.Ended || len(m.Segments) != 1109 || math.Abs(m.Duration()-5543.698558) > 0.001 {
		t.Errorf("media: %d segments, %.3f s, ended %v", len(m.Segments), m.Duration(), m.Ended)
	}
}

func TestSubtitleLanguage(t *testing.T) {
	for label, want := range map[string]string{
		"Українські": "ukr",
		"Українська": "ukr",
		"Англійські": "eng",
		"English":    "eng",
		"Польські":   "",
		"":           "",
	} {
		if got := subtitleLanguage(label); got != want {
			t.Errorf("subtitleLanguage(%q) = %q, want %q", label, got, want)
		}
	}
}
