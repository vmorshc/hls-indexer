// Package downloader is the worker role: it claims jobs, resolves the stream,
// downloads segments, muxes them with ffmpeg, validates and publishes the MKV.
// It is the only package that runs ffmpeg.
package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/vmorshc/hls-indexer/internal/hls"
	"github.com/vmorshc/hls-indexer/internal/jobs"
	"github.com/vmorshc/hls-indexer/internal/release"
	"github.com/vmorshc/hls-indexer/internal/source"
)

// Worker runs jobs from the store.
type Worker struct {
	Jobs    *jobs.Store
	Sources []source.Source
	// Incomplete and Downloads are paths.incomplete and paths.downloads.
	Incomplete string
	Downloads  string
	// Parallel is worker.jobs.
	Parallel int
	Pipeline Pipeline
	Log      *slog.Logger
}

// claimWait bounds one blocking claim so the worker notices ctx cancel.
const claimWait = time.Second

// Run re-queues jobs a previous run left in Downloading, then processes jobs
// until ctx ends.
func (w *Worker) Run(ctx context.Context) error {
	if err := w.Jobs.Requeue(ctx); err != nil {
		return fmt.Errorf("requeue: %w", err)
	}
	n := max(w.Parallel, 1)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() { w.loop(ctx) })
	}
	wg.Wait()
	return nil
}

func (w *Worker) loop(ctx context.Context) {
	for ctx.Err() == nil {
		j, ok, err := w.Jobs.Claim(ctx, claimWait)
		if err != nil {
			if ctx.Err() == nil {
				w.Log.Error("claim", "err", err)
				sleep(ctx, claimWait)
			}
			continue
		}
		if ok {
			w.run(ctx, j)
		}
	}
}

// run processes one job and records its terminal status. A cancelled ctx
// leaves the job Downloading, so the next start re-queues it.
func (w *Worker) run(ctx context.Context, j jobs.Job) {
	log := w.Log.With("job", j.ID, "release", j.Release)
	log.Info("job started")
	stage := filepath.Join(w.Incomplete, j.ID)
	storage, size, err := w.process(ctx, j, stage)
	if ctx.Err() != nil {
		os.RemoveAll(stage)
		log.Info("job interrupted")
		return
	}
	if err != nil {
		os.RemoveAll(stage)
		log.Warn("job failed", "err", err)
		msg := err.Error()
		w.retry(ctx, log, "mark failed", func() error { return w.Jobs.Fail(ctx, j.ID, msg) })
		return
	}
	w.retry(ctx, log, "mark completed", func() error { return w.Jobs.Complete(ctx, j.ID, storage, size) })
	log.Info("job completed", "storage", storage, "bytes", size)
}

// retry repeats a status write until it succeeds or ctx ends. A published file
// must not stay invisible because Redis blinked.
func (w *Worker) retry(ctx context.Context, log *slog.Logger, what string, f func() error) {
	wait := 100 * time.Millisecond
	for {
		err := f()
		if err == nil || ctx.Err() != nil {
			return
		}
		log.Error(what, "err", err)
		sleep(ctx, wait)
		wait = min(wait*2, 10*time.Second)
	}
}

func (w *Worker) process(ctx context.Context, j jobs.Job, stage string) (string, int64, error) {
	rid, err := release.Parse(j.Release)
	if err != nil {
		return "", 0, err
	}
	src := w.source(rid.Source)
	if src == nil {
		return "", 0, fmt.Errorf("unknown source %q", rid.Source)
	}
	ep, err := episode(ctx, src, rid)
	if err != nil {
		return "", 0, err
	}
	stream, err := src.Resolve(ctx, ep)
	if err != nil {
		return "", 0, fmt.Errorf("resolve: %w", err)
	}
	best, media, err := w.Pipeline.Playlist(ctx, stream.Master)
	if err != nil {
		return "", 0, err
	}
	subs, err := w.Pipeline.Subtitles(ctx, stream.Subtitles)
	if err != nil {
		return "", 0, err
	}
	size := source.Media{Bandwidth: best.Bandwidth, Duration: media.Duration()}.Size()
	if err := w.Jobs.Start(ctx, j.ID, len(media.Segments), size); err != nil {
		return "", 0, err
	}

	if err := os.RemoveAll(stage); err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return "", 0, err
	}
	out := filepath.Join(stage, j.Title+".mkv")
	var written int64
	progress := func(done int, n int64) {
		written += n
		if err := w.Jobs.Progress(ctx, j.ID, done, written); err != nil {
			w.Log.Warn("progress", "job", j.ID, "err", err)
		}
	}
	if err := w.Pipeline.Mux(ctx, media, subs, out, progress); err != nil {
		return "", 0, err
	}
	return w.publish(stage, j.ID)
}

