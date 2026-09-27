# API contract

HLS Indexer acts as a Newznab indexer and a SABnzbd download client for Sonarr and Radarr. It implements only the fields these clients read.
Full profile with request and response examples: `~/notes/HLS bridge — API-контракти та взаємодії.md`. It was verified against the code of Sonarr `v4.0.19.2979` and Radarr `v6.3.0.10514`.

## Boundaries

1. One service, two endpoints: `/indexer/api` (Newznab) and `/downloader/api` (SABnzbd).
2. *arr can reach the indexer through Prowlarr. *arr calls the downloader directly.
3. Only `addfile` creates a job. Search and `t=get` never start work. The API never waits for a download.
4. *arr and HLS Indexer mount `/data` at the same path. `/data/incomplete` and `/data/downloads` share one filesystem.
5. HLS Indexer makes no callbacks to *arr. *arr polls queue and history, then imports the file itself.

## Auth and formats

- Every call except `t=caps` carries `apikey`. The indexer and downloader have separate keys. Keep keys out of logs.
- Newznab returns UTF-8 XML. SABnzbd returns JSON (`output=json`).

## Newznab: `/indexer/api`

| `t` | Params | Result |
|---|---|---|
| `caps` | none | `search q`, `tv-search q,tvdbid,season,ep`, `movie-search q,tmdbid,imdbid`. Categories `2000` Movies, `5000` TV. Limits: default 100, max 100. |
| `tvsearch` | `q` or `tvdbid`, `season`, `ep`, `cat` | Episode releases. `season=2026&ep=09/15` is a date search. For anime, Sonarr sends an absolute number as `q=12` next to `tvdbid`. |
| `movie` | `q` or `tmdbid` or `imdbid` (without `tt`) | Movie releases |
| `search` | `q` | Radarr's title fallback: `q=Title 1999`. Sonarr's anime search: `q=Title 12`. |
| `get` | `id` | Internal NZB |

- Common params: `cat` (comma list), `offset` (default 0), `limit` (1–100, default 100), `extended=1` (no effect).
- A request with no `q` and no ID is an RSS request. v1 returns an empty channel.
- Until search and `t=get` ship, a search with `q` or an ID and `t=get` return error `203`.
- Response: RSS with `newznab:response offset total`. `total` counts the filtered set. Order is stable, newest first.
- Item: `guid` = release ID, `title`, `pubDate`, `enclosure` → `t=get` URL (absolute, from `http.public_url`), attrs `size`, `category`, and `tvdbid` / `tmdbid` / `imdb` only when verified. `size` estimates the media, not the NZB.
- Empty result: channel without items, `total="0"`. A source error is an error, never an empty result.

NZB from `t=get`:

```xml
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"><file subject="hls-indexer:<releaseId>"/></nzb>
```

The NZB carries no stream URLs, cookies or tokens.

## SABnzbd: `/downloader/api`

| `mode` | Behaviour |
|---|---|
| `version` | `{"version":"4.0.0"}` (emulated SAB version) |
| `get_config`, `fullstatus`, `get_cats` | `complete_dir=/data/downloads`, categories `sonarr` and `radarr` with empty `dir`, sorting off, `history_retention_option=all` |
| `addfile` | Multipart NZB. Accept only `hls-indexer:<releaseId>` and reject external XML entities. Persist the job, then return `{"status":true,"nzo_ids":["<jobId>"]}`. The same active release in the same category returns the existing `jobId`. `priority`: `-100` default, `-2` paused, `-1` low, `0` normal, `1` high, `2` force. |
| `queue` | Filter param `category`, item field `cat`. Fields: `mb`, `mbleft` (MiB), `percentage`, `timeleft` (`H:MM:SS`). Status `Queued`, `Downloading` (covers resolve, download, mux and validate) or `Paused`. `limit=0` returns all. |
| `history` | Item field `category`. Fields: `status` `Completed` or `Failed`, `fail_message`, `bytes`, `download_time` (s), `storage` = `/data/downloads/<jobId>`. Newest first. Filter by category before paging. |
| `retry` | New job, fresh resolve. Returns `{"status":true,"nzo_id":"<newJobId>"}`. |
| `queue&name=delete` | Stop the worker, drop the job. `del_files=1` deletes only this job's files. |
| `history&name=delete` | `archive=1` archives the entry, `archive=0` deletes it. `del_files` touches only this job's folder. Deleting a job that is already gone succeeds. |
| `queue&name=pause|resume` | Pause or resume one job |

Empty queue or history: `slots: []`.

## Errors

| Case | Response |
|---|---|
| Newznab logical error | HTTP 200 `<error code description/>`: `100` bad key, `200` missing param, `202` unknown `t`, `203` function not available, `300` unknown release |
| SAB logical error | HTTP 200 `{"status":false,"error":"..."}`: `API Key Required` (no key), `API Key Incorrect`, `Unknown release`, `not implemented` (unknown mode). Every mode, `version` included, checks the key. |
| Source down before a job exists | HTTP 503, no job |
| Failure after `addfile` | History `Failed` with `fail_message` |

## Invariants

1. One `jobId` follows the job from `addfile` through queue to history, and it survives restarts.
2. `Completed` means muxed, validated and atomically published.
3. End-to-end gate: in both Sonarr and Radarr, run Test indexer/client → Search → Grab → Completed → import → cleanup.
