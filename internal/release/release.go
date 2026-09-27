// Package release holds the pure release helpers: ID codec, voice hash,
// voice → group, resolution label and release title. No I/O.
package release

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// NZBSubjectPrefix starts the only file subject of an HLS Indexer NZB:
// hls-indexer:<release ID>.
const NZBSubjectPrefix = "hls-indexer:"

// ID is a stateless release coordinate. Season and Episode are 0 for a movie.
type ID struct {
	Source  string
	TitleID string
	Season  int
	Episode int
	Voice   string // VoiceHash of the voice name
}

// Movie reports whether the ID points to a movie.
func (id ID) Movie() bool { return id.Season == 0 && id.Episode == 0 }

// String encodes the ID as <source>:<title ID>:<sNNeNN | movie>:<voice hash>.
func (id ID) String() string {
	unit := "movie"
	if !id.Movie() {
		unit = fmt.Sprintf("s%02de%02d", id.Season, id.Episode)
	}
	return id.Source + ":" + id.TitleID + ":" + unit + ":" + id.Voice
}

var (
	partRe  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	unitRe  = regexp.MustCompile(`^s(\d{2,4})e(\d{2,5})$`)
	voiceRe = regexp.MustCompile(`^[0-9a-f]{8}$`)
)

// ErrInvalid marks a string that is not a release ID.
var ErrInvalid = errors.New("invalid release id")

// Parse decodes a release ID string.
func Parse(s string) (ID, error) {
	p := strings.Split(s, ":")
	if len(p) != 4 || !partRe.MatchString(p[0]) || !partRe.MatchString(p[1]) || !voiceRe.MatchString(p[3]) {
		return ID{}, ErrInvalid
	}
	id := ID{Source: p[0], TitleID: p[1], Voice: p[3]}
	if p[2] == "movie" {
		return id, nil
	}
	m := unitRe.FindStringSubmatch(p[2])
	if m == nil {
		return ID{}, ErrInvalid
	}
	id.Season, _ = strconv.Atoi(m[1])
	id.Episode, _ = strconv.Atoi(m[2])
	if id.Season < 1 || id.Episode < 1 {
		return ID{}, ErrInvalid
	}
	return id, nil
}

// VoiceHash is the first 8 hex chars of SHA-256 over the voice name, trimmed,
// whitespace collapsed and lowercased.
func VoiceHash(voice string) string {
	norm := strings.ToLower(strings.Join(strings.Fields(voice), " "))
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:4])
}

// Group converts a voice name into an ASCII release group: Cyrillic
// transliterated, "+" → "Plus", words capitalized and joined.
func Group(voice string) string {
	var b strings.Builder
	for _, w := range words(strings.ReplaceAll(voice, "+", " Plus "), true) {
		r := []rune(w)
		b.WriteString(strings.ToUpper(string(r[0])) + string(r[1:]))
	}
	if b.Len() == 0 {
		return "Unknown"
	}
	return b.String()
}

// words transliterates Cyrillic in s and splits it into alphanumeric words.
// Apostrophes join, every other non-alphanumeric splits. ascii also drops
// other non-ASCII letters, as the *arr group parser needs.
func words(s string, ascii bool) []string {
	s = strings.NewReplacer("'", "", "’", "", "ʼ", "").Replace(s)
	return strings.FieldsFunc(Translit(s), func(r rune) bool {
		return (ascii && r > unicode.MaxASCII) || !(unicode.IsLetter(r) || unicode.IsDigit(r))
	})
}

// Resolution maps the best variant size to an *arr resolution label with the
// *arr media-info thresholds. Unknown sizes give "".
func Resolution(width, height int) string {
	switch {
	case width >= 3200 || height >= 2100:
		return "2160p"
	case width >= 1800 || height >= 1000:
		return "1080p"
	case width >= 1200 || height >= 700:
		return "720p"
	case width >= 1000 || height >= 560:
		return "576p"
	case width > 0 || height > 0:
		return "480p"
	}
	return ""
}

// Name holds the parts of a release title. Season and Episode are 0 for a movie.
type Name struct {
	Title      string
	Year       int // movies only
	Season     int
	Episode    int
	Resolution string
	Voice      string
}

// Title builds Name.S01E02.1080p.WEB-DL.UKR-Group or Name.2004.1080p.WEB-DL.UKR-Group.
func Title(n Name) string {
	parts := words(n.Title, false)
	if n.Season > 0 || n.Episode > 0 {
		parts = append(parts, fmt.Sprintf("S%02dE%02d", n.Season, n.Episode))
	} else if n.Year > 0 {
		parts = append(parts, strconv.Itoa(n.Year))
	}
	if n.Resolution != "" {
		parts = append(parts, n.Resolution)
	}
	parts = append(parts, "WEB-DL", "UKR-"+Group(n.Voice))
	return strings.Join(parts, ".")
}
