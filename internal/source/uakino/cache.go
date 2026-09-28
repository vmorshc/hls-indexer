package uakino

import (
	"context"
	"encoding/json"
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
