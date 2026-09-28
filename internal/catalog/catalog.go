// Package catalog runs the search flow: query → TMDb → sources → sorted releases.
// It is the only package that combines TMDb and sources.
package catalog

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/vmorshc/hls-indexer/internal/metadata/tmdb"
	"github.com/vmorshc/hls-indexer/internal/release"
	"github.com/vmorshc/hls-indexer/internal/source"
)

// Newznab categories.
const (
	CatMovies = 2000
	CatTV     = 5000
)

// Type is the Newznab search function.
type Type int

const (
	TVSearch Type = iota + 1
	MovieSearch
	GenericSearch
)

// Query is one search request. Season and Ep keep the raw Newznab values so the
// flow can tell a date search (season=2026, ep=09/15) from an episode search.
type Query struct {
	Type       Type
	Q          string
	TVDBID     int
	TMDBID     int
	IMDBID     string // without tt
	Season     int
	Ep         string
	Categories []int
	Offset     int
	Limit      int
}

// Release is one search result.
type Release struct {
	ID       string
	Title    string
	PubDate  time.Time
	Size     int64
	Category int
	// Set only when the request carried an ID and TMDb matched it.
	TVDBID int
	TMDBID int
	IMDBID string // with tt
}

type Result struct {
	Total    int
	Releases []Release
}

type Catalog struct {
	tmdb    *tmdb.Client
	sources []source.Source
}

func New(t *tmdb.Client, sources ...source.Source) *Catalog {
	return &Catalog{tmdb: t, sources: sources}
}

// HasSource reports whether a release ID prefix names a registered source.
func (c *Catalog) HasSource(name string) bool {
	for _, s := range c.sources {
		if s.Name() == name {
			return true
		}
	}
	return false
}

// plan is the resolved request: what to search for and how to filter.
type plan struct {
	show    *tmdb.Show
	names   []string
	kinds   map[source.Kind]bool
	year    int // movie year or season air year; 0 means no filter
	season  int
	episode int
	// ID requests only.
	strict  bool   // title must match one of names
	display string // release name from TMDb
	tvdbID  int
	tmdbID  int
	imdbID  string
}

// Search runs the flow in docs/architecture/releases.md.
func (c *Catalog) Search(ctx context.Context, q Query) (Result, error) {
	p, ok, err := c.plan(ctx, q)
	if err != nil || !ok {
		return Result{}, err
	}
	var all []candidate
	for _, src := range c.sources {
		titles, err := c.titles(ctx, src, p)
		if err != nil {
			return Result{}, err
		}
		all = append(all, c.releases(src, titles, p, q.Categories)...)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].rel.PubDate.Equal(all[j].rel.PubDate) {
			return all[i].rel.PubDate.After(all[j].rel.PubDate)
		}
		return all[i].rel.ID < all[j].rel.ID
	})
	res := Result{Total: len(all)}
	page := paginate(all, q.Offset, q.Limit)
	if err := c.sample(ctx, page); err != nil {
		return Result{}, err
	}
	for _, r := range page {
		res.Releases = append(res.Releases, r.rel)
	}
	return res, nil
}

var (
	trailingYear = regexp.MustCompile(`^(.*\S)\s+\(?((?:19|20)\d{2})\)?$`)
	trailingNum  = regexp.MustCompile(`^(.*\S)\s+(\d{1,4})$`)
)

// splitYear strips a trailing year: "Title 1999" → "Title", 1999.
func splitYear(q string) (string, int) {
	if m := trailingYear.FindStringSubmatch(q); m != nil {
		y, _ := strconv.Atoi(m[2])
		return m[1], y
	}
	return q, 0
}

func wantKinds(q Query) map[source.Kind]bool {
	switch q.Type {
	case TVSearch:
		return map[source.Kind]bool{source.Series: inCats(q.Categories, CatTV)}
	case MovieSearch:
		return map[source.Kind]bool{source.Movie: inCats(q.Categories, CatMovies)}
	}
	return map[source.Kind]bool{source.Series: inCats(q.Categories, CatTV), source.Movie: inCats(q.Categories, CatMovies)}
}

