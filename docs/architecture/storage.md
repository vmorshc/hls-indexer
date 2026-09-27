# Storage

Redis is the only store. Every key uses the prefix `hls-indexer:`. Compose runs Redis with `appendonly yes` because accepted jobs must survive restarts.

| Data | Owner | Lifetime |
|---|---|---|
| Jobs: release ID, category, priority, status, progress, `fail_message`, timestamps | `internal/jobs` | until history delete with `archive=0` |
| Queue of pending job IDs, by priority | `internal/jobs` | until claimed |
| Active job per release and category, for `addfile` dedup | `internal/jobs` | while the job is active |
| History, newest first; archived entries flagged | `internal/jobs` | until deleted |
| Source cache | `internal/source/<name>` | source policy, see [UAKino cache](../uakino/rules.md#cache) |
| TMDb: ID → titles, year, season episode lists | `internal/metadata/tmdb` | 1 month |

- Releases and search results are never stored. The release ID carries everything the worker needs.
- Only the worker writes progress and terminal status. The API writes job creation, pause/resume and deletes.
