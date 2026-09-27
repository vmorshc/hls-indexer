# UAKino requests

UAKino has no API. The client scrapes HTML from a DLE-based site and one AJAX endpoint.
Required headers and limits: [rules.md](rules.md). Observed on 2026-09-27.

## Resolve chain

```
search → title page → playlist → player page → master m3u8 → media m3u8 → segments
```

## 1. Search

`POST /ua/`, form `do=search&subaction=search&story=<query>`.

- Items: `div#dle-content div.movie-item.short-item`.
- Keep only items with `.full-quality` whose link is not under `/news/`. The rest are news posts.
- Year: `.movie-desk-item` with label `Рік виходу`. Local title: `a.movie-title` text.
- Title link: `a.movie-title[href]`. The last path segment `<news_id>-<slug>` is the title ID.
- `.full-season` shows the available range, e.g. `2 сезон / 1-12 серія`. It never shows the season total.
- 10 items per page. We read the first page only. Page N: `POST /index.php?do=search` with `from_page=N`. `search_start` has no effect.

## 2. Title page

`GET /<news_id>-<slug>.html` returns 301 to the canonical `/<category path>/<news_id>-<slug>.html`. Any text after `<news_id>-` works. `GET /index.php?newsid=<news_id>` redirects the same way. `/<news_id>.html` returns 404.

| Data | Selector |
|---|---|
| Ukrainian title | `h1` (pages carry duplicate `og:title` tags) |
| Original title | `.origintitle[itemprop=alternateName]`, e.g. `Euphoria 2 season` |
| Year, quality, dubbing text | `.film-info .fi-item`: labels `Рік виходу`, `Якість`, `Озвучення` |
| Season links | `ul.seasons.clearfix`: `li.season-active` holds the current season (`2 сезон`), other `li a` link to sibling season pages |
| Update date | `.mov-date time[datetime]` |
| Update notes | `.mylisttooltiptext-reason`, free text, e.g. `18.09.2026 - Додано 8 серію 2 сезону, Мова жестів` |
| Inline movie player | `iframe#pre[src]` pointing to a player host |

Pages have no IMDb, TMDb or TVDB IDs and no finished/ongoing status.

## 3. Playlist

`GET /engine/ajax/playlists.php?news_id=<news_id>&xfield=playlist` with `X-Requested-With: XMLHttpRequest`.

- Success: JSON `{"success":true,"response":"<html>"}`.
- Without the header: HTTP 200 with body `error`.
- Movies with an inline player: `{"success":false,"message":"ERR_NOT_DATA"}`. Use `iframe#pre` from the title page.
- Cookies, `dle_login_hash` and `time` are not needed.

Items: `.playlists-videos .playlists-items li[data-file]`. Skip `li` without `data-file`.

| Kind | `li` text | `data-voice` | `data-id` |
|---|---|---|---|
| Serial | `Серія N` | voice name | voice group, e.g. `0_0`, same for all episodes of a voice |
| Movie | voice name | voice name | `0` |

`data-file` is protocol-relative, e.g. `//ashdi.vip/vod/51968`, or absolute. Resolve it against the final playlist URL, which gives `https:`.

## 4. Player page

`GET https://ashdi.vip/vod/<id>`. The HTML contains `new Playerjs({...})`:

- `file`: master m3u8 URL.
- `subtitle`: `[Label]url,[Label]url`, e.g. `[Українські]https://ashdi.vip/player/subtitle/83766_ua.vtt`. Often empty. Labels seen: `Українські`, `Англійські`. Language: a label starting with `укр` → `ukr`, `англ` or `eng` → `eng` (case-insensitive), else none.
- `poster`: image URL.

Other player hosts are untested.

## 5. HLS

- Master playlist: 3 variants with `BANDWIDTH` and `RESOLUTION`, e.g. `2128000 1920x1080`, `1096000 1280x720`, `714000 854x480`. Movies can be `1920x816`. No `EXT-X-MEDIA` tracks.
- URLs carry an opaque path token, e.g. `.../hls/<token>/index.m3u8`. Its lifetime is unknown.
- Media playlist: VOD with `EXT-X-ENDLIST`, 10 s target duration, absolute `.ts` URLs on `*.ashdi.vip/content/stream/...`, no `EXT-X-KEY`.
- Segments: MPEG-TS with H.264 High (B-frames) and one AAC-LC stereo 44.1 kHz track. A 1080p segment is about 2.5 MB.
- Subtitles: WebVTT with plain cues, no styles or cue settings.
- Player pages, playlists, segments and VTT need no Referer, Origin or cookies.

## 6. RSS

`GET /rss.xml` lists updates per title, without episode or voice. v1 does not use it.
