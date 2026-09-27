// Package newznab serves the Newznab indexer API at /indexer/api.
package newznab

import (
	"crypto/subtle"
	"encoding/xml"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Newznab error codes.
const (
	ErrBadKey         = 100
	ErrMissingParam   = 200
	ErrNoSuchFunction = 202
	ErrNotAvailable   = 203
)

const attrNS = "http://www.newznab.com/DTD/2010/feeds/attributes/"

type Handler struct {
	apiKey    string
	publicURL string
}

func New(apiKey, publicURL string) *Handler {
	return &Handler{apiKey: apiKey, publicURL: publicURL}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	t := q.Get("t")
	if t == "caps" {
		writeXML(w, "application/xml", capsDoc)
		return
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("apikey")), []byte(h.apiKey)) != 1 {
		writeError(w, ErrBadKey, "Incorrect user credentials")
		return
	}
	switch t {
	case "":
		writeError(w, ErrMissingParam, "Missing parameter (t)")
	case "search", "tvsearch", "movie":
		if isRSS(q) {
			h.writeChannel(w, q)
			return
		}
		writeError(w, ErrNotAvailable, "Function not available")
	case "get":
		if q.Get("id") == "" {
			writeError(w, ErrMissingParam, "Missing parameter (id)")
			return
		}
		writeError(w, ErrNotAvailable, "Function not available")
	default:
		writeError(w, ErrNoSuchFunction, "No such function")
	}
}

// isRSS reports a request with no q and no ID. v0 answers it with an empty channel.
func isRSS(q url.Values) bool {
	for _, k := range []string{"q", "tvdbid", "tmdbid", "imdbid"} {
		if strings.TrimSpace(q.Get(k)) != "" {
			return false
		}
	}
	return true
}

type rssDoc struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	NS      string     `xml:"xmlns:newznab,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title       string      `xml:"title"`
	Link        string      `xml:"link"`
	Description string      `xml:"description"`
	Response    rssResponse `xml:"newznab:response"`
}

type rssResponse struct {
	Offset int `xml:"offset,attr"`
	Total  int `xml:"total,attr"`
}

func (h *Handler) writeChannel(w http.ResponseWriter, q url.Values) {
	offset, err := strconv.Atoi(q.Get("offset"))
	if err != nil || offset < 0 {
		offset = 0
	}
	writeXML(w, "application/rss+xml", rssDoc{
		Version: "2.0",
		NS:      attrNS,
		Channel: rssChannel{
			Title:       "HLS Indexer",
			Link:        h.publicURL,
			Description: "Ukrainian dubs from HLS sources",
			Response:    rssResponse{Offset: offset},
		},
	})
}

type capsXML struct {
	XMLName xml.Name `xml:"caps"`
	Limits  struct {
		Default int `xml:"default,attr"`
		Max     int `xml:"max,attr"`
	} `xml:"limits"`
	Searching struct {
		Search      capsMode `xml:"search"`
		TVSearch    capsMode `xml:"tv-search"`
		MovieSearch capsMode `xml:"movie-search"`
	} `xml:"searching"`
	Categories []capsCategory `xml:"categories>category"`
}

type capsMode struct {
	Available       string `xml:"available,attr"`
	SupportedParams string `xml:"supportedParams,attr"`
}

type capsCategory struct {
	ID   int    `xml:"id,attr"`
	Name string `xml:"name,attr"`
}

var capsDoc = func() capsXML {
	var c capsXML
	c.Limits.Default, c.Limits.Max = 100, 100
	c.Searching.Search = capsMode{"yes", "q"}
	c.Searching.TVSearch = capsMode{"yes", "q,tvdbid,season,ep"}
	c.Searching.MovieSearch = capsMode{"yes", "q,tmdbid,imdbid"}
	c.Categories = []capsCategory{{5000, "TV"}, {2000, "Movies"}}
	return c
}()

type errorXML struct {
	XMLName     xml.Name `xml:"error"`
	Code        int      `xml:"code,attr"`
	Description string   `xml:"description,attr"`
}

// writeError sends a logical error: HTTP 200 with an <error> element.
func writeError(w http.ResponseWriter, code int, description string) {
	writeXML(w, "application/xml", errorXML{Code: code, Description: description})
}

func writeXML(w http.ResponseWriter, contentType string, v any) {
	b, err := xml.Marshal(v)
	if err != nil {
		http.Error(w, "encode xml", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType+"; charset=utf-8")
	w.Write([]byte(xml.Header))
	w.Write(b)
}
