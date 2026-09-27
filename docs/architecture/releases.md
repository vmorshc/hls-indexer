# Search and releases

Code: `internal/catalog` (flow), `internal/release` (ID, title, group), `internal/metadata/tmdb`.

## Release

One release is one episode in one voice, or one movie in one voice, at the best HLS variant. HLS Indexer publishes no season packs, for three reasons:

- Sonarr rejects packs in single-episode searches.
- Sonarr rejects a pack while any episode of the season has not aired.
- Season searches accept single episodes.

## Release ID

Stateless. `t=get`, `addfile` and the worker decode it without storage.

```
uakino:13059-eyforya-2-sezon:s02e03:9f86d081   episode
uakino:312-shrek-2:movie:ab12cd34              movie
<source>:<title ID>:<sNNeNN | movie>:<voice hash>
```

- Title ID: defined by the source. For UAKino it is `<news_id>-<slug>`.
- Season: from the source page. Episode: the source episode number.
- Voice hash: first 8 hex characters of SHA-256 over the voice name, trimmed, whitespace collapsed and lowercased.

## Release title

```
Euphoria.S02E03.1080p.WEB-DL.UKR-DniproFilm
Shrek.2.2004.1080p.WEB-DL.UKR-TakTrebaProdakshn
```

- Name: the TMDb English title when the request carried an ID. Otherwise the source's original title with suffixes like `2 season` removed. Words are joined with dots.
- Resolution: the best variant mapped with the *arr media-info thresholds (width ≥ 1800 or height ≥ 1000 → `1080p`). 1920×816 → `1080p`. Never publish raw dimensions.
- `UKR` sets the language in Sonarr and Radarr. Bare `UA` and Cyrillic names do not.
- Group: the voice name converted to ASCII so the *arr group parser keeps it:
  1. Transliterate Cyrillic.
  2. Replace `+` with `Plus`.
  3. Split into words, uppercase the first letter of each word and keep the rest, drop other non-alphanumerics.

  | Voice | Group |
  |---|---|
  | `Gwean & Maslinka` | `GweanMaslinka` |
  | `У куточку в таверні` | `UKutochkuVTaverni` |
  | `1+1` | `1Plus1` |

## Size and quality

Size = best-variant `BANDWIDTH` × sum of `EXTINF` / 8. It is an estimate.
The flow samples one episode per voice and reuses its quality and size for the voice's other episodes. It uses real per-episode data when cached.

## Search flow

1. No `q` and no ID: return an empty channel. RSS is off in v1.
2. ID (`tvdbid`, `tmdbid`, `imdbid`): TMDb gives the English title, the Ukrainian (`uk-UA`) title, year and season episode list.
3. `q` only: strip a trailing year and use it as a filter.
4. Anime absolute number (`q=12` with `tvdbid`, or `t=search&q=Title 12`): convert it to season and episode with TMDb episode data.
5. Search the source with every title, first page only. Merge by source title key.
6. Filter by year and season. Follow sibling season links when the requested season is on another page.
7. Load each title's voices and episodes. Filter by `ep`.
8. Build releases, sort by `pubDate` desc, then by release ID. Apply `offset` and `limit`.

Put `tvdbid`, `tmdbid` and `imdb` attrs only on releases matched through a TMDb lookup. Sonarr searches by ID first and falls back to title only when the ID search returns nothing. Radarr's title fallback uses generic `t=search`.

## Sonarr setup this model needs

- Indexer categories `5000`, Anime Categories `5000`, Anime Standard Format Search on.
- `Season Search Maximum Single Episode Age` = 0. Otherwise Sonarr rejects single episodes of old seasons.

Known risk: UAKino season pages can split seasons differently from TVDB/TMDb, especially for anime.
