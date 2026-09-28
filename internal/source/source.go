// Package source defines the Source interface and the types every source shares.
package source

import (
	"context"
	"errors"
	"time"
)

// ErrUnavailable wraps every network error, challenge page or unexpected
// response from a source. Search answers it with HTTP 503.
var ErrUnavailable = errors.New("source unavailable")

// Kind tells a movie from a series season.
type Kind int

const (
	Movie Kind = iota + 1
	Series
)

// Candidate is one search hit.
type Candidate struct {
	ID     string // source title ID
	Key    string // merge key: equal for the same title across queries
	Title  string // local (Ukrainian) title
	Year   int
	Season int // 0 for movies
	Kind   Kind
}

// Title is a loaded title page with its voices.
type Title struct {
	ID       string
	Key      string
	Title    string // local title without season suffix
	Original string // original title without season suffix, may be empty
	Year     int
	Season   int // 0 for movies
	Kind     Kind
	Updated  time.Time
	Seasons  []SeasonLink // sibling season pages
	Voices   []Voice
}

// SeasonLink points to the page of another season of the same series.
type SeasonLink struct {
	Season  int
	TitleID string
}

// Voice is one dub with its episodes. A movie voice has one episode with Number 0.
type Voice struct {
	Name     string
	Episodes []Episode
}

// Episode is one playable item. Locator is opaque outside the source.
type Episode struct {
	Number  int
	Locator string
}

// Media describes the best variant of an episode stream.
type Media struct {
	Width     int
	Height    int
	Bandwidth int64   // bits per second
	Duration  float64 // seconds, sum of EXTINF
}

// Size estimates the media size in bytes: bandwidth × duration / 8.
func (m Media) Size() int64 { return int64(float64(m.Bandwidth) * m.Duration / 8) }

// TitleOptions supplies season metadata without coupling sources to a metadata provider.
// Zero values mean the caller cannot confirm that the season has finished.
type TitleOptions struct {
	ExpectedEpisodes int
	LastEpisodeAired bool
}

// Source turns a query into titles and an episode into stream data.
type Source interface {
	// Name is the release ID prefix, e.g. "uakino".
	Name() string
	// Search returns the first page of hits for a query.
	Search(ctx context.Context, query string) ([]Candidate, error)
	// Title loads a title page and its voices.
	Title(ctx context.Context, titleID string, options TitleOptions) (Title, error)
	// Sample reads the best variant of an episode: resolution, bandwidth, duration.
	Sample(ctx context.Context, ep Episode) (Media, error)
	// Resolve returns the master playlist URL and subtitle tracks of an episode.
	Resolve(ctx context.Context, ep Episode) (Stream, error)
}

// Stream is a playable episode: the HLS master playlist and its subtitles.
type Stream struct {
	Master    string // absolute URL
	Subtitles []Subtitle
}

// Subtitle is one external WebVTT subtitle track.
type Subtitle struct {
	Label    string // track title, as the source shows it
	Language string // ISO 639-2 code, e.g. "ukr", "eng"; empty if unknown
	URL      string
}
