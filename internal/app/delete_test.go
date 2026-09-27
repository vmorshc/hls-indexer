package app_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vmorshc/hls-indexer/internal/testenv"
)

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func waitGone(t *testing.T, p string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); exists(p); {
		if time.Now().After(deadline) {
			t.Fatalf("%s still exists", p)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func historyIDs(t *testing.T, e *testenv.Env, query string) []string {
	t.Helper()
	var ids []string
	for _, s := range slots(t, e, "history", query) {
		ids = append(ids, s["nzo_id"].(string))
	}
	return ids
}

func mustTrue(t *testing.T, what string, res map[string]any) {
	t.Helper()
	if res["status"] != true {
		t.Fatalf("%s: %v", what, res)
	}
}

// S1: queue delete stops a running job and drops queued ones. del_files=1
// removes only the job's folders. The freed worker runs the next job, and a
// repeated delete succeeds.
func TestQueueDelete(t *testing.T) {
	g := newGate("segment1.ts")
	e, rels := queueEnv(t, g, testenv.Options{Worker: true})
	inc, dl := e.Config.Paths.Incomplete, e.Config.Paths.Downloads
	run := grab(t, e.Env, rels[0], 0)
	queued := grab(t, e.Env, rels[1], 0)
	waitSlot(t, e.Env, run, hasStatus("Downloading"))
	stage := filepath.Join(inc, run)
	for deadline := time.Now().Add(10 * time.Second); !exists(stage); {
		if time.Now().After(deadline) {
			t.Fatal("no staging folder")
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, d := range []string{filepath.Join(dl, run), filepath.Join(dl, "other"), filepath.Join(inc, "other")} {
		os.MkdirAll(d, 0o755)
	}

	mustTrue(t, "delete running", sab(t, e.Env, "mode=queue&name=delete&del_files=1&value="+run))
	mustTrue(t, "delete queued", sab(t, e.Env, "mode=queue&name=delete&del_files=1&value="+queued))
	if s := queueSlot(t, e.Env, run); s != nil {
		t.Fatalf("running job still queued: %v", s)
	}
	if s := queueSlot(t, e.Env, queued); s != nil {
		t.Fatalf("queued job still queued: %v", s)
	}
	waitGone(t, stage)
	if exists(filepath.Join(dl, run)) {
		t.Error("downloads folder kept")
	}
	if !exists(filepath.Join(dl, "other")) || !exists(filepath.Join(inc, "other")) {
		t.Error("foreign folder deleted")
	}
	mustTrue(t, "delete again", sab(t, e.Env, "mode=queue&name=delete&del_files=1&value="+run))

	// The release is free again. The single worker slot is free for it while
	// the deleted run's segment is still held.
	next := grab(t, e.Env, rels[0], 0)
	if next == run {
		t.Fatal("dedup kept the deleted job")
	}
	waitSlot(t, e.Env, next, hasStatus("Downloading"))
	g.open()
	if s := waitHistory(t, e.Env, next); s["status"] != "Completed" {
		t.Fatalf("history %v", s)
	}
	time.Sleep(300 * time.Millisecond)
	if ids := historyIDs(t, e.Env, ""); len(ids) != 1 {
		t.Errorf("deleted jobs reached history: %v", ids)
	}
}

// S1: history delete archives with archive=1 (the default) and deletes with
// archive=0. del_files touches only the job's folder. Archived entries leave
// history and show under archive=1. A repeated delete succeeds.
func TestHistoryDelete(t *testing.T) {
	g := newGate("none")
	g.open()
	e, rels := queueEnv(t, g, testenv.Options{Worker: true})
	dl := e.Config.Paths.Downloads
	var ids []string
	for _, r := range rels[:3] {
		ids = append(ids, grab(t, e.Env, r, 0))
	}
	for _, id := range ids {
		waitHistory(t, e.Env, id)
	}
	a, b, c := ids[0], ids[1], ids[2]

	mustTrue(t, "archive", sab(t, e.Env, "mode=history&name=delete&del_files=0&archive=1&value="+a))
	mustTrue(t, "archive default", sab(t, e.Env, "mode=history&name=delete&value="+c))
	eq(t, "history", historyIDs(t, e.Env, ""), []string{b})
	eq(t, "archive", historyIDs(t, e.Env, "archive=1"), []string{c, a})
	if !exists(filepath.Join(dl, a)) || !exists(filepath.Join(dl, c)) {
		t.Error("archive with del_files=0 deleted files")
	}

	mustTrue(t, "delete", sab(t, e.Env, "mode=history&name=delete&del_files=1&archive=0&value="+b))
	eq(t, "history", historyIDs(t, e.Env, ""), nil)
	if exists(filepath.Join(dl, b)) {
		t.Error("del_files=1 kept the folder")
	}
	if !exists(filepath.Join(dl, a)) || !exists(filepath.Join(dl, c)) {
		t.Error("del_files=1 deleted another job's folder")
	}

	mustTrue(t, "delete archived", sab(t, e.Env, "mode=history&name=delete&del_files=1&archive=0&value="+a))
	eq(t, "archive", historyIDs(t, e.Env, "archive=1"), []string{c})
	if exists(filepath.Join(dl, a)) {
		t.Error("archived folder kept")
	}

	// A delete aimed at the other list changes nothing, files included.
	mustTrue(t, "queue delete of finished", sab(t, e.Env, "mode=queue&name=delete&del_files=1&value="+c))
	eq(t, "archive", historyIDs(t, e.Env, "archive=1"), []string{c})
	if !exists(filepath.Join(dl, c)) {
		t.Error("queue delete removed a finished job's folder")
	}

	for _, id := range []string{a, b, "hls_0000000000000000", "../x"} {
		mustTrue(t, "delete gone "+id, sab(t, e.Env, "mode=history&name=delete&del_files=1&archive=0&value="+id))
	}
	if !exists(dl) {
		t.Fatal("downloads root deleted")
	}
}
