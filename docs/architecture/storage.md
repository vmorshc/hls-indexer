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

Job keys (`internal/jobs`):

| Key | Type | Holds |
|---|---|---|
| `hls-indexer:job:<jobId>` | hash | job fields, `run` = claim counter, `archived=1` after history delete with `archive=1` |
| `hls-indexer:queue` | zset | pending job IDs, score = −priority × 10¹³ + created ms. Paused jobs are not in it. |
| `hls-indexer:active` | zset | queued, paused and running job IDs, score = created ms |
| `hls-indexer:dedup:<category>:<releaseId>` | string | active job ID |
| `hls-indexer:history` | zset | finished job IDs, archived included, score = finished ms |

- Releases and search results are never stored. The release ID carries everything the worker needs.
- Only the worker writes progress and terminal status. The API writes job creation, pause/resume and deletes.
- Status changes that race run as Lua scripts: create with dedup, claim (only a `Queued` job), pause, resume, requeue, queue delete (only an active job), history delete (only a finished job), and finish (only the current run of a `Downloading` job).
