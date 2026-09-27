package app_test

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/vmorshc/hls-indexer/internal/config"
	"github.com/vmorshc/hls-indexer/internal/hls"
	"github.com/vmorshc/hls-indexer/internal/jobs"
	"github.com/vmorshc/hls-indexer/internal/testenv"
)

// gate holds requests for one fixture segment until opened.
type gate struct {
	file string
	ch   chan struct{}
	once sync.Once
}

func newGate(file string) *gate { return &gate{file: file, ch: make(chan struct{})} }

func (g *gate) open() { g.once.Do(func() { close(g.ch) }) }

func (g *gate) intercept(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/content/stream/"+g.file {
		return false
	}
	select {
	case <-g.ch:
		return false
	case <-r.Context().Done():
		return true
	}
}

type grabbable struct {
	title, cat string
	nzb        []byte
}

// queueEnv starts the API with a fake UAKino whose segment behind g waits for
// g.open, and returns Euphoria S2 episode releases to grab.
func queueEnv(t *testing.T, g *gate, o testenv.Options) (searchEnv, []grabbable) {
	t.Helper()
	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not in PATH", bin)
		}
	}
	ua := &testenv.FakeUAKino{Segments: true, Intercept: g.intercept}
	o.UAKino = ua
	e := startSearch(t, o)
	t.Cleanup(g.open)
	f := e.search(t, "t=tvsearch&tvdbid="+strconv.Itoa(testenv.EuphoriaTVDB)+"&season=2")
	var rels []grabbable
	for _, it := range f.Channel.Items {
		resp, err := http.Get(it.Enc.URL)
		if err != nil {
			t.Fatal(err)
		}
		nzb, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		rels = append(rels, grabbable{it.Title, "sonarr", nzb})
	}
	if len(rels) < 7 {
		t.Fatalf("need 7 releases, got %d", len(rels))
	}
	return e, rels
}

func grab(t *testing.T, e *testenv.Env, r grabbable, priority int) string {
	t.Helper()
	res := addfileP(t, e, r.nzb, r.title+".nzb", r.cat, priority)
	if res["status"] != true {
		t.Fatalf("addfile %v", res)
	}
	return res["nzo_ids"].([]any)[0].(string)
}

// queueSlot returns the queue slot of job id or nil.
func queueSlot(t *testing.T, e *testenv.Env, id string) map[string]any {
	t.Helper()
	for _, s := range slots(t, e, "queue", "limit=0") {
		if s["nzo_id"] == id {
			return s
		}
	}
	return nil
}

// waitSlot polls the queue until ok accepts the slot of job id.
func waitSlot(t *testing.T, e *testenv.Env, id string, ok func(map[string]any) bool) map[string]any {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); ; {
		if s := queueSlot(t, e, id); s != nil && ok(s) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s: queue %v", id, slots(t, e, "queue", "limit=0"))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func hasStatus(status string) func(map[string]any) bool {
	return func(s map[string]any) bool { return s["status"] == status }
}

func countStatus(t *testing.T, e *testenv.Env, status string) int {
	n := 0
	for _, s := range slots(t, e, "queue", "limit=0") {
		if s["status"] == status {
			n++
		}
	}
	return n
}

