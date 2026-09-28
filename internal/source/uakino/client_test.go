package uakino

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vmorshc/hls-indexer/internal/source"
)

func TestClientUsesHTTP2AndBrowserHeaders(t *testing.T) {
	var got *http.Request
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		io.WriteString(w, readFixture(t, "search-empty.html"))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())

	c, err := newClient(Options{BaseURL: srv.URL, RPS: 100}, &tls.Config{RootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Search(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if got.ProtoMajor != 2 {
		t.Errorf("proto %s, want HTTP/2", got.Proto)
	}
	if got.Header.Get("Accept-Language") != "uk-UA,uk;q=0.9" {
		t.Errorf("Accept-Language %q", got.Header.Get("Accept-Language"))
	}
	if ua := got.Header.Get("User-Agent"); !strings.Contains(ua, "Safari/") || !strings.Contains(ua, "Macintosh") {
		t.Errorf("User-Agent %q", ua)
	}
}

func TestClientFollowsDomainRedirectForSearch(t *testing.T) {
	newSite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.FormValue("story") != "Ейфорія" {
			t.Errorf("new site got %s story=%q", r.Method, r.FormValue("story"))
		}
		io.WriteString(w, readFixture(t, "search-eiforiia.html"))
	}))
	defer newSite.Close()
	oldSite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, newSite.URL+r.URL.Path, http.StatusMovedPermanently)
	}))
	defer oldSite.Close()

	c, err := New(Options{BaseURL: oldSite.URL, RPS: 100})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Search(context.Background(), "Ейфорія")
	if err != nil || len(got) == 0 {
		t.Fatalf("got %d candidates, err %v", len(got), err)
	}
}

func TestClientRateLimitsSiteOnly(t *testing.T) {
	var mu sync.Mutex
	var siteTimes []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ua/" {
			mu.Lock()
			siteTimes = append(siteTimes, time.Now())
			mu.Unlock()
			io.WriteString(w, readFixture(t, "search-empty.html"))
			return
		}
		io.WriteString(w, "player")
	}))
	defer srv.Close()
	c, err := New(Options{BaseURL: srv.URL, RPS: 5})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	start := time.Now()
	for range 10 {
		if _, _, err := c.getPlayer(ctx, srv.URL+"/vod/1"); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("10 player requests took %v, want no limit", d)
	}
	for range 3 {
		if _, err := c.Search(ctx, "x"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i < len(siteTimes); i++ {
		if gap := siteTimes[i].Sub(siteTimes[i-1]); gap < 180*time.Millisecond {
			t.Errorf("site request gap %v, want ≥ 200ms at 5 rps", gap)
		}
	}
}

func TestClientSourceUnavailable(t *testing.T) {
	tests := map[string]http.HandlerFunc{
		"challenge 403": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, "<html><head><title>Just a moment...</title></head></html>")
		},
		"challenge 200": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cf-Mitigated", "challenge")
			io.WriteString(w, "<html></html>")
		},
		"server error": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusBadGateway)
		},
		"foreign page": func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "<html><body>maintenance</body></html>")
		},
	}
	for name, h := range tests {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			c, _ := New(Options{BaseURL: srv.URL, RPS: 100})
			if _, err := c.Search(context.Background(), "x"); !errors.Is(err, source.ErrUnavailable) {
				t.Errorf("Search err = %v", err)
			}
			if _, err := c.Title(context.Background(), "312-shrek-2", source.TitleOptions{}); !errors.Is(err, source.ErrUnavailable) {
				t.Errorf("Title err = %v", err)
			}
		})
	}
}

func TestClientNetworkErrorHidesProxyCredentials(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	dead := srv.URL
	srv.Close()

	c, err := New(Options{BaseURL: "http://uakino.test", RPS: 100, ProxyURL: "http://user:s3cret-pass@" + strings.TrimPrefix(dead, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Search(context.Background(), "x")
	if !errors.Is(err, source.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("error leaks proxy password: %v", err)
	}

	_, err = New(Options{BaseURL: "http://uakino.test", RPS: 1, ProxyURL: "http://user:s3cret-pass@[bad"})
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("bad proxy URL error = %v", err)
	}
}
