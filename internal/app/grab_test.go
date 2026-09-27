package app_test

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/vmorshc/hls-indexer/internal/testenv"
)

const nzbFor = `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"><file subject="hls-indexer:%s"/></nzb>`

// addfile posts an NZB like Sonarr does: multipart field "name", file name = release title + .nzb.
func addfile(t *testing.T, e *testenv.Env, nzb []byte, filename, cat string) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("name", filename)
	fw.Write(nzb)
	mw.Close()
	u := e.API.URL + "/downloader/api?mode=addfile&output=json&priority=-100&cat=" + cat + "&apikey=" + testenv.DownloaderKey
	resp, err := http.Post(u, mw.FormDataContentType(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("addfile %d: %s", resp.StatusCode, b)
	}
	return out
}

func sab(t *testing.T, e *testenv.Env, query string) map[string]any {
	t.Helper()
	_, body := e.Get(t, "/downloader/api?output=json&apikey="+testenv.DownloaderKey+"&"+query)
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("%s: %s", query, body)
	}
	return out
}

func slots(t *testing.T, e *testenv.Env, mode, query string) []map[string]any {
	t.Helper()
	res := sab(t, e, "mode="+mode+"&"+query)
	raw, ok := res[mode].(map[string]any)["slots"].([]any)
	if !ok {
		t.Fatalf("%s: slots not a list: %v", mode, res)
	}
	out := make([]map[string]any, len(raw))
	for i, s := range raw {
		out[i] = s.(map[string]any)
	}
	return out
}

func TestAddfileRejectsForeignNZB(t *testing.T) {
	e := testenv.Start(t, testenv.Options{Redis: true})
	tests := map[string]string{
		"unknown release": `<nzb><file subject="hls-indexer:uakino:312-shrek-2:movie:xyz"/></nzb>`,
		"unknown source":  `<nzb><file subject="hls-indexer:other:312-shrek-2:movie:ab12cd34"/></nzb>`,
		"usenet subject":  `<nzb><file subject="some.release.part01.rar"/></nzb>`,
		"two files":       `<nzb><file subject="hls-indexer:uakino:312-shrek-2:movie:ab12cd34"/><file subject="hls-indexer:uakino:312-shrek-2:movie:ab12cd34"/></nzb>`,
		"dtd": `<!DOCTYPE nzb PUBLIC "-//newzBin//DTD NZB 1.1//EN" "http://www.newzbin.com/DTD/nzb/nzb-1.1.dtd">
<nzb><file subject="hls-indexer:uakino:312-shrek-2:movie:ab12cd34"/></nzb>`,
		"external entity": `<!DOCTYPE nzb [<!ENTITY x SYSTEM "file:///etc/passwd">]>
<nzb><file subject="hls-indexer:uakino:312-shrek-2:movie:&x;"/></nzb>`,
		"not xml": `hello`,
	}
	for name, nzb := range tests {
		t.Run(name, func(t *testing.T) {
			got := addfile(t, e, []byte(nzb), "x.nzb", "radarr")
			if got["status"] != false || got["error"] != "Unknown release" {
				t.Errorf("got %v", got)
			}
		})
	}
	if s := slots(t, e, "queue", ""); len(s) != 0 {
		t.Errorf("queue %v", s)
	}
}

