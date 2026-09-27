# HLS Indexer

HLS Indexer lets Sonarr and Radarr find and download Ukrainian-dubbed movies, series, anime and cartoons from open streaming sites. The first source is [UAKino](https://uakino.best).

Status: design stage. The code is not written yet.

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

Local run against the compose Redis:

```sh
go run ./cmd/hls-indexer api
go run ./cmd/hls-indexer worker
```

## Configuration

Three layers, later wins:

1. `config/default.yaml`, baked into the image.
2. An optional override file, `--config <path>`. HLS Indexer merges its keys over the defaults.
3. Env vars.

Compose hardcodes local values.

Secrets exist only as env vars:

| Env | Purpose |
|---|---|
| `INDEXER_API_KEY` | `apikey` for `/indexer/api` |
| `DOWNLOADER_API_KEY` | `apikey` for `/downloader/api` |
| `TMDB_API_KEY` | TMDb lookups |
| `REDIS_URL` | Redis connection |
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
| `uakino.base_url` | `UAKINO_BASE_URL` | `https://uakino.best` |
| `uakino.rps` | `UAKINO_RPS` | `1` |

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

```sh
go test ./...                                          # unit, fixture and snapshot tests
go test ./... -update                                  # rewrite golden files
UAKINO_LIVE=1 go test ./internal/source/uakino/...     # live UAKino, overwrites fixtures
```

## Docs

- [docs/architecture/overview.md](docs/architecture/overview.md): roles, modules, deployment.
- [docs/uakino/](docs/uakino/): UAKino requests and rules.
