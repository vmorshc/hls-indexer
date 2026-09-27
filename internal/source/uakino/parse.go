package uakino

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"

	"github.com/vmorshc/hls-indexer/internal/source"
)

var (
	titleIDRe     = regexp.MustCompile(`^(\d+)-[A-Za-z0-9_-]*$`)
	seasonRe      = regexp.MustCompile(`(\d+)\s*сезон`)
	ukSeasonTail  = regexp.MustCompile(`(?i)\s*\d+\s*сезон\s*$`)
	enSeasonTail  = regexp.MustCompile(`(?i)\s*(\d+\s*season|season\s*\d+)\s*$`)
	episodeRe     = regexp.MustCompile(`^Серія\s+(\d+)`)
	parenRe       = regexp.MustCompile(`\s*\([^)]*\)`)
	playerFileRe  = regexp.MustCompile(`(?s)new Playerjs\(\{.*?\bfile\s*:\s*(?:'([^']*)'|"([^"]*)")`)
	playerSubsRe  = regexp.MustCompile(`(?s)new Playerjs\(\{.*?\bsubtitle\s*:\s*(?:'([^']*)'|"([^"]*)")`)
	subtitleRe    = regexp.MustCompile(`\[([^\]]*)\]([^,\[]+)`)
	yearRe        = regexp.MustCompile(`\b(19|20)\d{2}\b`)
	errNotSite    = errors.New("unexpected page layout")
	errNotData    = errors.New("ERR_NOT_DATA")
	fallbackVoice = "UAKino"
)

// titleIDFromURL takes <news_id>-<slug> from the last path segment and drops the category path.
func titleIDFromURL(u *url.URL) (id, key string, ok bool) {
	seg := strings.TrimSuffix(path.Base(u.Path), ".html")
	m := titleIDRe.FindStringSubmatch(seg)
	if m == nil {
		return "", "", false
	}
	return seg, m[1], true
}

// parseSearch reads search hits. News posts and items without .full-quality are skipped.
func parseSearch(html string, base *url.URL) ([]source.Candidate, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, err
	}
	if doc.Find("#dle-content").Length() == 0 {
		return nil, fmt.Errorf("search: %w", errNotSite)
	}
	var out []source.Candidate
	doc.Find("#dle-content div.movie-item.short-item").Each(func(_ int, s *goquery.Selection) {
		if s.Find(".full-quality").Length() == 0 {
			return
		}
		href, _ := s.Find("a.movie-title").Attr("href")
		u, err := url.Parse(href)
		if err != nil {
			return
		}
		u = base.ResolveReference(u)
		if strings.HasPrefix(u.Path, "/news/") {
			return
		}
		id, key, ok := titleIDFromURL(u)
		if !ok {
			return
		}
		c := source.Candidate{
			ID:    id,
			Key:   key,
			Title: clean(s.Find("a.movie-title").Text()),
			Kind:  source.Movie,
		}
		s.Find(".movie-desk-item").Each(func(_ int, d *goquery.Selection) {
			if strings.Contains(d.Find(".fi-label").Text(), "Рік виходу") {
				c.Year, _ = strconv.Atoi(clean(d.Find(".deck-value").Text()))
			}
		})
		if fs := s.Find(".full-season"); fs.Length() > 0 {
			c.Kind = source.Series
			c.Season = seasonNumber(fs.Text())
		}
		out = append(out, c)
	})
	return out, nil
}

// page is a parsed title page before the playlist is loaded.
type page struct {
	title      source.Title
	dubbing    string // Озвучення text, used only for inline players
	inlineFile string // iframe#pre on a player host
}

func parseTitle(html string, pageURL *url.URL, isPlayer func(*url.URL) bool) (page, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return page{}, err
	}
	h1 := doc.Find("h1").First()
	if h1.Length() == 0 {
		return page{}, fmt.Errorf("title page: %w", errNotSite)
	}
	var p page
	t := &p.title
	t.ID, t.Key, _ = titleIDFromURL(pageURL)
	h1Text := clean(h1.Text())
	t.Title = clean(ukSeasonTail.ReplaceAllString(h1Text, ""))
	t.Original = clean(enSeasonTail.ReplaceAllString(clean(doc.Find(".origintitle").First().Text()), ""))
	doc.Find(".film-info .fi-item, .film-info .fi-item-s").Each(func(_ int, s *goquery.Selection) {
		label := s.Find(".fi-label").Text()
		desc := clean(s.Find(".fi-desc").Text())
		switch {
		case strings.Contains(label, "Рік виходу"):
			t.Year, _ = strconv.Atoi(yearRe.FindString(desc))
		case strings.Contains(strings.ToLower(label), "озвучення"):
			if p.dubbing == "" {
				p.dubbing = desc
			}
		}
	})
	if dt, ok := doc.Find(".mov-date time[datetime]").First().Attr("datetime"); ok {
		t.Updated, _ = time.Parse("2006-01-02T15:04:05", dt)
	}
	if active := doc.Find("ul.seasons li.season-active").First(); active.Length() > 0 {
		t.Season = seasonNumber(active.Text())
	} else if ukSeasonTail.MatchString(h1Text) {
		t.Season = seasonNumber(h1Text)
	}
	doc.Find("ul.seasons li a[href]").Each(func(_ int, a *goquery.Selection) {
		href, _ := a.Attr("href")
		u, err := url.Parse(href)
		if err != nil {
			return
		}
		id, _, ok := titleIDFromURL(pageURL.ResolveReference(u))
		if n := seasonNumber(a.Text()); ok && n > 0 {
			t.Seasons = append(t.Seasons, source.SeasonLink{Season: n, TitleID: id})
		}
	})
	doc.Find("iframe#pre[src]").EachWithBreak(func(_ int, f *goquery.Selection) bool {
		src, _ := f.Attr("src")
		u, err := url.Parse(src)
		if err != nil {
			return true
		}
		u = pageURL.ResolveReference(u)
		if isPlayer(u) {
			p.inlineFile = u.String()
			return false
		}
		return true
	})
	if t.Season > 0 || doc.Find("ul.seasons").Length() > 0 {
		t.Kind = source.Series
		if t.Season == 0 {
			t.Season = 1
		}
	} else {
		t.Kind = source.Movie
	}
	return p, nil
}