// inCats reports whether cat or one of its subcategories was requested. No cats means all.
func inCats(cats []int, cat int) bool {
	if len(cats) == 0 {
		return true
	}
	for _, c := range cats {
		if c/1000*1000 == cat {
			return true
		}
	}
	return false
}

func (c *Catalog) plan(ctx context.Context, q Query) (plan, bool, error) {
	p := plan{kinds: wantKinds(q)}
	if !p.kinds[source.Series] && !p.kinds[source.Movie] {
		return p, false, nil
	}
	query := strings.TrimSpace(q.Q)
	switch {
	case q.Type == TVSearch && q.TVDBID > 0:
		show, ok, err := c.tmdb.ShowByTVDB(ctx, q.TVDBID)
		if err != nil || !ok {
			return p, false, err
		}
		p.strict, p.display = true, show.Title
		p.tvdbID, p.tmdbID, p.imdbID = q.TVDBID, show.ID, show.IMDBID
		p.names = []string{show.Title, show.TitleUK}
		abs, _ := strconv.Atoi(query)
		return c.episode(ctx, p, show, q, abs)

	case q.Type == TVSearch:
		if query == "" {
			return p, false, nil
		}
		p.names = []string{query}
		if isDate(q) {
			show, ok, err := c.tmdb.SearchShow(ctx, query)
			if err != nil || !ok {
				return p, false, err
			}
			p.names = append(p.names, show.Title, show.TitleUK)
			return c.episode(ctx, p, show, q, 0)
		}
		p.season, p.episode = q.Season, atoi(q.Ep)
		return p, true, nil

	case q.Type == MovieSearch && (q.TMDBID > 0 || q.IMDBID != ""):
		var m tmdb.Movie
		var ok bool
		var err error
		if q.TMDBID > 0 {
			m, ok, err = c.tmdb.Movie(ctx, q.TMDBID)
		} else {
			m, ok, err = c.tmdb.MovieByIMDB(ctx, q.IMDBID)
		}
		if err != nil || !ok {
			return p, false, err
		}
		p.strict, p.display = true, m.Title
		p.tmdbID, p.imdbID, p.year = m.ID, m.IMDBID, m.Year
		p.names = []string{m.Title, m.TitleUK}
		return p, true, nil
	}

	// Title searches: t=movie&q, t=search&q.
	title, year := splitYear(query)
	if title == "" {
		return p, false, nil
	}
	p.names, p.year = []string{title}, year
	if year > 0 {
		p.kinds[source.Series] = false // a trailing year means Radarr's movie fallback
		return p, p.kinds[source.Movie], nil
	}
	// Anime title search: "Title 12" is an absolute episode when TMDb knows the series.
	if m := trailingNum.FindStringSubmatch(query); m != nil && p.kinds[source.Series] {
		show, ok, err := c.tmdb.SearchShow(ctx, m[1])
		if err != nil {
			return p, false, err
		}
		if ok && (nameKey(show.Title) == nameKey(m[1]) || nameKey(show.TitleUK) == nameKey(m[1])) {
			if s, e, ok := show.Absolute(atoi(m[2])); ok {
				return plan{
					show:    &show,
					names:   []string{m[1], show.Title, show.TitleUK},
					kinds:   map[source.Kind]bool{source.Series: true},
					season:  s,
					episode: e,
					year:    seasonYear(show, s),
				}, true, nil
			}
		}
	}
	return p, true, nil
}

// episode fills season and episode for a known series: an absolute number, a
// date or the plain season/ep params.
func (c *Catalog) episode(ctx context.Context, p plan, show tmdb.Show, q Query, absolute int) (plan, bool, error) {
	p.show = &show
	switch {
	case isDate(q):
		date := fmt.Sprintf("%04d-%s", q.Season, strings.ReplaceAll(q.Ep, "/", "-"))
		e, ok, err := c.tmdb.EpisodeByDate(ctx, show, date)
		if err != nil || !ok {
			return p, false, err
		}
		p.season, p.episode = e.Season, e.Number
	case absolute > 0:
		s, e, ok := show.Absolute(absolute)
		if !ok {
			return p, false, nil
		}
		p.season, p.episode = s, e
	default:
		p.season, p.episode = q.Season, atoi(q.Ep)
	}
	p.year = seasonYear(show, p.season)
	return p, true, nil
}

