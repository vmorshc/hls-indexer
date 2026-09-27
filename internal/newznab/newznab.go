// Package newznab serves the Newznab indexer API at /indexer/api.
package newznab

import (
	"crypto/subtle"
	"encoding/xml"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/vmorshc/hls-indexer/internal/catalog"
	"github.com/vmorshc/hls-indexer/internal/release"
)

// Newznab error codes.
const (
	ErrBadKey         = 100
	ErrMissingParam   = 200
	ErrNoSuchFunction = 202
	ErrUnknownRelease = 300
)

const attrNS = "http://www.newznab.com/DTD/2010/feeds/attributes/"

// Limits for the limit param.
const (
	DefaultLimit = 100
	MaxLimit     = 100
)

type Handler struct {
	apiKey    string
	publicURL string
	catalog   *catalog.Catalog
	log       *slog.Logger
}

func New(apiKey, publicURL string, c *catalog.Catalog, log *slog.Logger) *Handler {
	return &Handler{apiKey: apiKey, publicURL: strings.TrimRight(publicURL, "/"), catalog: c, log: log}
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
		h.search(w, r, t, q)
	case "get":
		h.get(w, q.Get("id"))
	default:
		writeError(w, ErrNoSuchFunction, "No such function")
	}
}

var searchTypes = map[string]catalog.Type{
	"tvsearch": catalog.TVSearch,
	"movie":    catalog.MovieSearch,
	"search":   catalog.GenericSearch,
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request, t string, q url.Values) {
	offset := intParam(q, "offset")
	if offset < 0 {
		offset = 0
	}
	if isRSS(q) {
		h.writeChannel(w, offset, catalog.Result{})
		return
	}
	limit := intParam(q, "limit")
	if limit < 1 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	query := catalog.Query{
		Type:       searchTypes[t],
		Q:          strings.TrimSpace(q.Get("q")),
		TVDBID:     intParam(q, "tvdbid"),
		TMDBID:     intParam(q, "tmdbid"),
		IMDBID:     strings.TrimPrefix(strings.TrimSpace(q.Get("imdbid")), "tt"),
		Season:     intParam(q, "season"),
		Ep:         strings.TrimSpace(q.Get("ep")),
		Categories: cats(q.Get("cat")),
		Offset:     offset,
		Limit:      limit,
	}
	res, err := h.catalog.Search(r.Context(), query)
	if err != nil {
		h.log.Error("search failed", "t", t, "q", query.Q, "tvdbid", query.TVDBID, "tmdbid", query.TMDBID, "imdbid", query.IMDBID, "err", err)
		http.Error(w, "source unavailable", http.StatusServiceUnavailable)
		return
	}
	h.writeChannel(w, offset, res)
}

func intParam(q url.Values, k string) int {
	n, err := strconv.Atoi(strings.TrimSpace(q.Get(k)))
	if err != nil {
		return 0
	}
	return n
}

func cats(s string) []int {
	var out []int
	for _, p := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func (h *Handler) get(w http.ResponseWriter, id string) {
	if id == "" {
		writeError(w, ErrMissingParam, "Missing parameter (id)")
		return
	}
	rid, err := release.Parse(id)
	if err != nil || !h.catalog.HasSource(rid.Source) {
		writeError(w, ErrUnknownRelease, "No such item")
		return
	}
	b, err := xml.Marshal(nzbDoc{NS: nzbNS, File: nzbFile{Subject: NZBSubjectPrefix + rid.String()}})
	if err != nil {
		http.Error(w, "encode nzb", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-nzb")
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(rid.String(), ":", "_")+`.nzb"`)
	w.Write([]byte(xml.Header))
	w.Write(b)
}

// NZBSubjectPrefix starts the only file subject of an HLS Indexer NZB.
const NZBSubjectPrefix = "hls-indexer:"

const nzbNS = "http://www.newzbin.com/DTD/2003/nzb"

type nzbDoc struct {
	XMLName xml.Name `xml:"nzb"`
	NS      string   `xml:"xmlns,attr"`
	File    nzbFile  `xml:"file"`
}

type nzbFile struct {
	Subject string `xml:"subject,attr"`
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
	Items       []rssItem   `xml:"item"`
}

type rssItem struct {
	Title     string       `xml:"title"`
	GUID      rssGUID      `xml:"guid"`
	Link      string       `xml:"link"`
	PubDate   string       `xml:"pubDate"`
	Enclosure rssEnclosure `xml:"enclosure"`
	Attrs     []rssAttr    `xml:"newznab:attr"`
}

type rssGUID struct {
	IsPermaLink bool   `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

type rssEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type rssAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type rssResponse struct {
	Offset int `xml:"offset,attr"`
	Total  int `xml:"total,attr"`
}

func (h *Handler) writeChannel(w http.ResponseWriter, offset int, res catalog.Result) {
	ch := rssChannel{
		Title:       "HLS Indexer",
		Link:        h.publicURL,
		Description: "Ukrainian dubs from HLS sources",
		Response:    rssResponse{Offset: offset, Total: res.Total},
	}
	for _, r := range res.Releases {
		ch.Items = append(ch.Items, h.item(r))
	}
	writeXML(w, "application/rss+xml", rssDoc{Version: "2.0", NS: attrNS, Channel: ch})
}

func (h *Handler) item(r catalog.Release) rssItem {
	link := h.publicURL + "/indexer/api?" + url.Values{"t": {"get"}, "id": {r.ID}, "apikey": {h.apiKey}}.Encode()
	attrs := []rssAttr{
		{"category", strconv.Itoa(r.Category)},
		{"size", strconv.FormatInt(r.Size, 10)},
	}
	if r.TVDBID > 0 {
		attrs = append(attrs, rssAttr{"tvdbid", strconv.Itoa(r.TVDBID)})
	}
	if r.TMDBID > 0 {
		attrs = append(attrs, rssAttr{"tmdbid", strconv.Itoa(r.TMDBID)})
	}
	if imdb := strings.TrimPrefix(r.IMDBID, "tt"); imdb != "" {
		attrs = append(attrs, rssAttr{"imdb", imdb})
	}
	return rssItem{
		Title:     r.Title,
		GUID:      rssGUID{Value: r.ID},
		Link:      link,
		PubDate:   r.PubDate.UTC().Format(time.RFC1123Z),
		Enclosure: rssEnclosure{URL: link, Length: r.Size, Type: "application/x-nzb"},
		Attrs:     attrs,
	}
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
