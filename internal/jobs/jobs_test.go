package jobs_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vmorshc/hls-indexer/internal/jobs"
	"github.com/vmorshc/hls-indexer/internal/testenv"
)

func claim(t *testing.T, s *jobs.Store) jobs.Job {
	t.Helper()
	j, ok, err := s.Claim(context.Background(), time.Second)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	return j
}

func complete(t *testing.T, s *jobs.Store, j jobs.Job, bytes int64) {
	t.Helper()
	if ok, err := s.Complete(context.Background(), j, "/d/"+j.ID, bytes); err != nil || !ok {
		t.Fatalf("complete %s: ok=%v err=%v", j.ID, ok, err)
	}
}

func status(t *testing.T, s *jobs.Store, id string) jobs.Job {
	t.Helper()
	j, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func noClaim(t *testing.T, s *jobs.Store) {
	t.Helper()
	if j, ok, err := s.Claim(context.Background(), time.Second); ok || err != nil {
		t.Fatalf("claimed %+v err=%v, want none", j, err)
	}
}

func TestPriorityIsStored(t *testing.T) {
	ctx := context.Background()
	s := jobs.New(testenv.Redis(t))
	for i, p := range []int{jobs.PriorityPaused, jobs.PriorityLow, jobs.PriorityNormal, jobs.PriorityHigh, jobs.PriorityForce} {
		id, _ := s.Add(ctx, "r"+string(rune('a'+i)), "t", "sonarr", p)
		if j := status(t, s, id); j.Priority != p {
			t.Errorf("priority %d stored as %d", p, j.Priority)
		}
	}
	id, _ := s.Add(ctx, "rz", "t", "sonarr", jobs.PriorityDefault)
	if j := status(t, s, id); j.Priority != jobs.PriorityNormal {
		t.Errorf("default stored as %d", j.Priority)
	}
}

func TestPausedPriorityWaitsForResume(t *testing.T) {
	ctx := context.Background()
	s := jobs.New(testenv.Redis(t))
	id, _ := s.Add(ctx, "r1", "t", "sonarr", jobs.PriorityPaused)
	if j := status(t, s, id); j.Status != jobs.Paused {
		t.Fatalf("status %s", j.Status)
	}
	noClaim(t, s)
	if again, _ := s.Add(ctx, "r1", "t", "sonarr", jobs.PriorityNormal); again != id {
		t.Fatalf("paused job not deduped: %s", again)
	}
	if err := s.Resume(ctx, id); err != nil {
		t.Fatal(err)
	}
	if j := claim(t, s); j.ID != id || j.Priority != jobs.PriorityNormal {
		t.Fatalf("claimed %+v", j)
	}
}

func TestPauseQueuedJob(t *testing.T) {
	ctx := context.Background()
	s := jobs.New(testenv.Redis(t))
	a, _ := s.Add(ctx, "r1", "t", "sonarr", jobs.PriorityHigh)
	b, _ := s.Add(ctx, "r2", "t", "sonarr", jobs.PriorityNormal)
	if err := s.Pause(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.Pause(ctx, a); err != nil {
		t.Fatalf("pause twice: %v", err)
	}
	if q, _ := s.Queue(ctx, ""); len(q) != 2 || q[0].Status != jobs.Paused {
		t.Fatalf("queue %+v", q)
	}
	if j := claim(t, s); j.ID != b {
		t.Fatalf("claimed %s, want %s", j.ID, b)
	}
	s.Resume(ctx, a)
	if err := s.Resume(ctx, a); err != nil {
		t.Fatalf("resume twice: %v", err)
	}
	if j := claim(t, s); j.ID != a || j.Priority != jobs.PriorityHigh {
		t.Fatalf("claimed %+v", j)
	}
}

// A paused running job belongs to the API: the old run cannot finish it or
// write progress, even after a resume and a new claim.
func TestPauseRunningJobStopsItsRun(t *testing.T) {
	ctx := context.Background()
	s := jobs.New(testenv.Redis(t))
	id, _ := s.Add(ctx, "r1", "t", "sonarr", jobs.PriorityNormal)
	old := claim(t, s)
	s.Start(ctx, old, 10, 1000)
	if err := s.Pause(ctx, id); err != nil {
		t.Fatal(err)
	}
	if j := status(t, s, id); j.Status != jobs.Paused || j.SegmentsTotal != 10 {
		t.Fatalf("job %+v", j)
	}
	if ok, err := s.Complete(ctx, old, "/d", 1); ok || err != nil {
		t.Fatalf("complete paused: ok=%v err=%v", ok, err)
	}
	if ok, _ := s.Fail(ctx, old, "boom"); ok {
		t.Fatal("failed paused job")
	}
	s.Resume(ctx, id)
	cur := claim(t, s)
	if cur.Run == old.Run {
		t.Fatalf("same run %d", cur.Run)
	}
	s.Progress(ctx, old, 5, 500)
	if j := status(t, s, id); j.SegmentsDone != 0 || j.Status != jobs.Downloading {
		t.Fatalf("stale progress written: %+v", j)
	}
	s.Progress(ctx, cur, 2, 200)
	if j := status(t, s, id); j.SegmentsDone != 2 || j.BytesDone != 200 {
		t.Fatalf("progress %+v", j)
	}
	complete(t, s, cur, 1)
}

func TestPauseResumeNeedActiveJob(t *testing.T) {
	ctx := context.Background()
	s := jobs.New(testenv.Redis(t))
	s.Add(ctx, "r1", "t", "sonarr", jobs.PriorityNormal)
	done := claim(t, s)
	complete(t, s, done, 1)
	for _, id := range []string{done.ID, "hls_missing"} {
		if err := s.Pause(ctx, id); err != jobs.ErrNotFound {
			t.Errorf("pause %s: %v", id, err)
		}
		if err := s.Resume(ctx, id); err != jobs.ErrNotFound {
			t.Errorf("resume %s: %v", id, err)
		}
	}
}

func TestRequeueKeepsPausedJobs(t *testing.T) {
	ctx := context.Background()
	s := jobs.New(testenv.Redis(t))
	id, _ := s.Add(ctx, "r1", "t", "sonarr", jobs.PriorityNormal)
	claim(t, s)
	s.Pause(ctx, id)
	if err := s.Requeue(ctx); err != nil {
		t.Fatal(err)
	}
	noClaim(t, s)
	if j := status(t, s, id); j.Status != jobs.Paused {
		t.Fatalf("status %s", j.Status)
	}
}

func TestAddDedupsActiveReleasePerCategory(t *testing.T) {
	ctx := context.Background()
	s := jobs.New(testenv.Redis(t))
	a, err := s.Add(ctx, "uakino:1-a:movie:00000000", "A", "radarr", jobs.PriorityDefault)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.Add(ctx, "uakino:1-a:movie:00000000", "A", "radarr", jobs.PriorityDefault)
	c, _ := s.Add(ctx, "uakino:1-a:movie:00000000", "A", "sonarr", jobs.PriorityDefault)
	if a != b || a == c || !strings.HasPrefix(a, "hls_") {
		t.Fatalf("ids %s %s %s", a, b, c)
	}
	complete(t, s, claim(t, s), 1)
	if d, _ := s.Add(ctx, "uakino:1-a:movie:00000000", "A", "radarr", jobs.PriorityDefault); d == a {
		t.Fatal("finished job reused")
	}
}

func TestClaimOrdersByPriorityThenAge(t *testing.T) {
	ctx := context.Background()
	s := jobs.New(testenv.Redis(t))
	low, _ := s.Add(ctx, "r1", "t", "sonarr", jobs.PriorityLow)
	n1, _ := s.Add(ctx, "r2", "t", "sonarr", jobs.PriorityDefault)
	time.Sleep(2 * time.Millisecond)
	n2, _ := s.Add(ctx, "r3", "t", "sonarr", jobs.PriorityNormal)
	high, _ := s.Add(ctx, "r4", "t", "sonarr", jobs.PriorityHigh)
	for _, want := range []string{high, n1, n2, low} {
		if j := claim(t, s); j.ID != want || j.Status != jobs.Downloading {
			t.Fatalf("claimed %s %s, want %s", j.ID, j.Status, want)
		}
	}
	if _, ok, _ := s.Claim(ctx, time.Second); ok {
		t.Fatal("queue should be empty")
	}
}

func TestRequeueRestartsDownloadingJobs(t *testing.T) {
	ctx := context.Background()
	s := jobs.New(testenv.Redis(t))
	id, _ := s.Add(ctx, "r1", "t", "sonarr", jobs.PriorityNormal)
	s.Progress(ctx, claim(t, s), 3, 100)
	if err := s.Requeue(ctx); err != nil {
		t.Fatal(err)
	}
	j := claim(t, s)
	if j.ID != id || j.SegmentsDone != 0 {
		t.Fatalf("got %+v", j)
	}
}

func TestRequeueRestoresJobDroppedBetweenPopAndClaim(t *testing.T) {
	ctx := context.Background()
	r := testenv.Redis(t)
	s := jobs.New(r)
	id, _ := s.Add(ctx, "r1", "t", "sonarr", jobs.PriorityNormal)
	r.ZPopMin(ctx, jobs.Prefix+"queue") // crash after the pop, before the status write
	if err := s.Requeue(ctx); err != nil {
		t.Fatal(err)
	}
	if j := claim(t, s); j.ID != id {
		t.Fatalf("claimed %s", j.ID)
	}
}

func TestQueueAndHistory(t *testing.T) {
	ctx := context.Background()
	s := jobs.New(testenv.Redis(t))
	var ids []string
	for i, cat := range []string{"sonarr", "radarr", "sonarr", "sonarr"} {
		time.Sleep(2 * time.Millisecond)
		id, _ := s.Add(ctx, "r"+string(rune('a'+i)), "t", cat, jobs.PriorityNormal)
		ids = append(ids, id)
	}
	if q, _ := s.Queue(ctx, "radarr"); len(q) != 1 || q[0].ID != ids[1] {
		t.Fatalf("queue radarr %+v", q)
	}
	for i := range 3 {
		time.Sleep(2 * time.Millisecond)
		j := claim(t, s)
		if j.ID != ids[i] {
			t.Fatalf("claimed %s, want %s", j.ID, ids[i])
		}
		if i == 1 {
			s.Fail(ctx, j, "boom")
		} else {
			s.Complete(ctx, j, "/d/"+j.ID, 5)
		}
	}
	if q, _ := s.Queue(ctx, ""); len(q) != 1 || q[0].ID != ids[3] || q[0].Status != jobs.Queued {
		t.Fatalf("queue %+v", q)
	}
	h, total, err := s.History(ctx, "sonarr", "", 0, 1)
	if err != nil || total != 2 || len(h) != 1 || h[0].ID != ids[2] || h[0].Storage != "/d/"+ids[2] {
		t.Fatalf("history page 1: %+v total=%d err=%v", h, total, err)
	}
	h, _, _ = s.History(ctx, "sonarr", "", 1, 1)
	if len(h) != 1 || h[0].ID != ids[0] {
		t.Fatalf("history page 2: %+v", h)
	}
	h, total, _ = s.History(ctx, "", "failed", 0, 0)
	if total != 1 || h[0].Status != jobs.Failed || h[0].FailMessage != "boom" {
		t.Fatalf("history radarr %+v", h)
	}
	if h, _, _ := s.History(ctx, "none", "", 0, 0); h == nil || len(h) != 0 {
		t.Fatalf("empty history %#v", h)
	}
}

func TestKeysUsePrefix(t *testing.T) {
	ctx := context.Background()
	r := testenv.Redis(t)
	s := jobs.New(r)
	s.Add(ctx, "r1", "t", "sonarr", jobs.PriorityNormal)
	complete(t, s, claim(t, s), 1)
	keys, _ := r.Keys(ctx, "*").Result()
	for _, k := range keys {
		if !strings.HasPrefix(k, jobs.Prefix) {
			t.Errorf("key %s", k)
		}
	}
}

func TestRetryReplacesFailedJob(t *testing.T) {
	ctx := context.Background()
	s := jobs.New(testenv.Redis(t))
	old, _ := s.Add(ctx, "r1", "Title", "sonarr", jobs.PriorityHigh)
	s.Fail(ctx, claim(t, s), "boom")
	id, err := s.Retry(ctx, old)
	if err != nil || id == old {
		t.Fatalf("retry %s %v", id, err)
	}
	if h, total, _ := s.History(ctx, "", "", 0, 0); total != 0 {
		t.Fatalf("history %+v", h)
	}
	j := claim(t, s)
	if j.ID != id || j.Release != "r1" || j.Title != "Title" || j.Category != "sonarr" || j.Priority != jobs.PriorityHigh {
		t.Fatalf("new job %+v", j)
	}
	if _, err := s.Get(ctx, old); err != jobs.ErrNotFound {
		t.Fatalf("old job: %v", err)
	}
}

func TestRetryReturnsActiveJobOfSameRelease(t *testing.T) {
	ctx := context.Background()
	s := jobs.New(testenv.Redis(t))
	failed, _ := s.Add(ctx, "r1", "t", "sonarr", jobs.PriorityNormal)
	s.Fail(ctx, claim(t, s), "boom")
	active, _ := s.Add(ctx, "r1", "t", "sonarr", jobs.PriorityNormal)
	if id, err := s.Retry(ctx, failed); err != nil || id != active {
		t.Fatalf("retry %s %v, want %s", id, err, active)
	}
	if q, _ := s.Queue(ctx, ""); len(q) != 1 {
		t.Fatalf("queue %+v", q)
	}
}

func TestRetryNeedsFailedJob(t *testing.T) {
	ctx := context.Background()
	s := jobs.New(testenv.Redis(t))
	done, _ := s.Add(ctx, "r2", "t", "sonarr", jobs.PriorityNormal)
	complete(t, s, claim(t, s), 1)
	queued, _ := s.Add(ctx, "r1", "t", "sonarr", jobs.PriorityNormal)
	for _, id := range []string{queued, done, "hls_missing"} {
		if _, err := s.Retry(ctx, id); err != jobs.ErrNotFound {
			t.Errorf("%s: err %v", id, err)
		}
	}
}
