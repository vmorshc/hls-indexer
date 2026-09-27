# HLS Indexer

A Go service that serves a Newznab indexer and a SABnzbd download client to Sonarr and Radarr. It scrapes sources with Ukrainian dubs (UAKino first) and turns HLS streams into MKV files. Human overview, config and commands: [README.md](README.md).

## Docs

Read the matching doc before you change its area:

- Modules, package boundaries, `Source` interface, Docker: [docs/architecture/overview.md](docs/architecture/overview.md)
- Newznab or SABnzbd endpoints, params, errors: [docs/architecture/api-contract.md](docs/architecture/api-contract.md)
- Search flow, release ID, release title, voice → group: [docs/architecture/releases.md](docs/architecture/releases.md)
- Jobs, worker pipeline, ffmpeg, validation, publish: [docs/architecture/downloader.md](docs/architecture/downloader.md)
- Redis keys, TTLs, persistence: [docs/architecture/storage.md](docs/architecture/storage.md)
- UAKino requests, selectors, HLS layout: [docs/uakino/requests.md](docs/uakino/requests.md)
- UAKino headers, limits, IDs, quirks, cache, test titles: [docs/uakino/rules.md](docs/uakino/rules.md)

When a decision in code changes, update its doc in the same change.

## Layout

```
cmd/hls-indexer/          main, roles: api | worker
config/default.yaml       defaults, embedded in the binary (config/embed.go)
internal/app/             role wiring: API handler, api and worker runners
internal/testenv/         S1 test harness
internal/config/          defaults + override file + env
internal/newznab/         /indexer/api
internal/sabnzbd/         /downloader/api
internal/catalog/         search flow
internal/metadata/tmdb/   TMDb client
internal/release/         release ID, title, group
internal/source/          Source interface
internal/source/uakino/   UAKino client, parser, cache policy
internal/hls/             m3u8 parser
internal/jobs/            Redis jobs, queue, history
internal/downloader/      HLS → MKV worker
```

## Rules

- Source-specific types stay inside `internal/source/<name>`. Other packages use `internal/source` types.
- Secrets come from env only. Every other setting lives in the YAML config (`config/default.yaml` plus an override file). Env vars can override any setting.
- Libraries: `net/http`, `log/slog`, `github.com/redis/go-redis/v9`, `goquery`, `go.yaml.in/yaml/v4`. Ask before adding others.

## Tests

- HTTP API (S1): use `internal/testenv`. It runs the API in-process and plugs fake UAKino and TMDb servers. `Options.Worker` also runs the worker on the test Redis from `TEST_REDIS_URL` (`docker compose up -d redis-test`). Each test package claims its own database there, so packages run in parallel. `testenv.Redis` flushes that database. Tests that need Redis skip when the env var is unset.
- Source parsers: HTML fixtures in `testdata/`. Live mode (`UAKINO_LIVE=1`) fetches fresh HTML for the test titles, parses it and overwrites the fixtures.
- Helpers (`internal/release`, title parsing, conversions): table tests for the base scenarios only.
- Video pipeline: input is 2–3 real segments of the lowest variant, never a whole episode, served by a local test server. The golden file stores the `ffprobe` summary of the output MKV: tracks, codecs, languages, duration, packet counts. Rewrite goldens with `go test ./internal/downloader -update` (only that package defines the flag).

## Agent skills

### Issue tracker

Issues are tracked in GitHub Issues (`vmorshc/hls-indexer`) via the `gh` CLI. **ALWAYS** see `docs/agents/issue-tracker.md` if you work with issues.
