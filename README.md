# HLS Indexer

HLS Indexer lets Sonarr and Radarr find and download Ukrainian-dubbed movies, series, anime and cartoons from open streaming sites. The first source is [UAKino](https://uakino.best).

Status: v0 in progress. Config, Docker, Newznab caps and search, `t=get` and SABnzbd config work. Downloads are not implemented yet.

## Goals

1. Scan open sources for titles with Ukrainian voice-over.
2. Serve a Newznab-compatible indexer API over those sources.
3. Act as a SABnzbd-compatible download client that turns HLS streams into MKV files.

## How it works

1. Sonarr or Radarr searches `/indexer/api`. HLS Indexer asks TMDb and the source, then returns one release per episode or movie and voice, e.g. `Euphoria.S02E03.1080p.WEB-DL.UKR-DniproFilm`.
2. *arr grabs a release and posts its NZB to `/downloader/api`. The NZB carries only a release ID.
3. The worker resolves the HLS stream, downloads segments, muxes an MKV with ffmpeg and publishes it to `/data/downloads/<jobId>`.
4. *arr polls the queue and history, then imports the file from the shared disk.

## Requirements

- Docker with Compose.
- For local runs and tests: Go 1.27, plus `ffmpeg` and `ffprobe` on `PATH`.
- A TMDb API key.

## Build

```sh
docker build -t hls-indexer .
go build -o bin/hls-indexer ./cmd/hls-indexer
```

## Start

```sh
docker compose up --build        # redis + api (:8080) + worker, ./data mounted at /data
```

Compose sets the local API keys `indexer-local-key` and `downloader-local-key` and takes `TMDB_API_KEY` from the shell.

Local run against the compose Redis:

```sh
go run ./cmd/hls-indexer api
go run ./cmd/hls-indexer worker
```

## Configuration

Three layers, later wins:

1. `config/default.yaml`, embedded in the binary. The image also ships it at `/etc/hls-indexer/default.yaml` as a template for overrides.
2. An optional override file, `--config <path>`. HLS Indexer merges its keys over the defaults.
3. Env vars.

Unknown keys in the override file are an error. Compose hardcodes local values.

Secrets exist only as env vars:

| Env | Purpose |
|---|---|
| `INDEXER_API_KEY` | `apikey` for `/indexer/api` |
| `DOWNLOADER_API_KEY` | `apikey` for `/downloader/api` |
| `TMDB_API_KEY` | TMDb lookups |
| `REDIS_URL` | Redis connection, both roles |
| `UAKINO_PROXY_URL` | Optional HTTP proxy for UAKino, off when empty |

Settings:

| Key | Env | Default |
|---|---|---|
| `http.addr` | `HTTP_ADDR` | `:8080` |
| `http.public_url` | `PUBLIC_URL` | `http://localhost:8080` (base for `t=get` links) |
| `paths.incomplete` | `INCOMPLETE_DIR` | `/data/incomplete` |
| `paths.downloads` | `DOWNLOADS_DIR` | `/data/downloads` |
| `worker.jobs` | `WORKER_JOBS` | `1` |
| `worker.segment_concurrency` | `SEGMENT_CONCURRENCY` | `8` |
| `worker.segment_attempts` | `SEGMENT_ATTEMPTS` | `5` (tries per segment and subtitle, then the job fails) |
| `worker.segment_backoff` | `SEGMENT_BACKOFF` | `1s` (first wait between tries, doubled each time) |
| `worker.x264_preset` | `X264_PRESET` | `veryfast` (re-encode of non-H.264 video) |
| `worker.x264_crf` | `X264_CRF` | `20` |
| `worker.aac_bitrate` | `AAC_BITRATE` | `192k` (re-encode of non-AAC audio) |
| `uakino.base_url` | `UAKINO_BASE_URL` | `https://uakino.best` |
| `uakino.rps` | `UAKINO_RPS` | `1` |
| `uakino.cache_ttl` | `UAKINO_CACHE_TTL` | `720h` (30 days, must be positive) |
| `uakino.player_hosts` | `UAKINO_PLAYER_HOSTS` (comma list) | `[ashdi.vip]` (inline movie players, subdomains included) |
| `tmdb.base_url` | `TMDB_BASE_URL` | `https://api.themoviedb.org/3` |

## Sonarr and Radarr setup

Indexer, type Newznab:

- URL `http://<host>:8080`, API Path `/indexer/api`, API key `INDEXER_API_KEY`.
- Categories: `5000` in Sonarr, `2000` in Radarr.
- Sonarr only: Anime Categories `5000`, Anime Standard Format Search on, Season Search Maximum Single Episode Age `0`.

Download client, type SABnzbd:

- Host `<host>`, port `8080`, URL Base `/downloader`, API key `DOWNLOADER_API_KEY`.
- Category `sonarr` or `radarr`. Completed Download Handling on.

Mount `/data` at the same path in *arr and HLS Indexer.

## Tests

Tests that need Redis use the separate test Redis from compose. Each test package claims its own database there and flushes it before each test, so packages run in parallel. Without `TEST_REDIS_URL` they skip.

```sh
docker compose up -d redis-test                        # test Redis on localhost:6380
export TEST_REDIS_URL=redis://localhost:6380/0
go test ./...                                          # unit, fixture and snapshot tests
go test ./internal/downloader -update                   # rewrite golden files
UAKINO_LIVE=1 go test ./internal/source/uakino/...     # live UAKino, overwrites fixtures
```

## Docs

- [docs/architecture/overview.md](docs/architecture/overview.md): roles, modules, deployment.
- [docs/uakino/](docs/uakino/): UAKino requests and rules.