// S1: priorities are stored and shown, the worker claims by priority then age,
// -2 adds a paused job that runs only after resume, and worker.jobs=1 runs one
// job at a time.
func TestPriorityOrderAndPausedPriority(t *testing.T) {
	g := newGate("segment1.ts")
	e, rels := queueEnv(t, g, testenv.Options{Worker: true})
	blocker := grab(t, e.Env, rels[0], 0)
	waitSlot(t, e.Env, blocker, hasStatus("Downloading"))

	add := []struct {
		priority int
		label    string
	}{{-1, "Low"}, {0, "Normal"}, {1, "High"}, {2, "Force"}, {-100, "Normal"}, {-2, "Paused"}}
	ids := make([]string, len(add))
	for i, a := range add {
		ids[i] = grab(t, e.Env, rels[i+1], a.priority)
		time.Sleep(5 * time.Millisecond)
	}
	paused := ids[5]
	if again := grab(t, e.Env, rels[6], 0); again != paused {
		t.Errorf("paused release not deduped: %s", again)
	}
	time.Sleep(300 * time.Millisecond)
	for i, a := range add {
		s := queueSlot(t, e.Env, ids[i])
		want := "Queued"
		if a.priority == -2 {
			want = "Paused"
		}
		if s["priority"] != a.label || s["status"] != want {
			t.Errorf("priority %d: slot %v", a.priority, s)
		}
	}
	if n := countStatus(t, e.Env, "Downloading"); n != 1 {
		t.Errorf("%d jobs Downloading, want 1", n)
	}

	g.open()
	want := []string{blocker, ids[3], ids[2], ids[1], ids[4], ids[0]}
	for _, id := range want {
		waitHistory(t, e.Env, id)
	}
	var got []string
	for _, s := range slots(t, e.Env, "history", "") {
		got = append([]string{s["nzo_id"].(string)}, got...)
	}
	eq(t, "claim order", got, want)
	time.Sleep(300 * time.Millisecond)
	if s := queueSlot(t, e.Env, paused); s["status"] != "Paused" {
		t.Fatalf("paused job ran: %v", s)
	}

	res := sab(t, e.Env, "mode=queue&name=resume&value="+paused)
	if res["status"] != true {
		t.Fatalf("resume %v", res)
	}
	if s := waitHistory(t, e.Env, paused); s["status"] != "Completed" {
		t.Fatalf("resumed job %v", s)
	}
}

