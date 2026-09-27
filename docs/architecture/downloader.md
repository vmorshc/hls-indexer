# Downloader

Code: `internal/sabnzbd` (accepts jobs), `internal/jobs` (state), `internal/downloader` (worker role).

## Job lifecycle

```
addfile → Queued → Downloading → Completed
                 ↘ Paused ↗     ↘ Failed → retry → new job
```

- `addfile` decodes the release ID, stores the job in Redis, enqueues it and returns `jobId`.
- The worker claims jobs by priority. `worker.jobs` sets how many run at once (default 1).
- A worker restart re-queues `Downloading` jobs. They start again from zero with clean staging.
- `Downloading` covers resolve, segment download, mux and validation.

## Pipeline

1. **Resolve.** Decode the release ID and call `Source.Resolve`. It returns the master m3u8, subtitle URLs and labels. Pick the best variant and read the media playlist.
2. **Probe.** Run `ffprobe` on the first segment. H.264 video and AAC audio get stream copy. Other codecs get a re-encode to H.264 and AAC.
3. **Download.** Fetch segments in parallel (`worker.segment_concurrency`, default 8). Buffer each segment fully in memory. Retry a segment until its bytes are complete. Never skip a segment.
4. **Mux.** Start `ffmpeg` as a subprocess. Write segments to `pipe:0` in playlist order. Convert each VTT to SRT in Go and feed it through its own pipe (`pipe:3`, `pipe:4`, …). Set track language (`ukr`, `eng`) and title from the source label. Output: `/data/incomplete/<jobId>/<release title>.mkv`. No segment files touch the disk.
5. **Validate.** All of these must pass:
   - The ledger shows every segment written.
   - `ffmpeg` exited 0.
   - `ffprobe` shows 1 video, 1 audio and the expected subtitle tracks.
   - A/V duration matches the sum of `EXTINF` within a tolerance calibrated on real streams.

   `ffmpeg` exits 0 on premature input EOF, so the exit code alone proves nothing.
6. **Publish.** fsync the file, then rename `/data/incomplete/<jobId>` to `/data/downloads/<jobId>`. Mark `Completed` with `storage=/data/downloads/<jobId>`.

Progress for `queue`: completed segments and bytes against the playlist total and the size estimate.

## Failures

| Case | Action |
|---|---|
| Playlist fetch fails with a cached HLS URL | The source deletes the cached URL. The worker resolves again once. |
| Segment retries exhausted, codec error, validation fails | `Failed` with `fail_message`, staging removed |
| SAB `retry` | New job, fresh resolve |
| Delete with `del_files=1` | Remove only `<jobId>` folders in `incomplete` and `downloads` |

## Runtime

The Go binary stays pure Go (`CGO_ENABLED=0`). The image carries static `ffmpeg` and `ffprobe` from `mwader/static-ffmpeg`.
