package downloader

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// vttTime is a WebVTT timestamp: [hh:]mm:ss.ttt.
var vttTime = regexp.MustCompile(`^(?:(\d{2,}):)?([0-5]\d):([0-5]\d)\.(\d{3})$`)

// vttToSRT converts WebVTT to SubRip. It drops the header, NOTE, STYLE and
// REGION blocks, cue IDs, cue settings and cues without text, and renumbers
// cues from 1. Cue text is copied as is: UAKino cues are plain.
func vttToSRT(vtt []byte) ([]byte, error) {
	s := strings.TrimPrefix(string(vtt), "\ufeff")
	s = strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(s)
	blocks := strings.Split(s, "\n\n")
	if h := blocks[0]; h != "WEBVTT" && !strings.HasPrefix(h, "WEBVTT ") && !strings.HasPrefix(h, "WEBVTT\t") && !strings.HasPrefix(h, "WEBVTT\n") {
		return nil, errors.New("not WebVTT")
	}
	var out bytes.Buffer
	n := 0
	for _, b := range blocks[1:] {
		lines := strings.Split(strings.Trim(b, "\n"), "\n")
		i := 0
		for i < len(lines) && !strings.Contains(lines[i], "-->") {
			i++
		}
		if i == len(lines) {
			continue // NOTE, STYLE, REGION or an empty block
		}
		start, end, err := vttTiming(lines[i])
		if err != nil {
			return nil, err
		}
		text := lines[i+1:]
		if len(text) == 0 {
			continue
		}
		n++
		fmt.Fprintf(&out, "%d\n%s --> %s\n%s\n\n", n, start, end, strings.Join(text, "\n"))
	}
	return out.Bytes(), nil
}

// vttTiming parses "start --> end [settings]" into SRT timestamps.
func vttTiming(line string) (string, string, error) {
	f := strings.Fields(line)
	if len(f) < 3 || f[1] != "-->" {
		return "", "", fmt.Errorf("cue timing %q", line)
	}
	start, ok1 := srtTime(f[0])
	end, ok2 := srtTime(f[2])
	if !ok1 || !ok2 {
		return "", "", fmt.Errorf("cue timing %q", line)
	}
	return start, end, nil
}

// srtTime turns a WebVTT timestamp into HH:MM:SS,mmm.
func srtTime(t string) (string, bool) {
	m := vttTime.FindStringSubmatch(t)
	if m == nil {
		return "", false
	}
	h := m[1]
	if h == "" {
		h = "00"
	}
	return h + ":" + m[2] + ":" + m[3] + "," + m[4], true
}
