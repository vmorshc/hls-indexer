# Downloader

Code: `internal/sabnzbd` (accepts jobs), `internal/jobs` (state), `internal/downloader` (worker role).

## Job lifecycle

```
addfile → Queued → Downloading → Completed
                 ↘ Paused ↗     ↘ Failed → retry → new job
```

- `addfile` decodes the release ID, stores the job in Redis, enqueues it and returns `jobId`. The job keeps the upload's file name without `.nzb` as the release title (*arr names the upload after it). Without a file name it uses the release ID with `:` → `_`.
- The worker claims jobs by priority, then age. `worker.jobs` sets how many run at once (default 1). *Not built yet:* `Paused`. A `-2` job queues last and still runs.
- A worker restart re-queues `Downloading` jobs. They start again from zero with clean staging.
- `Downloading` covers resolve, segment download, mux and validation.

## Pipeline

1. **Resolve.** Decode the release ID, load the title page and find the voice (by hash) and episode. Call `Source.Resolve`. It returns the master m3u8, subtitle URLs and labels. Pick the best variant and read the media playlist. A playlist without `EXT-X-ENDLIST` fails.
2. **Probe.** Run `ffprobe` on the first segment. H.264 video and AAC audio get stream copy. Other codecs get a re-encode to H.264 and AAC. *Not built yet: v0 always stream-copies.*
3. **Download.** Fetch segments in parallel (`worker.segment_concurrency`, default 8). At most that many segments sit in memory. A segment is complete when the status is 200 and the body matches `Content-Length`. v0 tries each segment 5 times (backoff 1 s, doubled). Never skip a segment.
4. **Mux.** Start `ffmpeg` as a subprocess. Write segments to `pipe:0` in playlist order. Map the first video and first audio track, stream copy, audio language `ukr`. Output: `/data/incomplete/<jobId>/<release title>.mkv`. No segment files touch the disk. *Not built yet:* convert each VTT to SRT in Go and feed it through its own pipe (`pipe:3`, `pipe:4`, …) with language (`ukr`, `eng`) and title from the source label.
5. **Validate.** All of these must pass:
   - The ledger shows every segment written.
   - `ffmpeg` exited 0.
   - `ffprobe` shows 1 video and 1 audio track (subtitle tracks come with subtitle muxing).
   - Each track's duration (MKV `DURATION` tag) is within **2 s** of the sum of `EXTINF`.

   `ffmpeg` exits 0 on premature input EOF, so the exit code alone proves nothing.

   Tolerance calibration: four whole 480p UAKino streams piped the same way (September 2026):

   | Stream | EXTINF sum | Video Δ | Audio Δ |
   |---|---|---|---|
   | Shrek 2 (`vod/83766`) | 5543.70 s | −0.32 s | −0.28 s |
   | Euphoria S2E1 (`vod/51968`) | 3670.41 s | −0.20 s | −0.17 s |
   | Chainsaw Man E1 (`vod/76971`) | 1525.02 s | +0.05 s | +1.11 s |
   | Thomas Crown Affair (`vod/128413`) | 6797.08 s | +0.05 s | +0.21 s |

   The shortest segment is about 4.6 s, so 2 s still catches a lost segment. Single segments do not match their `EXTINF` (off by up to 2 s), only whole streams do. Tests with 2–3 segments use the probed durations as `EXTINF`.
6. **Publish.** fsync the file and the staging folder, then rename `/data/incomplete/<jobId>` to `/data/downloads/<jobId>`. Mark `Completed` with `storage=/data/downloads/<jobId>`. The worker retries a failed status write until Redis answers.

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
