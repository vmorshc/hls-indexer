package downloader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/vmorshc/hls-indexer/internal/hls"
	"github.com/vmorshc/hls-indexer/internal/source"
)

// DurationTolerance is the allowed gap between each track's duration and the
// sum of EXTINF. Calibrated on real UAKino streams, see docs/architecture/downloader.md.
const DurationTolerance = 2.0

// Subtitle is one subtitle track ready for the mux.
type Subtitle struct {
	Language string // ISO 639-2, empty if unknown
	Title    string
	SRT      []byte
}

// Subtitles downloads each VTT with the segment retries and converts it to SRT.
func (p Pipeline) Subtitles(ctx context.Context, subs []source.Subtitle) ([]Subtitle, error) {
	var out []Subtitle
	for _, s := range subs {
		vtt, err := p.segment(ctx, s.URL)
		if err != nil {
			return nil, fmt.Errorf("subtitle %q: %w", s.Label, err)
		}
		srt, err := vttToSRT(vtt)
		if err != nil {
			return nil, fmt.Errorf("subtitle %q: %w", s.Label, err)
		}
		out = append(out, Subtitle{Language: s.Language, Title: s.Label, SRT: srt})
	}
	return out, nil
}

// Mux downloads the segments in parallel and writes them to ffmpeg's stdin in
// playlist order. Segments stay in memory. Each subtitle goes to ffmpeg through
// its own pipe: pipe:3, pipe:4, … progress gets the count of written segments
// and the size of the last one. Mux validates the output.
func (p Pipeline) Mux(ctx context.Context, m hls.Media, subs []Subtitle, out string, progress func(done int, n int64)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, p.FFmpeg, muxArgs(subs, out)...)
	cmd.Stderr = &limitedWriter{buf: &stderr, max: 4096}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	var writers []*os.File
	defer func() {
		for _, w := range writers {
			w.Close()
		}
	}()
	for range subs {
		r, w, err := os.Pipe()
		if err != nil {
			return err
		}
		cmd.ExtraFiles = append(cmd.ExtraFiles, r)
		writers = append(writers, w)
	}
	err = cmd.Start()
	for _, r := range cmd.ExtraFiles {
		r.Close() // ffmpeg holds its own copies
	}
	if err != nil {
		return fmt.Errorf("ffmpeg: %w", err)
	}

	subErrs := make([]error, len(subs))
	var wg sync.WaitGroup
	for i, w := range writers {
		wg.Go(func() {
			if _, err := w.Write(subs[i].SRT); err != nil {
				subErrs[i] = fmt.Errorf("ffmpeg pipe:%d: %w", i+3, err)
			}
			w.Close()
		})
	}
	ledger, writeErr := p.feed(ctx, m, stdin, progress)
	stdin.Close()
	if writeErr != nil {
		cancel()
		cmd.Wait()
		wg.Wait()
		return writeErr
	}
	waitErr := cmd.Wait()
	wg.Wait()
	if waitErr != nil {
		return fmt.Errorf("ffmpeg: %v: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	if err := errors.Join(subErrs...); err != nil {
		return err
	}
	if ledger != len(m.Segments) {
		return fmt.Errorf("ledger: %d of %d segments written", ledger, len(m.Segments))
	}
	pr, err := p.Probe(ctx, out)
	if err != nil {
		return err
	}
	return pr.Validate(m.Duration(), len(subs))
}

// muxArgs maps the first video and audio track of pipe:0 and one SRT track
// per subtitle pipe, all stream copy.
func muxArgs(subs []Subtitle, out string) []string {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-f", "mpegts", "-i", "pipe:0"}
	for i := range subs {
		args = append(args, "-f", "srt", "-i", fmt.Sprintf("pipe:%d", i+3))
	}
	args = append(args, "-map", "0:v:0", "-map", "0:a:0")
	for i := range subs {
		args = append(args, "-map", fmt.Sprintf("%d:s:0", i+1))
	}
	args = append(args, "-c", "copy", "-metadata:s:a:0", "language=ukr")
	for i, s := range subs {
		if s.Language != "" {
			args = append(args, fmt.Sprintf("-metadata:s:s:%d", i), "language="+s.Language)
		}
		if s.Title != "" {
			args = append(args, fmt.Sprintf("-metadata:s:s:%d", i), "title="+s.Title)
		}
	}
	return append(args, "-f", "matroska", "-y", out)
}

type result struct {
	data []byte
	err  error
}

