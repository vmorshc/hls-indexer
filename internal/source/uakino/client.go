// Package uakino is the UAKino source: HTTP client, parsers and the Source implementation.
package uakino

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/vmorshc/hls-indexer/internal/source"
)

const (
	userAgent      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15"
	acceptLanguage = "uk-UA,uk;q=0.9"
	maxBody        = 8 << 20
	requestTimeout = 30 * time.Second
)

// Options configure the client.
type Options struct {
	BaseURL string
	// RPS limits requests to the site. Player pages and the CDN are not limited.
	RPS float64
	// ProxyURL is an optional HTTP proxy for every request. Empty means none.
	ProxyURL string
	// PlayerHosts lists hosts accepted as inline players, subdomains included.
	PlayerHosts []string
}

// Client talks to the UAKino site and its player hosts. It keeps cookies in memory only.
type Client struct {
	base        *url.URL
	site        *http.Client // rate limited
	player      *http.Client // not limited
	playerHosts []string
}

// New builds the client. Errors never include the proxy URL.
func New(o Options) (*Client, error) { return newClient(o, nil) }

func newClient(o Options, tlsConfig *tls.Config) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(o.BaseURL, "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("uakino.base_url %q is invalid", o.BaseURL)
	}
	if o.RPS <= 0 {
		return nil, errors.New("uakino.rps must be positive")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ForceAttemptHTTP2 = true
	tr.Proxy = nil
	if tlsConfig != nil {
		tr.TLSClientConfig = tlsConfig
	}
	if o.ProxyURL != "" {
		p, err := url.Parse(o.ProxyURL)
		if err != nil || p.Host == "" {
			return nil, errors.New("UAKINO_PROXY_URL is invalid")
		}
		tr.Proxy = http.ProxyURL(p)
	}
	lim := &limiter{interval: time.Duration(float64(time.Second) / o.RPS)}
	return &Client{
		base:        base,
		site:        &http.Client{Transport: headers{limited{tr, lim}}, Timeout: requestTimeout, CheckRedirect: keepPost},
		player:      &http.Client{Transport: headers{tr}, Timeout: requestTimeout},
		playerHosts: o.PlayerHosts,
	}, nil
}

// keepPost stops Go from turning a redirected POST into a GET. postSite
// repeats the POST at the new location instead.
func keepPost(req *http.Request, via []*http.Request) error {
	if via[0].Method == http.MethodPost {
		return http.ErrUseLastResponse
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

// headers adds the headers Cloudflare expects to every request.
type headers struct{ next http.RoundTripper }

func (h headers) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("User-Agent", userAgent)
	r.Header.Set("Accept-Language", acceptLanguage)
	return h.next.RoundTrip(r)
}

type limited struct {
	next http.RoundTripper
	lim  *limiter
}

func (l limited) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := l.lim.wait(r.Context()); err != nil {
		return nil, err
	}
	return l.next.RoundTrip(r)
}

// limiter spaces requests by a fixed interval.
type limiter struct {
	mu       sync.Mutex
	interval time.Duration
	next     time.Time
}

func (l *limiter) wait(ctx context.Context) error {
	l.mu.Lock()
	now := time.Now()
	at := l.next
	if at.Before(now) {
		at = now
	}
	l.next = at.Add(l.interval)
	l.mu.Unlock()
	d := time.Until(at)
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// siteURL resolves a path or URL against the base URL.
func (c *Client) siteURL(ref string) *url.URL {
	u, err := url.Parse(ref)
	if err != nil {
		return c.base
	}
	return c.base.ResolveReference(u)
}

func (c *Client) getSite(ctx context.Context, path string, hdr http.Header) (string, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.siteURL(path).String(), nil)
	if err != nil {
		return "", nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	return do(c.site, req)
}

// postSite posts a form and follows redirects with the same POST, e.g. after a domain change.
func (c *Client) postSite(ctx context.Context, path string, form url.Values) (string, *url.URL, error) {
	target := c.siteURL(path)
	for range 5 {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), strings.NewReader(form.Encode()))
		if err != nil {
			return "", nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		body, final, loc, err := send(c.site, req)
		if err != nil || loc == nil {
			return body, final, err
		}
		target = loc
	}
	return "", nil, fmt.Errorf("%w: too many redirects at %s", source.ErrUnavailable, target.Redacted())
}

func (c *Client) getPlayer(ctx context.Context, rawURL string) (string, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil, fmt.Errorf("%w: bad url", source.ErrUnavailable)
	}
	return do(c.player, req)
}

// do runs a request and returns the body and the final URL after redirects.
// Network errors, non-200 statuses and challenge pages are ErrUnavailable.
func do(hc *http.Client, req *http.Request) (string, *url.URL, error) {
	body, final, loc, err := send(hc, req)
	if err == nil && loc != nil {
		err = fmt.Errorf("%w: unfollowed redirect at %s", source.ErrUnavailable, req.URL.Redacted())
	}
	return body, final, err
}

// send is do that returns an unfollowed redirect as loc instead of an error.
func send(hc *http.Client, req *http.Request) (body string, final, loc *url.URL, err error) {
	resp, err := hc.Do(req)
	if err != nil {
		if ctxErr := req.Context().Err(); ctxErr != nil {
			return "", nil, nil, ctxErr
		}
		return "", nil, nil, fmt.Errorf("%w: %s %s: %v", source.ErrUnavailable, req.Method, req.URL.Redacted(), unwrapURLError(err))
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", nil, nil, fmt.Errorf("%w: read %s: %v", source.ErrUnavailable, req.URL.Redacted(), err)
	}
	body = string(b)
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if l, err := resp.Location(); err == nil {
			return "", nil, l, nil
		}
	}
	if resp.StatusCode != http.StatusOK {
		return "", nil, nil, fmt.Errorf("%w: %s returned %d", source.ErrUnavailable, req.URL.Redacted(), resp.StatusCode)
	}
	if isChallenge(resp, body) {
		return "", nil, nil, fmt.Errorf("%w: challenge page at %s", source.ErrUnavailable, req.URL.Redacted())
	}
	return body, resp.Request.URL, nil, nil
}

// unwrapURLError drops the url.Error wrapper, whose text repeats the request URL.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func isChallenge(resp *http.Response, body string) bool {
	return resp.Header.Get("Cf-Mitigated") == "challenge" || strings.Contains(body, "<title>Just a moment...</title>")
}

func (c *Client) isPlayerHost(u *url.URL) bool {
	h := strings.ToLower(u.Hostname())
	for _, p := range c.playerHosts {
		p = strings.ToLower(p)
		if h == p || strings.HasSuffix(h, "."+p) {
			return true
		}
	}
	return false
}
