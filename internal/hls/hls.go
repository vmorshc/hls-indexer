// Package hls parses HLS master and media playlists.
package hls

import (
	"bufio"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// Variant is one EXT-X-STREAM-INF entry of a master playlist.
type Variant struct {
	URL       string // absolute
	Bandwidth int64
	Width     int
	Height    int
}

// Media is a media playlist.
type Media struct {
	Segments []Segment
	Ended    bool // EXT-X-ENDLIST present
}

// Segment is one media segment.
type Segment struct {
	URL      string // absolute
	Duration float64
}

// Duration is the sum of EXTINF.
func (m Media) Duration() float64 {
	var d float64
	for _, s := range m.Segments {
		d += s.Duration
	}
	return d
}

var ErrNotPlaylist = errors.New("not an m3u8 playlist")

// ParseMaster reads the variants of a master playlist. Relative URLs resolve against base.
func ParseMaster(body string, base *url.URL) ([]Variant, error) {
	lines, err := playlistLines(body)
	if err != nil {
		return nil, err
	}
	var out []Variant
	var cur *Variant
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "#EXT-X-STREAM-INF:"):
			v := Variant{}
			attrs := parseAttrs(strings.TrimPrefix(l, "#EXT-X-STREAM-INF:"))
			v.Bandwidth, _ = strconv.ParseInt(attrs["BANDWIDTH"], 10, 64)
			if w, h, ok := strings.Cut(attrs["RESOLUTION"], "x"); ok {
				v.Width, _ = strconv.Atoi(w)
				v.Height, _ = strconv.Atoi(h)
			}
			cur = &v
		case strings.HasPrefix(l, "#"):
		case cur != nil:
			cur.URL = resolve(base, l)
			out = append(out, *cur)
			cur = nil
		}
	}
	if len(out) == 0 {
		return nil, errors.New("master playlist has no variants")
	}
	return out, nil
}

// Best returns the variant with the highest bandwidth.
func Best(vs []Variant) Variant {
	best := vs[0]
	for _, v := range vs[1:] {
		if v.Bandwidth > best.Bandwidth {
			best = v
		}
	}
	return best
}

// ParseMedia reads the segments of a media playlist. Relative URLs resolve against base.
func ParseMedia(body string, base *url.URL) (Media, error) {
	lines, err := playlistLines(body)
	if err != nil {
		return Media{}, err
	}
	var m Media
	dur := -1.0
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "#EXTINF:"):
			v, _, _ := strings.Cut(strings.TrimPrefix(l, "#EXTINF:"), ",")
			d, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return Media{}, errors.New("bad EXTINF: " + l)
			}
			dur = d
		case l == "#EXT-X-ENDLIST":
			m.Ended = true
		case strings.HasPrefix(l, "#"):
		case dur >= 0:
			m.Segments = append(m.Segments, Segment{URL: resolve(base, l), Duration: dur})
			dur = -1
		}
	}
	if len(m.Segments) == 0 {
		return Media{}, errors.New("media playlist has no segments")
	}
	return m, nil
}

func playlistLines(body string) ([]string, error) {
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 || lines[0] != "#EXTM3U" {
		return nil, ErrNotPlaylist
	}
	return lines, sc.Err()
}

// parseAttrs splits an attribute list: KEY=value,KEY="quoted, value".
func parseAttrs(s string) map[string]string {
	out := map[string]string{}
	for s != "" {
		k, rest, ok := strings.Cut(s, "=")
		if !ok {
			break
		}
		var v string
		if strings.HasPrefix(rest, `"`) {
			end := strings.Index(rest[1:], `"`)
			if end < 0 {
				v, rest = rest[1:], ""
			} else {
				v, rest = rest[1:end+1], rest[end+2:]
			}
			rest = strings.TrimPrefix(rest, ",")
		} else {
			v, rest, _ = strings.Cut(rest, ",")
		}
		out[strings.TrimSpace(k)] = v
		s = rest
	}
	return out
}

func resolve(base *url.URL, ref string) string {
	u, err := url.Parse(ref)
	if err != nil || base == nil {
		return ref
	}
	return base.ResolveReference(u).String()
}