func (w *Worker) source(name string) source.Source {
	for _, s := range w.Sources {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

// episode finds the release's voice and episode on the title page.
func episode(ctx context.Context, src source.Source, rid release.ID) (source.Episode, error) {
	t, err := src.Title(ctx, rid.TitleID)
	if err != nil {
		return source.Episode{}, fmt.Errorf("title: %w", err)
	}
	for _, v := range t.Voices {
		if release.VoiceHash(v.Name) != rid.Voice {
			continue
		}
		for _, e := range v.Episodes {
			if rid.Movie() || e.Number == rid.Episode {
				return e, nil
			}
		}
	}
	return source.Episode{}, errors.New("release no longer on the source")
}

// publish syncs the staging folder and renames it into downloads. It returns
// the published folder and the MKV size.
func (w *Worker) publish(stage, id string) (string, int64, error) {
	var size int64
	entries, err := os.ReadDir(stage)
	if err != nil {
		return "", 0, err
	}
	for _, e := range entries {
		p := filepath.Join(stage, e.Name())
		if err := syncPath(p); err != nil {
			return "", 0, err
		}
		if fi, err := e.Info(); err == nil {
			size += fi.Size()
		}
	}
	if err := syncPath(stage); err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(w.Downloads, 0o755); err != nil {
		return "", 0, err
	}
	dst := filepath.Join(w.Downloads, id)
	if err := os.RemoveAll(dst); err != nil {
		return "", 0, err
	}
	if err := os.Rename(stage, dst); err != nil {
		return "", 0, err
	}
	if err := syncPath(w.Downloads); err != nil {
		return "", 0, err
	}
	return dst, size, nil
}

func syncPath(p string) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// Pipeline downloads and muxes one stream.
type Pipeline struct {
	HTTP *http.Client
	// Concurrency is worker.segment_concurrency.
	Concurrency int
	// Attempts per segment and the first backoff, doubled on each retry.
	Attempts int
	Backoff  time.Duration
	FFmpeg   string
	FFprobe  string
}

// Playlist reads the master playlist, picks the best variant and reads its media playlist.
func (p Pipeline) Playlist(ctx context.Context, master string) (hls.Variant, hls.Media, error) {
	body, base, err := p.get(ctx, master)
	if err != nil {
		return hls.Variant{}, hls.Media{}, fmt.Errorf("master playlist: %w", err)
	}
	vs, err := hls.ParseMaster(string(body), base)
	if err != nil {
		return hls.Variant{}, hls.Media{}, fmt.Errorf("master playlist: %w", err)
	}
	best := hls.Best(vs)
	body, base, err = p.get(ctx, best.URL)
	if err != nil {
		return hls.Variant{}, hls.Media{}, fmt.Errorf("media playlist: %w", err)
	}
	m, err := hls.ParseMedia(string(body), base)
	if err != nil {
		return hls.Variant{}, hls.Media{}, fmt.Errorf("media playlist: %w", err)
	}
	if !m.Ended {
		return hls.Variant{}, hls.Media{}, errors.New("media playlist: not VOD (no EXT-X-ENDLIST)")
	}
	return best, m, nil
}

// maxSegment caps one segment in memory. A 1080p segment is about 2.5 MB.
const maxSegment = 64 << 20

// get fetches a URL fully. It fails on a non-200 status or a short body.
func (p Pipeline) get(ctx context.Context, u string) ([]byte, *url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL carries a stream token
		}
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxSegment+1))
	if err != nil {
		return nil, nil, err
	}
	if len(b) > maxSegment {
		return nil, nil, errors.New("body too large")
	}
	if resp.ContentLength >= 0 && int64(len(b)) != resp.ContentLength {
		return nil, nil, fmt.Errorf("short body: %d of %d bytes", len(b), resp.ContentLength)
	}
	return b, resp.Request.URL, nil
}
