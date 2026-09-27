# UAKino rules

Code: `internal/source/uakino`. Request details: [requests.md](requests.md).

## Access

| Rule | Reason |
|---|---|
| Use HTTP/2 for `uakino.best` | Cloudflare returns a 403 `Just a moment...` challenge to HTTP/1.1. Go `net/http` over TLS negotiates HTTP/2 by default and passes. |
| Send `Accept-Language: uk-UA,uk;q=0.9` and a desktop Safari `User-Agent` | A request without `Accept-Language` gets the 403 challenge even over HTTP/2. |
| Take the base URL from config (`uakino.base_url`) and follow redirects | The domain changed before: `uakino.club` redirects to `uakino.best`. |
| Rate-limit `uakino.best` only (`uakino.rps`, default 1) | Player pages and CDN have no limit. |
| Keep cookies in memory only | No tested request needs cookies. |
| Support an optional HTTP proxy (`UAKINO_PROXY_URL`), off by default. Keep its credentials out of logs. | Fallback for network blocks. |

A challenge page or network error means "source unavailable": HTTP 503 before a job exists, `Failed` after.

## Identity

- Title key: the numeric `news_id`. The title ID keeps the readable form `<news_id>-<slug>`, e.g. `13059-eyforya-2-sezon`. Build page URLs as `/<title ID>.html`.
- Leave the category path out of IDs. UAKino puts the category in the path, so a category move breaks it.
- Each season has its own page and `news_id`. `ul.seasons` links the seasons of one series.
- Voice key: a hash of the normalized `data-voice` name. `data-id` identifies a voice group, and its order can change.

## Quirks

- Search returns news posts. Skip items without `.full-quality`.
- English and Ukrainian queries return different sets. `Breaking Bad` misses season page `14024`, and `Пуститися берега` finds it. Search with both titles.
- The page's `Озвучення` text and the playlist voice names disagree, e.g. `FanVoxUA` vs `FanWoxUA`. Use playlist names only.
- Movies come in two forms: AJAX voices, or `iframe#pre` with `ERR_NOT_DATA` from the playlist endpoint. Trailers also use `iframe#pre` (YouTube). Accept only player hosts.
- Pages never say a season is finished. Finished seasons still gain voices and episodes: Euphoria S2 (2022) gained voice `Мова жестів` on 18.09.2026.
- Voices within one season can have different episode counts. Chainsaw Man S1: nine voices have 1–12, Flame Studio has 1–3.

## Cache

The `uakino` package owns this policy and stores it in Redis.

| Data | TTL |
|---|---|
| Search results | not cached |
| Title page and playlist, ongoing | not cached |
| Title page and playlist, finished (all voices have the TMDb episode count, last episode aired; movies count as finished) | 1 month |
| Episode data: quality, size, duration | 1 month |
| HLS master URL | 1 month. Delete and resolve again when a playlist fetch fails. |

The caller passes the expected TMDb episode count. The `uakino` package has no TMDb dependency.

## Test titles

| Title ID | Case |
|---|---|
| `13059-eyforya-2-sezon` | Serial season 2, 3 voices × 8 episodes, sibling seasons `9828`, `33384` |
| `15577-lyudina-benzopila-1-sezon` | Anime serial, 10 voices, unequal episode counts |
| `312-shrek-2` | Movie with AJAX voices and VTT subtitles |
| `2403-afera-tomasa-krauna` | Movie with inline `iframe#pre` |

Fixtures: saved HTML for these titles, plus 2–3 segments of the lowest variant (480p) of one episode. Live mode (`UAKINO_LIVE=1`) fetches fresh HTML, runs the parsers and overwrites the fixtures.