// inlineVoice names the voice of an inline player from the dubbing text:
// one name without parentheses, else "UAKino".
func inlineVoice(dubbing string) string {
	v := clean(parenRe.ReplaceAllString(dubbing, ""))
	if v == "" || strings.ContainsAny(v, ",|") {
		return fallbackVoice
	}
	return v
}

// parsePlaylist reads voices and episodes from the playlist JSON. data-file
// resolves against the playlist URL, so protocol-relative links get https:.
// It returns errNotData for ERR_NOT_DATA.
func parsePlaylist(body string, base *url.URL) (voices []source.Voice, serial bool, err error) {
	var resp struct {
		Success  bool   `json:"success"`
		Response string `json:"response"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, false, fmt.Errorf("playlist: %w", errNotSite)
	}
	if !resp.Success {
		if resp.Message == "ERR_NOT_DATA" {
			return nil, false, errNotData
		}
		return nil, false, fmt.Errorf("playlist: %s", resp.Message)
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(resp.Response))
	if err != nil {
		return nil, false, err
	}
	if doc.Find(".playlists-videos .playlists-items").Length() == 0 {
		return nil, false, fmt.Errorf("playlist: %w", errNotSite)
	}
	index := map[string]int{}
	seen := map[string]bool{}
	doc.Find(".playlists-videos .playlists-items li[data-file]").Each(func(_ int, li *goquery.Selection) {
		file, _ := li.Attr("data-file")
		voice, _ := li.Attr("data-voice")
		voice = clean(voice)
		u, err := url.Parse(strings.TrimSpace(file))
		if err != nil || file == "" {
			return
		}
		ep := source.Episode{Locator: base.ResolveReference(u).String()}
		text := clean(li.Text())
		if m := episodeRe.FindStringSubmatch(text); m != nil {
			ep.Number, _ = strconv.Atoi(m[1])
			serial = true
		}
		if voice == "" {
			voice = text
		}
		k := voice + "\x00" + strconv.Itoa(ep.Number)
		if seen[k] {
			return
		}
		seen[k] = true
		i, ok := index[voice]
		if !ok {
			i = len(voices)
			index[voice] = i
			voices = append(voices, source.Voice{Name: voice})
		}
		voices[i].Episodes = append(voices[i].Episodes, ep)
	})
	return voices, serial, nil
}

// player is the parsed Playerjs config of a player page.
type player struct {
	File      string
	Subtitles []subtitle
}

type subtitle struct {
	Label string
	URL   string
}

func parsePlayer(html string) (player, error) {
	m := playerFileRe.FindStringSubmatch(html)
	if m == nil || strings.TrimSpace(m[1]+m[2]) == "" {
		return player{}, fmt.Errorf("player page: %w", errNotSite)
	}
	p := player{File: strings.TrimSpace(m[1] + m[2])}
	if s := playerSubsRe.FindStringSubmatch(html); s != nil {
		for _, sm := range subtitleRe.FindAllStringSubmatch(s[1]+s[2], -1) {
			p.Subtitles = append(p.Subtitles, subtitle{Label: strings.TrimSpace(sm[1]), URL: strings.TrimSpace(sm[2])})
		}
	}
	return p, nil
}

// subtitleLanguage maps a player subtitle label to an ISO 639-2 code, "" if unknown.
func subtitleLanguage(label string) string {
	l := strings.ToLower(label)
	switch {
	case strings.HasPrefix(l, "укр"):
		return "ukr"
	case strings.HasPrefix(l, "англ"), strings.HasPrefix(l, "eng"):
		return "eng"
	}
	return ""
}

func seasonNumber(s string) int {
	m := seasonRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// clean collapses whitespace.
func clean(s string) string { return strings.Join(strings.Fields(s), " ") }
