package downloader

import "testing"

func TestVTTToSRT(t *testing.T) {
	tests := []struct {
		name, vtt, srt string
	}{
		{"minutes only",
			"WEBVTT\n\n00:52.970 --> 00:57.391\nOnce upon a time\nin a kingdom\n\n01:01.979 --> 01:05.148\nAnd throughout\n",
			"1\n00:00:52,970 --> 00:00:57,391\nOnce upon a time\nin a kingdom\n\n2\n00:01:01,979 --> 00:01:05,148\nAnd throughout\n\n"},
		{"hours, cue id, settings",
			"WEBVTT - title\n\nintro\n01:02:03.004 --> 01:02:05.000 align:start line:0\nПривіт\n",
			"1\n01:02:03,004 --> 01:02:05,000\nПривіт\n\n"},
		{"BOM, CRLF, NOTE and STYLE blocks",
			"\ufeffWEBVTT\r\n\r\nNOTE a comment\r\n\r\nSTYLE\r\n::cue { color: red }\r\n\r\n00:01.000 --> 00:02.000\r\nText\r\n",
			"1\n00:00:01,000 --> 00:00:02,000\nText\n\n"},
		{"cue without text is dropped",
			"WEBVTT\n\n00:01.000 --> 00:02.000\n\n00:03.000 --> 00:04.000\nB\n",
			"1\n00:00:03,000 --> 00:00:04,000\nB\n\n"},
		{"no cues", "WEBVTT\n", ""},
	}
	for _, tt := range tests {
		got, err := vttToSRT([]byte(tt.vtt))
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if string(got) != tt.srt {
			t.Errorf("%s:\ngot  %q\nwant %q", tt.name, got, tt.srt)
		}
	}
}

func TestVTTToSRTRejects(t *testing.T) {
	for name, vtt := range map[string]string{
		"not WebVTT": "<html>error</html>",
		"bad timing": "WEBVTT\n\n1:2 --> 00:02.000\nText\n",
	} {
		if _, err := vttToSRT([]byte(vtt)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}
