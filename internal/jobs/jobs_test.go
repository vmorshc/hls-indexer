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
	if err := s.Complete(ctx, a, "/data/downloads/"+a, 1); err != nil {
		t.Fatal(err)
	}
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
	claim(t, s)
	s.Progress(ctx, id, 3, 100)
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
		id, _ := s.Add(ctx, "r"+string(rune('a'+i)), "t", cat, jobs.PriorityNormal)
		ids = append(ids, id)
	}
	if q, _ := s.Queue(ctx, "radarr"); len(q) != 1 || q[0].ID != ids[1] {
		t.Fatalf("queue radarr %+v", q)
	}
	for i, id := range ids[:3] {
		time.Sleep(2 * time.Millisecond)
		if i == 1 {
			s.Fail(ctx, id, "boom")
		} else {
			s.Complete(ctx, id, "/d/"+id, 5)
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
	id, _ := s.Add(ctx, "r1", "t", "sonarr", jobs.PriorityNormal)
	s.Complete(ctx, id, "/d", 1)
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
	claim(t, s)
	s.Fail(ctx, old, "boom")
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
	s.Fail(ctx, failed, "boom")
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
	queued, _ := s.Add(ctx, "r1", "t", "sonarr", jobs.PriorityNormal)
	done, _ := s.Add(ctx, "r2", "t", "sonarr", jobs.PriorityNormal)
	s.Complete(ctx, done, "/d", 1)
	for _, id := range []string{queued, done, "hls_missing"} {
		if _, err := s.Retry(ctx, id); err != jobs.ErrNotFound {
			t.Errorf("%s: err %v", id, err)
		}
	}
}