// S1: worker.jobs limits how many jobs run at once.
func TestWorkerJobsLimit(t *testing.T) {
	g := newGate("segment1.ts")
	e, rels := queueEnv(t, g, testenv.Options{Worker: true, Configure: func(c *config.Config) { c.Worker.Jobs = 2 }})
	var ids []string
	for _, r := range rels[:3] {
		ids = append(ids, grab(t, e.Env, r, 0))
	}
	for deadline := time.Now().Add(10 * time.Second); countStatus(t, e.Env, "Downloading") < 2; {
		if time.Now().After(deadline) {
			t.Fatalf("queue %v", slots(t, e.Env, "queue", ""))
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if d, q := countStatus(t, e.Env, "Downloading"), countStatus(t, e.Env, "Queued"); d != 2 || q != 1 {
		t.Fatalf("%d Downloading, %d Queued: %v", d, q, slots(t, e.Env, "queue", ""))
	}
	g.open()
	for _, id := range ids {
		if s := waitHistory(t, e.Env, id); s["status"] != "Completed" {
			t.Errorf("history %v", s)
		}
	}
}

var clock = regexp.MustCompile(`^\d+:\d\d:\d\d$`)

// S1: queue progress comes from written segments and bytes. Pausing a running
// job stops it with clean staging and frees the worker. Resume runs it again
// from zero. history reports bytes and download_time.
func TestProgressPauseResume(t *testing.T) {
	g := newGate("segment3.ts")
	e, rels := queueEnv(t, g, testenv.Options{Worker: true})
	a := grab(t, e.Env, rels[0], 0)

	s := waitSlot(t, e.Env, a, func(s map[string]any) bool { return s["percentage"] == "66" && s["timeleft"] != "0:00:00" })
	written := len(segmentFixture(t, "segment1.ts")) + len(segmentFixture(t, "segment2.ts"))
	// Size estimate: best variant BANDWIDTH × total EXTINF / 8.
	vs, err := hls.ParseMaster(string(segmentFixture(t, "master-83766.m3u8")), &url.URL{})
	if err != nil {
		t.Fatal(err)
	}
	size := float64(hls.Best(vs).Bandwidth) * (6.17 + 5.00 + 4.14) / 8
	wantMB := fmt.Sprintf("%.2f", size/(1<<20))
	wantLeft := fmt.Sprintf("%.2f", math.Max(size-float64(written), 0)/(1<<20))
	if s["mb"] != wantMB || s["mbleft"] != wantLeft {
		t.Errorf("mb %v mbleft %v, want %s %s", s["mb"], s["mbleft"], wantMB, wantLeft)
	}
	if !clock.MatchString(s["timeleft"].(string)) || s["status"] != "Downloading" {
		t.Errorf("slot %v", s)
	}

	res := sab(t, e.Env, "mode=queue&name=pause&value="+a)
	if res["status"] != true {
		t.Fatalf("pause %v", res)
	}
	if s := queueSlot(t, e.Env, a); s["status"] != "Paused" {
		t.Fatalf("slot %v", s)
	}
	stage := filepath.Join(e.Config.Paths.Incomplete, a)
	for deadline := time.Now().Add(10 * time.Second); ; {
		if _, err := os.Stat(stage); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("staging kept after pause")
		}
		time.Sleep(50 * time.Millisecond)
	}
	s = waitSlot(t, e.Env, a, func(s map[string]any) bool { return s["percentage"] == "0" })
	if s["status"] != "Paused" {
		t.Fatalf("slot %v", s)
	}

	b := grab(t, e.Env, rels[1], 0)
	waitSlot(t, e.Env, b, hasStatus("Downloading"))
	time.Sleep(1200 * time.Millisecond)
	g.open()
	waitHistory(t, e.Env, b)
	if s := queueSlot(t, e.Env, a); s["status"] != "Paused" {
		t.Fatalf("paused job ran: %v", s)
	}

	sab(t, e.Env, "mode=queue&name=resume&value="+a)
	h := waitHistory(t, e.Env, a)
	fi, err := os.Stat(filepath.Join(e.Config.Paths.Downloads, a, rels[0].title+".mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if h["status"] != "Completed" || h["bytes"] != float64(fi.Size()) {
		t.Errorf("history %v, file %d bytes", h, fi.Size())
	}
	hb := waitHistory(t, e.Env, b)
	if dl, _ := hb["download_time"].(float64); dl < 1 {
		t.Errorf("download_time %v, job waited at least 1 s", hb["download_time"])
	}
}

func TestPauseResumeUnknownJob(t *testing.T) {
	e := testenv.Start(t, testenv.Options{Redis: true})
	for _, name := range []string{"pause", "resume"} {
		got := sab(t, e, "mode=queue&name="+name+"&value=hls_missing")
		if got["status"] != false || got["error"] != "Unknown job" {
			t.Errorf("%s: got %v", name, got)
		}
	}
}

// S1: a worker restart re-queues a Downloading job. It starts from zero with
// clean staging and completes.
func TestRestartRequeuesDownloadingJob(t *testing.T) {
	g := newGate("none")
	g.open()
	e, rels := queueEnv(t, g, testenv.Options{Redis: true})
	id := grab(t, e.Env, rels[0], 0)

	// A worker crashed mid-download: job Downloading, progress and staging left.
	store := jobs.New(e.Redis)
	j, ok, err := store.Claim(context.Background(), time.Second)
	if err != nil || !ok || j.ID != id {
		t.Fatalf("claim %+v ok=%v err=%v", j, ok, err)
	}
	store.Start(context.Background(), j, 3, 1<<20)
	store.Progress(context.Background(), j, 2, 1000)
	stage := filepath.Join(e.Config.Paths.Incomplete, id)
	os.MkdirAll(stage, 0o755)
	os.WriteFile(filepath.Join(stage, "leftover.part"), []byte("x"), 0o644)
	if s := queueSlot(t, e.Env, id); s["status"] != "Downloading" || s["percentage"] != "66" {
		t.Fatalf("slot %v", s)
	}

	e.StartWorker(t)
	if s := waitHistory(t, e.Env, id); s["status"] != "Completed" {
		t.Fatalf("history %v", s)
	}
	entries, _ := os.ReadDir(filepath.Join(e.Config.Paths.Downloads, id))
	if len(entries) != 1 || entries[0].Name() != rels[0].title+".mkv" {
		t.Errorf("published %v", entries)
	}
}