func (c *Catalog) titleOptions(ctx context.Context, show tmdb.Show, season int) (source.TitleOptions, error) {
	se, ok := show.Season(season)
	if !ok || se.EpisodeCount <= 0 {
		return source.TitleOptions{}, nil
	}
	options := source.TitleOptions{ExpectedEpisodes: se.EpisodeCount}
	eps, err := c.tmdb.SeasonEpisodes(ctx, show.ID, season)
	if err != nil {
		return options, err
	}
	today := time.Now().UTC().Format("2006-01-02")
	for _, ep := range eps {
		if ep.Number != se.EpisodeCount {
			continue
		}
		if _, err := time.Parse("2006-01-02", ep.AirDate); err == nil {
			options.LastEpisodeAired = ep.AirDate <= today
		}
	}
	return options, nil
}

func seasonYear(show tmdb.Show, season int) int {
	if se, ok := show.Season(season); ok {
		return se.Year()
	}
	return 0
}

func isDate(q Query) bool { return q.Season >= 1900 && strings.Contains(q.Ep, "/") }

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// titles searches the source with every name, merges hits by title key,
// filters them and loads the matching title pages.
func (c *Catalog) titles(ctx context.Context, src source.Source, p plan) ([]source.Title, error) {
	var cands []source.Candidate
	seen := map[string]bool{}
	for _, name := range dedupe(p.names) {
		hits, err := src.Search(ctx, name)
		if err != nil {
			return nil, err
		}
		for _, h := range hits {
			if !seen[h.Key] {
				seen[h.Key] = true
				cands = append(cands, h)
			}
		}
	}

	var picked, otherSeason []source.Candidate
	for _, cd := range cands {
		switch {
		case !p.kinds[cd.Kind]:
		case cd.Kind == source.Series && p.season > 0 && cd.Season != p.season:
			otherSeason = append(otherSeason, cd)
		case !yearOK(p.year, cd.Year):
		default:
			picked = append(picked, cd)
		}
	}

	if p.strict {
		picked = preferNameMatches(p.names, picked)
		otherSeason = preferNameMatches(p.names, otherSeason)
	}

	loaded := map[string]bool{}
	var out []source.Title
	seasonOptions := map[int]source.TitleOptions{}
	load := func(id string, season int) (source.Title, bool, error) {
		if loaded[id] {
			return source.Title{}, false, nil
		}
		loaded[id] = true
		options, known := seasonOptions[season]
		if !known && p.show != nil {
			var err error
			options, err = c.titleOptions(ctx, *p.show, season)
			if err != nil {
				return source.Title{}, false, err
			}
			seasonOptions[season] = options
		}
		t, err := src.Title(ctx, id, options)
		if err != nil {
			return t, false, err
		}
		return t, true, nil
	}
	accept := func(t source.Title) bool {
		return p.kinds[t.Kind] && (p.season == 0 || t.Season == p.season) && yearOK(p.year, t.Year) && (!p.strict || nameMatch(p.names, t))
	}
	for _, cd := range picked {
		t, ok, err := load(cd.ID, cd.Season)
		if err != nil {
			return nil, err
		}
		if ok && accept(t) {
			out = append(out, t)
		}
	}
	if p.season == 0 || len(out) > 0 {
		return out, nil
	}
	// The requested season is on a sibling page that search did not return.
	for _, cd := range otherSeason {
		t, ok, err := load(cd.ID, cd.Season)
		if err != nil {
			return nil, err
		}
		if !ok || (p.strict && !nameMatch(p.names, t)) {
			continue
		}
		for _, sl := range t.Seasons {
			if sl.Season != p.season {
				continue
			}
			sib, ok, err := load(sl.TitleID, sl.Season)
			if err != nil {
				return nil, err
			}
			if ok && accept(sib) {
				out = append(out, sib)
			}
		}
		if len(out) > 0 {
			break
		}
	}
	return out, nil
}

