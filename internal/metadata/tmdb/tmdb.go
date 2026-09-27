// Package tmdb looks up titles, years and episode lists on TMDb and caches
// every response in Redis for a month.
package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// CacheTTL is how long a TMDb response stays in Redis.
const CacheTTL = 30 * 24 * time.Hour

const keyPrefix = "hls-indexer:tmdb:"

// ErrUnavailable wraps network errors and unexpected TMDb responses.
var ErrUnavailable = errors.New("tmdb unavailable")

type Client struct {
	base string
	key  string
	http *http.Client
	rdb  *redis.Client
}

// New builds a client. A nil rdb disables the cache.
func New(baseURL, apiKey string, rdb *redis.Client) *Client {
	return &Client{
		base: strings.TrimRight(baseURL, "/"),
		key:  apiKey,
		http: &http.Client{Timeout: 20 * time.Second},
		rdb:  rdb,
	}
}

// Show is a TV series.
type Show struct {
	ID      int
	TVDBID  int
	IMDBID  string // with tt
	Title   string // English
	TitleUK string // uk-UA, may be empty
	Year    int
	Seasons []Season // season 0 (specials) excluded
}

type Season struct {
	Number       int
	EpisodeCount int
	AirDate      string // YYYY-MM-DD, may be empty
}

// Year of the season's first air date, 0 when unknown.
func (s Season) Year() int { return yearOf(s.AirDate) }

// Season returns the season with number n.
func (s Show) Season(n int) (Season, bool) {
	for _, se := range s.Seasons {
		if se.Number == n {
			return se, true
		}
	}
	return Season{}, false
}

// Absolute converts an absolute episode number to season and episode by
// counting the episodes of regular seasons in order.
func (s Show) Absolute(n int) (season, episode int, ok bool) {
	if n < 1 {
		return 0, 0, false
	}
	for _, se := range s.Seasons {
		if n <= se.EpisodeCount {
			return se.Number, n, true
		}
		n -= se.EpisodeCount
	}
	return 0, 0, false
}

// Movie is a film.
type Movie struct {
	ID      int
	IMDBID  string // with tt
	Title   string
	TitleUK string
	Year    int
}

// Episode is one episode of a season.
type Episode struct {
	Season  int
	Number  int
	AirDate string
}

type translations struct {
	Translations []struct {
		Lang string `json:"iso_639_1"`
		Data struct {
			Name  string `json:"name"`
			Title string `json:"title"`
		} `json:"data"`
	} `json:"translations"`
}

func (t translations) uk() string {
	for _, tr := range t.Translations {
		if tr.Lang == "uk" {
			return strings.TrimSpace(tr.Data.Name + tr.Data.Title)
		}
	}
	return ""
}

// ShowByTVDB finds a series by its TVDB ID. ok is false when TMDb has no match.
func (c *Client) ShowByTVDB(ctx context.Context, tvdbID int) (Show, bool, error) {
	var found struct {
		TV []struct {
			ID int `json:"id"`
		} `json:"tv_results"`
	}
	ok, err := c.get(ctx, "/find/"+strconv.Itoa(tvdbID), url.Values{"external_source": {"tvdb_id"}}, &found)
	if err != nil || !ok || len(found.TV) == 0 {
		return Show{}, false, err
	}
	return c.Show(ctx, found.TV[0].ID)
}

// SearchShow returns the first TMDb series match for a title.
func (c *Client) SearchShow(ctx context.Context, query string) (Show, bool, error) {
	var found struct {
		Results []struct {
			ID int `json:"id"`
		} `json:"results"`
	}
	ok, err := c.get(ctx, "/search/tv", url.Values{"query": {query}}, &found)
	if err != nil || !ok || len(found.Results) == 0 {
		return Show{}, false, err
	}
	return c.Show(ctx, found.Results[0].ID)
}

// Show loads a series by TMDb ID.
func (c *Client) Show(ctx context.Context, id int) (Show, bool, error) {
	var r struct {
		ID           int    `json:"id"`
		Name         string `json:"name"`
		FirstAirDate string `json:"first_air_date"`
		Seasons      []struct {
			Number       int    `json:"season_number"`
			EpisodeCount int    `json:"episode_count"`
			AirDate      string `json:"air_date"`
		} `json:"seasons"`
		ExternalIDs struct {
			TVDBID int    `json:"tvdb_id"`
			IMDBID string `json:"imdb_id"`
		} `json:"external_ids"`
		Translations translations `json:"translations"`
	}
	ok, err := c.get(ctx, "/tv/"+strconv.Itoa(id), url.Values{"append_to_response": {"external_ids,translations"}}, &r)
	if err != nil || !ok {
		return Show{}, false, err
	}
	s := Show{
		ID:      r.ID,
		TVDBID:  r.ExternalIDs.TVDBID,
		IMDBID:  r.ExternalIDs.IMDBID,
		Title:   r.Name,
		TitleUK: r.Translations.uk(),
		Year:    yearOf(r.FirstAirDate),
	}
	for _, se := range r.Seasons {
		if se.Number > 0 {
			s.Seasons = append(s.Seasons, Season{Number: se.Number, EpisodeCount: se.EpisodeCount, AirDate: se.AirDate})
		}
	}
	return s, true, nil
}

