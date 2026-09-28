# Storage

Redis is the only store. Every key uses the prefix `hls-indexer:`. Compose runs Redis with `appendonly yes` because accepted jobs must survive restarts.

| Data | Owner | Lifetime |
|---|---|---|
| Jobs: release ID, category, priority, status, progress, `fail_message`, timestamps | `internal/jobs` | until queue delete, history delete with `archive=0` or `retry` |
| Queue of pending job IDs, by priority | `internal/jobs` | until claimed |
| Active job per release and category, for `addfile` dedup | `internal/jobs` | while the job is active |
| History, newest first; archived entries flagged | `internal/jobs` | until deleted |
| Source cache | `internal/source/<name>` | source policy, see [UAKino cache](../uakino/rules.md#cache) |
| TMDb: ID → titles, year, season episode lists | `internal/metadata/tmdb` | 1 month |

UAKino owns `hls-indexer:uakino:title:<news_id>`, a JSON string containing the parsed finished title and playlist. Its TTL is `uakino.cache_ttl` (default `720h`). Both roles share this cache. Ongoing titles and search results have no keys.

UAKino also owns JSON string keys `hls-indexer:uakino:media:<locator-sha256>` (sampled resolution, bandwidth, duration) and `hls-indexer:uakino:stream:<locator-sha256>` (master URL, subtitles). Both use `uakino.cache_ttl` without renewal on reads. Search alone writes media entries. A stream invalidation compares the stored value before deleting it to preserve concurrent replacements.

Job keys (`internal/jobs`):

| Key | Type | Holds |
|---|---|---|
| `hls-indexer:job:<jobId>` | hash | job fields, `run` = claim counter, `archived=1` after history delete with `archive=1`, `fresh_resolve=true` on SAB retry jobs to bypass the source stream cache |
| `hls-indexer:queue` | zset | pending job IDs, score = −priority × 10¹³ + created ms. Paused jobs are not in it. |
| `hls-indexer:active` | zset | queued, paused and running job IDs, score = created ms |
| `hls-indexer:dedup:<category>:<releaseId>` | string | active job ID |
| `hls-indexer:cancelled:<jobId>` | string | set by queue delete, TTL 1 day. A finish of a missing job fails only with it. |
| `hls-indexer:history` | zset | finished job IDs, archived included, score = finished ms |

- Releases and search results are never stored. The release ID carries everything the worker needs.
- Only the worker writes progress and terminal status. The API writes job creation, pause/resume and deletes.
- Status changes that race run as Lua scripts: create with dedup, claim (only a `Queued` job), pause, resume, requeue, queue delete (only an active job), history delete (only a finished job), and finish (only the current run of a `Downloading` job).
