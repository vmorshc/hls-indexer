# Downloader

Code: `internal/sabnzbd` (accepts jobs), `internal/jobs` (state), `internal/downloader` (worker role).

## Job lifecycle

```
addfile → Queued → Downloading → Completed | Failed → retry → new job
Queued, Downloading → pause → Paused → resume → Queued
addfile with priority -2 → Paused
```

- `addfile` decodes the release ID, stores the job in Redis, enqueues it and returns `jobId`. The job keeps the upload's file name without `.nzb` as the release title (*arr names the upload after it). Without a file name it uses the release ID with `:` → `_`.
- The worker claims jobs by priority, then age. `worker.jobs` sets how many run at once (default 1). `-100` is stored as `0`.
- Priority `-2` adds the job `Paused`, out of the queue. Resume queues it with priority `0`, as in SABnzbd.
- Pause takes a `Queued` job out of the queue. On a `Downloading` job, the worker checks the job every second and stops the run: staging removed, progress reset, no status write. Resume queues the job again. It starts from zero.
- Each claim starts a new run (`run` counter on the job). Progress and terminal writes apply only while their run is current and the job is `Downloading`. A paused, resumed or deleted job ignores its old run. A run that publishes after a pause removes its published folder. The worker never runs two runs of one job at once.
- A worker restart re-queues `Downloading` jobs. They start again from zero with clean staging. `Paused` jobs stay paused.
- `Downloading` covers resolve, segment download, mux and validation.

## Pipeline

1. **Resolve.** Decode the release ID, load the title page and find the voice (by hash) and episode. Call `Source.Resolve`. It returns the master m3u8 and subtitle tracks: VTT URL, label and language. Pick the best variant and read the media playlist. A playlist without `EXT-X-ENDLIST` fails. Download each VTT (same retries as segments) and convert it to SRT in Go. A subtitle that fails to download or parse fails the job.
2. **Probe.** Download the first segment and pipe it to `ffprobe`. H.264 video and AAC audio get stream copy. Other video is re-encoded with `libx264 -pix_fmt yuv420p` (8-bit plays everywhere), preset `worker.x264_preset` (default `veryfast`), CRF `worker.x264_crf` (default 20). Other audio is re-encoded with `aac` at `worker.aac_bitrate` (default `192k`). A segment without a video or audio stream fails the job. The probed segment is the first one fed to `ffmpeg`, not downloaded again.
3. **Download.** Fetch segments in parallel (`worker.segment_concurrency`, default 8). At most that many segments sit in memory. A segment is complete when the status is 200 and the body matches `Content-Length`. A short body or a non-200 status is retried: `worker.segment_attempts` tries (default 5), first wait `worker.segment_backoff` (default 1 s), doubled each time. When the tries run out, the job fails. Never skip a segment.
4. **Mux.** Start `ffmpeg` as a subprocess. Write segments to `pipe:0` in playlist order. Write each SRT to its own pipe (`pipe:3`, `pipe:4`, …, ffmpeg's extra file descriptors). Map the first video and first audio track and one subtitle track per pipe. Video and audio use the codec args from the probe, subtitles stream copy. Audio language `ukr`. A subtitle track gets the source's language (`ukr`, `eng`, none if unknown) and the source label as title. Output: `/data/incomplete/<jobId>/<release title>.mkv`. No segment or subtitle files touch the disk.
5. **Validate.** All of these must pass:
   - The ledger shows every segment written.
   - `ffmpeg` exited 0.
   - `ffprobe` shows 1 video, 1 audio and one subtitle track per source subtitle. Subtitle durations are not checked.
   - Each track's duration (MKV `DURATION` tag) is within **2 s** of the sum of `EXTINF`.

   `ffmpeg` exits 0 on premature input EOF, so the exit code alone proves nothing.

   Tolerance calibration: four whole 480p UAKino streams piped the same way (September 2026):

   | Stream | EXTINF sum | Video Δ | Audio Δ |
   |---|---|---|---|
   | Shrek 2 (`vod/83766`) | 5543.70 s | −0.32 s | −0.28 s |
   | Euphoria S2E1 (`vod/51968`) | 3670.41 s | −0.20 s | −0.17 s |
   | Chainsaw Man E1 (`vod/76971`) | 1525.02 s | +0.05 s | +1.11 s |
   | Thomas Crown Affair (`vod/128413`) | 6797.08 s | +0.05 s | +0.21 s |

   The shortest segment is about 4.6 s, so 2 s still catches a lost segment. Single segments do not match their `EXTINF` (off by up to 2 s), only whole streams do. Tests with 2–3 segments use the probed durations as `EXTINF`. The re-encode test uses the same 3 segments converted to MPEG-2 video and MP2 audio (`internal/downloader/testdata/mpeg2-*.ts`).
6. **Publish.** fsync the file and the staging folder, then rename `/data/incomplete/<jobId>` to `/data/downloads/<jobId>`. Mark `Completed` with `storage=/data/downloads/<jobId>`. The worker retries a failed status write until Redis answers.

Progress for `queue`: completed segments and bytes against the playlist total and the size estimate.

## Failures

| Case | Action |
|---|---|
| Playlist fetch fails with a cached HLS URL | The source deletes the cached URL. The worker resolves again once. |
| Source outage, segment retries exhausted, codec error, validation fails | `Failed` with `fail_message`, staging removed |
| SAB `retry` of a `Failed` job | New job with the same release, title, category and priority. It resolves again. The failed job leaves history, as in SABnzbd. When the same release is already active in the category, retry returns that job's ID (`addfile` dedup). |
| Delete with `del_files=1` | Remove only `<jobId>` folders in `incomplete` and `downloads` |

## Runtime

The Go binary stays pure Go (`CGO_ENABLED=0`). The image carries static `ffmpeg` and `ffprobe` from `mwader/static-ffmpeg`.