// SeasonEpisodes lists the episodes of one season with air dates.
func (c *Client) SeasonEpisodes(ctx context.Context, showID, season int) ([]Episode, error) {
	var r struct {
		Episodes []struct {
			Season  int    `json:"season_number"`
			Number  int    `json:"episode_number"`
			AirDate string `json:"air_date"`
		} `json:"episodes"`
	}
	ok, err := c.get(ctx, fmt.Sprintf("/tv/%d/season/%d", showID, season), nil, &r)
	if err != nil || !ok {
		return nil, err
	}
	out := make([]Episode, len(r.Episodes))
	for i, e := range r.Episodes {
		out[i] = Episode{Season: e.Season, Number: e.Number, AirDate: e.AirDate}
	}
	return out, nil
}

// EpisodeByDate finds the episode that aired on date (YYYY-MM-DD).
func (c *Client) EpisodeByDate(ctx context.Context, s Show, date string) (Episode, bool, error) {
	year := yearOf(date)
	for _, se := range s.Seasons {
		if y := se.Year(); y != 0 && y > year {
			continue
		}
		eps, err := c.SeasonEpisodes(ctx, s.ID, se.Number)
		if err != nil {
			return Episode{}, false, err
		}
		for _, e := range eps {
			if e.AirDate == date {
				return e, true, nil
			}
		}
	}
	return Episode{}, false, nil
}

// Movie loads a film by TMDb ID.
func (c *Client) Movie(ctx context.Context, id int) (Movie, bool, error) {
	var r struct {
		ID           int          `json:"id"`
		Title        string       `json:"title"`
		ReleaseDate  string       `json:"release_date"`
		IMDBID       string       `json:"imdb_id"`
		Translations translations `json:"translations"`
	}
	ok, err := c.get(ctx, "/movie/"+strconv.Itoa(id), url.Values{"append_to_response": {"translations"}}, &r)
	if err != nil || !ok {
		return Movie{}, false, err
	}
	return Movie{ID: r.ID, IMDBID: r.IMDBID, Title: r.Title, TitleUK: r.Translations.uk(), Year: yearOf(r.ReleaseDate)}, true, nil
}

// MovieByIMDB finds a film by IMDb ID, with or without the tt prefix.
func (c *Client) MovieByIMDB(ctx context.Context, imdbID string) (Movie, bool, error) {
	var found struct {
		Movies []struct {
			ID int `json:"id"`
		} `json:"movie_results"`
	}
	id := "tt" + strings.TrimPrefix(imdbID, "tt")
	ok, err := c.get(ctx, "/find/"+url.PathEscape(id), url.Values{"external_source": {"imdb_id"}}, &found)
	if err != nil || !ok || len(found.Movies) == 0 {
		return Movie{}, false, err
	}
	return c.Movie(ctx, found.Movies[0].ID)
}

// get fetches path with the English language and decodes JSON into v.
// It returns ok=false on 404. Responses are cached under the path and query,
// never the key.
func (c *Client) get(ctx context.Context, path string, q url.Values, v any) (bool, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("language", "en-US")
	cacheKey := keyPrefix + path + "?" + q.Encode()
	if c.rdb != nil {
		if b, err := c.rdb.Get(ctx, cacheKey).Bytes(); err == nil {
			return true, json.Unmarshal(b, v)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path+"?"+q.Encode(), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	if strings.Count(c.key, ".") == 2 {
		req.Header.Set("Authorization", "Bearer "+c.key) // v4 read access token
	} else {
		aq := req.URL.Query()
		aq.Set("api_key", c.key)
		req.URL.RawQuery = aq.Encode()
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL carries the key
		}
		return false, fmt.Errorf("%w: %s: %v", ErrUnavailable, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return false, fmt.Errorf("%w: %s: %v", ErrUnavailable, path, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("%w: %s returned %d", ErrUnavailable, path, resp.StatusCode)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, fmt.Errorf("%w: %s: %v", ErrUnavailable, path, err)
	}
	if c.rdb != nil {
		c.rdb.Set(ctx, cacheKey, b, CacheTTL)
	}
	return true, nil
}

func yearOf(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, _ := strconv.Atoi(date[:4])
	return y
}
