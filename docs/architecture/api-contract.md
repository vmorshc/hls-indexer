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
- Response: RSS with `newznab:response offset total`. `total` counts the filtered set. Order is stable, newest first.
- `cat` matches by parent category: `5070` selects `5000` releases.
- Item: `guid` = release ID, `title`, `pubDate`, `link` and `enclosure` → `t=get` URL with `apikey` (absolute, from `http.public_url`), attrs `size`, `category`, and `tvdbid` / `tmdbid` / `imdb` (digits without `tt`) only when verified. `size` estimates the media, not the NZB.
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
| `addfile` | Multipart NZB in field `name` (or `nzbfile`). Accept only one `hls-indexer:<releaseId>` file and reject any DTD or entity declaration with `Unknown release`. The upload file name without `.nzb` (or `nzbname`) becomes the output file name. Persist the job, then return `{"status":true,"nzo_ids":["<jobId>"]}`. The same active release in the same category returns the existing `jobId`. `priority`: `-100` default (orders as `0`), `-2` paused, `-1` low, `0` normal, `1` high, `2` force. |
| `queue` | Filter param `category`, item field `cat`. Paging `start`, `limit`. Fields: `mb`, `mbleft` (MiB), `percentage`, `timeleft` (`H:MM:SS`). Status `Queued`, `Downloading` (covers resolve, download, mux and validate) or `Paused`. `limit=0` returns all. |
| `history` | Item field `category`. Fields: `nzo_id`, `name`, `status` `Completed` or `Failed`, `fail_message`, `bytes`, `download_time` (s), `storage` = `/data/downloads/<jobId>`. Newest first. Filter params `category` and `status` apply before paging (`start`, `limit`). Archived entries are hidden. `archive=1` lists only them. |
| `retry` | `value=<jobId>` of a `Failed` job. New job, fresh resolve. The failed job leaves history. Returns `{"status":true,"nzo_id":"<newJobId>"}`. Any other ID returns `Unknown job`. |
| `queue&name=delete` | `value=<jobId>`. Drop a `Queued`, `Paused` or `Downloading` job. The worker stops its run within a second. `del_files=1` deletes only `<jobId>` in `incomplete` and `downloads`. Returns `{"status":true}`. |
| `history&name=delete` | `value=<jobId>` of a finished job. `archive=1` (default, as in SABnzbd 4) archives the entry, `archive=0` deletes it. `del_files=1` deletes only `<jobId>` in `incomplete` and `downloads`. Returns `{"status":true}`. |

Deletes succeed for a job that is already gone or in the other list (nothing changes). Files are deleted only for values shaped like a job ID (`hls_` + 16 hex) and never for a job in the other list. A failed file delete returns `Failed to delete files`.
| `queue&name=pause|resume` | `value=<jobId>`. Pause or resume the job. Returns `{"status":true,"nzo_ids":[...]}`. Pausing a paused job or resuming a queued one succeeds. A job not in the queue returns `Unknown job`. |

Empty queue or history: `slots: []`.

## Errors

| Case | Response |
|---|---|
| Newznab logical error | HTTP 200 `<error code description/>`: `100` bad key, `200` missing param, `202` unknown `t`, `300` unknown release (malformed ID or unknown source prefix) |
| SAB logical error | HTTP 200 `{"status":false,"error":"..."}`: `API Key Required` (no key), `API Key Incorrect`, `Unknown release`, `Unknown job` (`retry`, pause, resume), `Failed to delete files` (delete with `del_files=1`), `not implemented` (unknown mode). Every mode, `version` included, checks the key. |
| Source or TMDb down before a job exists | HTTP 503 with a plain-text body, no job |
| Failure after `addfile` | History `Failed` with `fail_message` |

## Invariants

1. One `jobId` follows the job from `addfile` through queue to history, and it survives restarts.
2. `Completed` means muxed, validated and atomically published.
3. End-to-end gate: in both Sonarr and Radarr, run Test indexer/client → Search → Grab → Completed → import → cleanup.