// preferNameMatches keeps only hits whose local title matches a TMDb title when
// there are such hits. It saves page loads, which the site rate-limits.
func preferNameMatches(names []string, cands []source.Candidate) []source.Candidate {
	var exact []source.Candidate
	for _, cd := range cands {
		if nameMatch(names, source.Title{Title: cd.Title}) {
			exact = append(exact, cd)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return cands
}

// yearOK allows one year of difference: release dates differ between countries.
func yearOK(want, got int) bool {
	return want == 0 || got == 0 || (got-want <= 1 && want-got <= 1)
}

func nameMatch(names []string, t source.Title) bool {
	for _, n := range names {
		k := nameKey(n)
		if k != "" && (k == nameKey(t.Title) || k == nameKey(t.Original)) {
			return true
		}
	}
	return false
}

// nameKey compares titles by letters and digits only.
func nameKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func dedupe(names []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if k := nameKey(n); k != "" && !seen[k] {
			seen[k] = true
			out = append(out, n)
		}
	}
	return out
}

// candidate is a release before sampling.
type candidate struct {
	rel   Release
	src   source.Source
	ep    source.Episode
	voice string
	name  release.Name
}

func (c *Catalog) releases(src source.Source, titles []source.Title, p plan, cats []int) []candidate {
	var out []candidate
	seen := map[string]bool{}
	for _, t := range titles {
		cat := CatMovies
		if t.Kind == source.Series {
			cat = CatTV
		}
		if !inCats(cats, cat) {
			continue
		}
		name := p.display
		if name == "" {
			name = t.Original
		}
		if name == "" {
			name = t.Title
		}
		year := t.Year
		if p.strict && p.year > 0 && t.Kind == source.Movie {
			year = p.year
		}
		for _, v := range t.Voices {
			for _, e := range v.Episodes {
				if t.Kind == source.Series && (e.Number < 1 || (p.episode > 0 && e.Number != p.episode)) {
					continue
				}
				id := release.ID{Source: src.Name(), TitleID: t.ID, Voice: release.VoiceHash(v.Name)}
				n := release.Name{Title: name, Voice: v.Name}
				if t.Kind == source.Series {
					id.Season, id.Episode = t.Season, e.Number
					n.Season, n.Episode = t.Season, e.Number
				} else {
					n.Year = year
				}
				ids := id.String()
				if seen[ids] {
					continue
				}
				seen[ids] = true
				r := Release{ID: ids, PubDate: t.Updated, Category: cat}
				if p.strict {
					r.TVDBID, r.TMDBID, r.IMDBID = p.tvdbID, p.tmdbID, p.imdbID
				}
				out = append(out, candidate{rel: r, src: src, ep: e, voice: src.Name() + ":" + t.ID + ":" + id.Voice, name: n})
			}
		}
	}
	return out
}

func paginate(all []candidate, offset, limit int) []candidate {
	if offset >= len(all) {
		return nil
	}
	end := len(all)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return all[offset:end]
}

// sampleConcurrency bounds parallel player/CDN requests while sampling.
const sampleConcurrency = 4

// sample reads quality and size once per voice and reuses them for the voice's
// other episodes, then builds the release titles.
func (c *Catalog) sample(ctx context.Context, page []candidate) error {
	first := map[string]int{}
	cached := map[int]source.Media{}
	var order []string
	for i, r := range page {
		if m, ok := r.src.CachedMedia(ctx, r.ep); ok {
			cached[i] = m
			continue
		}
		if _, ok := first[r.voice]; !ok {
			first[r.voice] = i
			order = append(order, r.voice)
		}
	}
	media := make(map[string]source.Media, len(order))
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, sampleConcurrency)
	var wg sync.WaitGroup
	for _, v := range order {
		r := page[first[v]]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			m, err := r.src.Sample(ctx, r.ep)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			media[v] = m
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	for i := range page {
		m, ok := cached[i]
		if !ok {
			m = media[page[i].voice]
		}
		page[i].name.Resolution = release.Resolution(m.Width, m.Height)
		page[i].rel.Title = release.Title(page[i].name)
		page[i].rel.Size = m.Size()
	}
	return nil
}