// feed downloads segments with bounded concurrency and writes them in order.
// At most Concurrency segments are in memory. It returns the number written.
func (p Pipeline) feed(ctx context.Context, m hls.Media, w interface{ Write([]byte) (int, error) }, progress func(int, int64)) (int, error) {
	n := max(p.Concurrency, 1)
	slots := make([]chan result, len(m.Segments))
	for i := range slots {
		slots[i] = make(chan result, 1)
	}
	sem := make(chan struct{}, n)
	go func() {
		for i, s := range m.Segments {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			go func() {
				b, err := p.segment(ctx, s.URL)
				slots[i] <- result{b, err}
			}()
		}
	}()
	for i := range slots {
		var r result
		select {
		case r = <-slots[i]:
		case <-ctx.Done():
			return i, ctx.Err()
		}
		if r.err != nil {
			return i, fmt.Errorf("segment %d: %w", i+1, r.err)
		}
		if _, err := w.Write(r.data); err != nil {
			return i, fmt.Errorf("ffmpeg stdin: %w", err)
		}
		<-sem // the segment leaves memory only after the write
		progress(i+1, int64(len(r.data)))
	}
	return len(slots), nil
}

// segment fetches one segment with retries.
func (p Pipeline) segment(ctx context.Context, u string) ([]byte, error) {
	attempts := max(p.Attempts, 1)
	wait := p.Backoff
	var err error
	for a := range attempts {
		if a > 0 {
			sleep(ctx, wait)
			wait *= 2
		}
		var b []byte
		if b, _, err = p.get(ctx, u); err == nil && len(b) > 0 {
			return b, nil
		}
		if err == nil {
			err = errors.New("empty body")
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("%d attempts: %w", attempts, err)
}

// Track is one stream of the output file.
type Track struct {
	Type     string // video, audio, subtitle
	Codec    string
	Language string
	Title    string
	Duration float64 // seconds
	Packets  int
}

// Probe is the ffprobe view of an output file.
type Probe struct {
	Duration float64
	Tracks   []Track
}

// Probe runs ffprobe with packet counts.
func (p Pipeline) Probe(ctx context.Context, file string) (Probe, error) {
	out, err := exec.CommandContext(ctx, p.FFprobe, "-v", "error", "-count_packets",
		"-show_entries", "format=duration:stream=codec_type,codec_name,nb_read_packets:stream_tags=language,title,DURATION",
		"-of", "json", file).Output()
	if err != nil {
		return Probe{}, fmt.Errorf("ffprobe: %w", err)
	}
	var raw struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType string            `json:"codec_type"`
			CodecName string            `json:"codec_name"`
			Packets   string            `json:"nb_read_packets"`
			Tags      map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return Probe{}, fmt.Errorf("ffprobe: %w", err)
	}
	var pr Probe
	pr.Duration, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	for _, s := range raw.Streams {
		n, _ := strconv.Atoi(s.Packets)
		pr.Tracks = append(pr.Tracks, Track{
			Type: s.CodecType, Codec: s.CodecName, Language: s.Tags["language"], Title: s.Tags["title"],
			Duration: clock(s.Tags["DURATION"]), Packets: n,
		})
	}
	return pr, nil
}

// clock parses an MKV DURATION tag: HH:MM:SS.nnnnnnnnn.
func clock(s string) float64 {
	p := strings.Split(s, ":")
	if len(p) != 3 {
		return 0
	}
	h, _ := strconv.Atoi(p[0])
	m, _ := strconv.Atoi(p[1])
	sec, _ := strconv.ParseFloat(p[2], 64)
	return float64(h*3600+m*60) + sec
}

// Validate checks 1 video and 1 audio track, each within DurationTolerance of
// want, and subs subtitle tracks.
func (pr Probe) Validate(want float64, subs int) error {
	count := map[string]int{}
	for _, t := range pr.Tracks {
		count[t.Type]++
		if t.Type != "video" && t.Type != "audio" {
			continue
		}
		if math.Abs(t.Duration-want) > DurationTolerance {
			return fmt.Errorf("validate: %s duration %.3fs, playlist %.3fs", t.Type, t.Duration, want)
		}
	}
	if count["video"] != 1 || count["audio"] != 1 {
		return fmt.Errorf("validate: %d video and %d audio tracks, want 1 and 1", count["video"], count["audio"])
	}
	if count["subtitle"] != subs {
		return fmt.Errorf("validate: %d subtitle tracks, want %d", count["subtitle"], subs)
	}
	return nil
}

// limitedWriter keeps the first max bytes, e.g. of ffmpeg stderr.
type limitedWriter struct {
	buf *bytes.Buffer
	max int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		l.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}
