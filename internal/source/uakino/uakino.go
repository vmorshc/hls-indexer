package uakino

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"

	"github.com/vmorshc/hls-indexer/internal/hls"
	"github.com/vmorshc/hls-indexer/internal/source"
)

// Name is the release ID prefix of this source.
const Name = "uakino"

var _ source.Source = (*Client)(nil)

func (c *Client) Name() string { return Name }

// Search posts the site search and returns the first page of hits.
func (c *Client) Search(ctx context.Context, query string) ([]source.Candidate, error) {
	body, final, err := c.postSite(ctx, "/ua/", url.Values{
		"do":        {"search"},
		"subaction": {"search"},
		"story":     {query},
	})
	if err != nil {
		return nil, err
	}
	out, err := parseSearch(body, final)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", source.ErrUnavailable, err)
	}
	return out, nil
}

// ErrBadTitleID marks a title ID that is not <news_id>-<slug>.
var ErrBadTitleID = errors.New("bad uakino title id")

// Title loads the title page and its playlist. Movies without a playlist use
// the inline player when it points to a player host.
func (c *Client) Title(ctx context.Context, titleID string) (source.Title, error) {
	m := titleIDRe.FindStringSubmatch(titleID)
	if m == nil {
		return source.Title{}, ErrBadTitleID
	}
	body, final, err := c.getSite(ctx, "/"+titleID+".html", nil)
	if err != nil {
		return source.Title{}, err
	}
	p, err := parseTitle(body, final, c.isPlayerHost)
	if err != nil {
		return source.Title{}, fmt.Errorf("%w: %v", source.ErrUnavailable, err)
	}
	t := p.title
	// The canonical URL may carry another slug. Keep the ID the caller used.
	t.ID, t.Key = titleID, m[1]

	body, _, err = c.getSite(ctx, "/engine/ajax/playlists.php?news_id="+m[1]+"&xfield=playlist",
		http.Header{"X-Requested-With": {"XMLHttpRequest"}})
	if err != nil {
		return source.Title{}, err
	}
	voices, serial, err := parsePlaylist(body, c.base)
	switch {
	case errors.Is(err, errNotData):
		if p.inlineFile != "" {
			t.Voices = []source.Voice{{Name: inlineVoice(p.dubbing), Episodes: []source.Episode{{Locator: p.inlineFile}}}}
		}
	case err != nil:
		return source.Title{}, fmt.Errorf("%w: %v", source.ErrUnavailable, err)
	default:
		t.Voices = voices
	}
	if serial {
		t.Kind = source.Series
		if t.Season == 0 {
			t.Season = 1
		}
		for _, v := range t.Voices {
			sort.Slice(v.Episodes, func(i, j int) bool { return v.Episodes[i].Number < v.Episodes[j].Number })
		}
	} else if len(t.Voices) > 0 {
		t.Kind, t.Season = source.Movie, 0
	}
	return t, nil
}

// Sample reads the player page, the master playlist and the best variant's
// media playlist.
func (c *Client) Sample(ctx context.Context, ep source.Episode) (source.Media, error) {
	body, _, err := c.getPlayer(ctx, ep.Locator)
	if err != nil {
		return source.Media{}, err
	}
	p, err := parsePlayer(body)
	if err != nil {
		return source.Media{}, fmt.Errorf("%w: %v", source.ErrUnavailable, err)
	}
	body, final, err := c.getPlayer(ctx, p.File)
	if err != nil {
		return source.Media{}, err
	}
	variants, err := hls.ParseMaster(body, final)
	if err != nil {
		return source.Media{}, fmt.Errorf("%w: master %v", source.ErrUnavailable, err)
	}
	best := hls.Best(variants)
	body, final, err = c.getPlayer(ctx, best.URL)
	if err != nil {
		return source.Media{}, err
	}
	media, err := hls.ParseMedia(body, final)
	if err != nil {
		return source.Media{}, fmt.Errorf("%w: media %v", source.ErrUnavailable, err)
	}
	return source.Media{Width: best.Width, Height: best.Height, Bandwidth: best.Bandwidth, Duration: media.Duration()}, nil
}