func TestQueueWithoutWorker(t *testing.T) {
	e := testenv.Start(t, testenv.Options{Redis: true})
	nzb := func(id string) []byte { return []byte(fmtNZB(id)) }
	a := addfile(t, e, nzb("uakino:312-shrek-2:movie:ab12cd34"), "Shrek.2.2004.1080p.WEB-DL.UKR-CinePlus.nzb", "radarr")
	again := addfile(t, e, nzb("uakino:312-shrek-2:movie:ab12cd34"), "Shrek.2.2004.1080p.WEB-DL.UKR-CinePlus.nzb", "radarr")
	b := addfile(t, e, nzb("uakino:13059-eyforya-2-sezon:s02e03:0a3a8c0e"), "Euphoria.S02E03.nzb", "sonarr")
	idA := a["nzo_ids"].([]any)[0].(string)
	if a["status"] != true || again["nzo_ids"].([]any)[0] != idA || b["nzo_ids"].([]any)[0] == idA {
		t.Fatalf("addfile %v %v %v", a, again, b)
	}
	all := slots(t, e, "queue", "limit=0")
	if len(all) != 2 {
		t.Fatalf("queue %v", all)
	}
	radarr := slots(t, e, "queue", "category=radarr")
	if len(radarr) != 1 {
		t.Fatalf("radarr queue %v", radarr)
	}
	s := radarr[0]
	if s["nzo_id"] != idA || s["cat"] != "radarr" || s["status"] != "Queued" ||
		s["filename"] != "Shrek.2.2004.1080p.WEB-DL.UKR-CinePlus" || s["priority"] != "Normal" || s["timeleft"] != "0:00:00" {
		t.Errorf("slot %v", s)
	}
	if h := slots(t, e, "history", ""); len(h) != 0 {
		t.Errorf("history %v", h)
	}
}

func fmtNZB(id string) string {
	return string(bytes.Replace([]byte(nzbFor), []byte("%s"), []byte(id), 1))
}

// S1: search → t=get → addfile → Completed → MKV in downloads/<jobId>, for an
// episode and a movie.
func TestGrabPublishesMKV(t *testing.T) {
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not in PATH", bin)
		}
	}
	ua := &testenv.FakeUAKino{Segments: true, Hide: []string{"13405-shrek-privid-lorda-farkuada"}}
	e := startSearch(t, testenv.Options{UAKino: ua, Worker: true})
	tests := []struct {
		name, query, cat string
	}{
		{"episode", "t=tvsearch&tvdbid=" + strconv.Itoa(testenv.EuphoriaTVDB) + "&season=2&ep=3", "sonarr"},
		{"movie", "t=movie&tmdbid=" + strconv.Itoa(testenv.ShrekTMDb), "radarr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := e.search(t, tt.query).Channel.Items[0]
			resp, err := http.Get(it.Enc.URL)
			if err != nil {
				t.Fatal(err)
			}
			nzb, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			start := time.Now()
			res := addfile(t, e.Env, nzb, it.Title+".nzb", tt.cat)
			if time.Since(start) > 2*time.Second || res["status"] != true {
				t.Fatalf("addfile %v in %s", res, time.Since(start))
			}
			id := res["nzo_ids"].([]any)[0].(string)

			var slot map[string]any
			for deadline := time.Now().Add(30 * time.Second); slot == nil; {
				for _, s := range slots(t, e.Env, "history", "category="+tt.cat) {
					if s["nzo_id"] == id {
						slot = s
					}
				}
				if time.Now().After(deadline) {
					t.Fatalf("job %s not in history, queue %v", id, slots(t, e.Env, "queue", ""))
				}
				time.Sleep(100 * time.Millisecond)
			}
			dir := filepath.Join(e.Config.Paths.Downloads, id)
			if slot["status"] != "Completed" || slot["storage"] != dir || slot["category"] != tt.cat || slot["name"] != it.Title {
				t.Fatalf("history %v", slot)
			}
			fi, err := os.Stat(filepath.Join(dir, it.Title+".mkv"))
			if err != nil {
				t.Fatal(err)
			}
			if slot["bytes"] != float64(fi.Size()) {
				t.Errorf("bytes %v, file %d", slot["bytes"], fi.Size())
			}
			if _, err := os.Stat(filepath.Join(e.Config.Paths.Incomplete, id)); !os.IsNotExist(err) {
				t.Errorf("staging left: %v", err)
			}
			if q := slots(t, e.Env, "queue", ""); len(q) != 0 {
				t.Errorf("queue %v", q)
			}
		})
	}
}
