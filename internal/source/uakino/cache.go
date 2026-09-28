package uakino

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/redis/go-redis/v9"
	"github.com/vmorshc/hls-indexer/internal/source"
)

func finished(t source.Title, options source.TitleOptions) bool {
	if t.Kind == source.Movie {
		return true
	}
	if options.ExpectedEpisodes <= 0 || !options.LastEpisodeAired || len(t.Voices) == 0 {
		return false
	}
	for _, v := range t.Voices {
		if len(v.Episodes) != options.ExpectedEpisodes {
			return false
		}
		for i, ep := range v.Episodes {
			if ep.Number != i+1 {
				return false
			}
		}
	}
	return true
}

func episodeKey(kind string, ep source.Episode) string {
	return fmt.Sprintf("%s:%x", kind, sha256.Sum256([]byte(ep.Locator)))
}

// CachedMedia returns only data measured for this episode, never a voice estimate.
func (c *Client) CachedMedia(ctx context.Context, ep source.Episode) (source.Media, bool) {
	var m source.Media
	ok := c.cacheGet(ctx, episodeKey("media", ep), &m)
	return m, ok
}

var invalidateStream = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end
return 0
`)

// InvalidateStream only removes the stream that failed, not a newer resolution.
func (c *Client) InvalidateStream(ctx context.Context, ep source.Episode, stream source.Stream) error {
	if c.rdb == nil {
		return nil
	}
	b, err := json.Marshal(stream)
	if err != nil {
		return err
	}
	return invalidateStream.Run(ctx, c.rdb, []string{cachePrefix + episodeKey("stream", ep)}, b).Err()
}

const cachePrefix = "hls-indexer:uakino:"

func (c *Client) cacheGet(ctx context.Context, key string, value any) bool {
	if c.rdb == nil {
		return false
	}
	b, err := c.rdb.Get(ctx, cachePrefix+key).Bytes()
	return err == nil && json.Unmarshal(b, value) == nil
}

func (c *Client) cacheSet(ctx context.Context, key string, value any) {
	if c.rdb == nil || c.cacheTTL <= 0 {
		return
	}
	b, err := json.Marshal(value)
	if err == nil {
		c.rdb.Set(ctx, cachePrefix+key, b, c.cacheTTL)
	}
}
