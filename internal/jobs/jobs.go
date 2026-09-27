// Package jobs is the Redis job store: jobs, the pending queue, the active
// list, dedup and history. It is the only package that touches job keys.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Prefix starts every job key.
const Prefix = "hls-indexer:"

const (
	queueKey   = Prefix + "queue"   // zset: pending job IDs, score = priority order
	activeKey  = Prefix + "active"  // zset: queued and running job IDs, score = created ms
	historyKey = Prefix + "history" // zset: finished job IDs, score = finished ms
)

func jobKey(id string) string              { return Prefix + "job:" + id }
func dedupKey(category, rel string) string { return Prefix + "dedup:" + category + ":" + rel }

// Status values match the SABnzbd names.
const (
	Queued      = "Queued"
	Paused      = "Paused"
	Downloading = "Downloading"
	Completed   = "Completed"
	Failed      = "Failed"
)

// SAB priorities.
const (
	PriorityDefault = -100
	PriorityPaused  = -2
	PriorityLow     = -1
	PriorityNormal  = 0
	PriorityHigh    = 1
	PriorityForce   = 2
)

// Job is one download.
type Job struct {
	ID       string
	Release  string // release ID
	Title    string // release title, the output file name without extension
	Category string
	Priority int
	Status   string
	// Run counts claims. Worker writes carry it and apply only while it is
	// current, so a paused, resumed or deleted job ignores a stale run.
	Run      int64
	Created  time.Time
	Started  time.Time
	Finished time.Time
	// Progress. SizeEstimate is bandwidth × duration / 8 of the chosen variant.
	SegmentsTotal int
	SegmentsDone  int
	BytesDone     int64
	SizeEstimate  int64
	// Terminal fields.
	Bytes       int64 // size of the published file
	Storage     string
	FailMessage string
	Archived    bool // history delete with archive=1
}

// Store keeps jobs in Redis.
type Store struct {
	rdb *redis.Client
	now func() time.Time
}

func New(rdb *redis.Client) *Store { return &Store{rdb: rdb, now: time.Now} }

// createScript stores a job unless the same release is active in the category.
// It returns the ID of the job that owns the release. An empty queue score
// (paused job) keeps the job out of the queue.
var createScript = redis.NewScript(`
local existing = redis.call('GET', KEYS[1])
if existing then return existing end
redis.call('SET', KEYS[1], ARGV[1])
redis.call('HSET', KEYS[2], unpack(ARGV, 4))
if ARGV[2] ~= '' then redis.call('ZADD', KEYS[3], ARGV[2], ARGV[1]) end
redis.call('ZADD', KEYS[4], ARGV[3], ARGV[1])
return ARGV[1]
`)

// Add persists a new job and enqueues it. Priority -100 orders as normal.
// Priority -2 stores the job Paused and out of the queue until Resume. When the
// same release is already active in the same category it returns the existing
// job ID.
func (s *Store) Add(ctx context.Context, rel, title, category string, priority int) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	now := s.now()
	status, score := Queued, any(queueScore(priority, now))
	if priority == PriorityPaused {
		status, score = Paused, ""
	}
	args := []any{id, score, now.UnixMilli()}
	args = append(args, fields(Job{
		ID: id, Release: rel, Title: title, Category: category,
		Priority: priority, Status: status, Created: now,
	})...)
	return createScript.Run(ctx, s.rdb,
		[]string{dedupKey(category, rel), jobKey(id), queueKey, activeKey}, args...).Text()
}

// pauseScript pauses a Queued or Downloading job and takes it out of the
// queue. The worker sees the status change and stops the run.
var pauseScript = redis.NewScript(`
local st = redis.call('HGET', KEYS[1], 'status')
if st == 'Queued' or st == 'Downloading' then
  redis.call('HSET', KEYS[1], 'status', 'Paused')
  redis.call('ZREM', KEYS[2], ARGV[1])
  return 1
end
if st == 'Paused' then return 1 end
return 0
`)

// Pause pauses one active job. A paused job stays paused. It returns
// ErrNotFound unless the job is active. The API calls it.
func (s *Store) Pause(ctx context.Context, id string) error {
	ok, err := pauseScript.Run(ctx, s.rdb, []string{jobKey(id), queueKey}, id).Bool()
	if err == nil && !ok {
		err = ErrNotFound
	}
	return err
}

// resumeScript queues a Paused job again with priority ARGV[2] and score ARGV[3].
var resumeScript = redis.NewScript(`
local st = redis.call('HGET', KEYS[1], 'status')
if st == 'Paused' then
  redis.call('HSET', KEYS[1], 'status', 'Queued', 'priority', ARGV[2])
  redis.call('ZADD', KEYS[2], ARGV[3], ARGV[1])
  return 1
end
if st == 'Queued' or st == 'Downloading' then return 1 end
return 0
`)

// Resume queues a Paused job again. A job added with priority -2 resumes with
// normal priority, as in SABnzbd. Resuming a queued or running job does
// nothing. It returns ErrNotFound unless the job is active. The API calls it.
func (s *Store) Resume(ctx context.Context, id string) error {
	j, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	p := j.Priority
	if p == PriorityPaused {
		p = PriorityNormal
	}
	ok, err := resumeScript.Run(ctx, s.rdb, []string{jobKey(id), queueKey},
		id, p, queueScore(p, j.Created)).Bool()
	if err == nil && !ok {
		err = ErrNotFound
	}
	return err
}

// queueScore orders the queue: higher priority first, then oldest first.
// Default priority orders as normal.
func queueScore(priority int, created time.Time) float64 {
	if priority == PriorityDefault {
		priority = PriorityNormal
	}
	return float64(-int64(priority)*1e13 + created.UnixMilli())
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "hls_" + hex.EncodeToString(b), nil
}

// claimScript marks a popped job Downloading and starts a new run. A job the
// API paused after the pop stays paused.
var claimScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'status') ~= 'Queued' then return false end
redis.call('HSET', KEYS[1], 'status', 'Downloading', 'started', ARGV[1],
  'segments_total', 0, 'segments_done', 0, 'bytes_done', 0, 'size_estimate', 0)
return redis.call('HINCRBY', KEYS[1], 'run', 1)
`)

// Claim waits up to wait for a queued job, marks it Downloading and returns it.
// ok is false when nothing arrived in time.
func (s *Store) Claim(ctx context.Context, wait time.Duration) (Job, bool, error) {
	res, err := s.rdb.BZPopMin(ctx, wait, queueKey).Result()
	if errors.Is(err, redis.Nil) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	id := res.Member.(string)
	run, err := claimScript.Run(ctx, s.rdb, []string{jobKey(id)}, s.now().UnixMilli()).Int64()
	if errors.Is(err, redis.Nil) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	j, err := s.Get(ctx, id)
	j.Run = run // the job may have been claimed again since
	return j, err == nil, err
}

// requeueScript queues a Downloading or Queued job again with score ARGV[2].
// ARGV[3] is the priority the score came from. A resume in between changes
// it, and the resumed job keeps its own score.
var requeueScript = redis.NewScript(`
local st = redis.call('HGET', KEYS[1], 'status')
if st ~= 'Downloading' and st ~= 'Queued' then return 0 end
if redis.call('HGET', KEYS[1], 'priority') ~= ARGV[3] then return 0 end
redis.call('HSET', KEYS[1], 'status', 'Queued', 'segments_done', 0, 'bytes_done', 0)
redis.call('ZADD', KEYS[2], ARGV[2], ARGV[1])
return 1
`)

// Requeue puts every Downloading job back in the queue and re-adds Queued jobs
// a crash dropped between pop and claim. Paused jobs stay paused. The worker
// calls it at start.
func (s *Store) Requeue(ctx context.Context) error {
	active, err := s.list(ctx, activeKey, false)
	if err != nil {
		return err
	}
	for _, j := range active {
		err := requeueScript.Run(ctx, s.rdb, []string{jobKey(j.ID), queueKey},
			j.ID, queueScore(j.Priority, j.Created), j.Priority).Err()
		if err != nil {
			return err
		}
	}
	return nil
}

// setRunScript writes job fields only while run ARGV[1] is current. A paused
// job keeps its run, so the stopped run can reset its progress.
var setRunScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'run') ~= ARGV[1] then return 0 end
redis.call('HSET', KEYS[1], unpack(ARGV, 2))
return 1
`)

func (s *Store) setRun(ctx context.Context, j Job, values ...any) error {
	return setRunScript.Run(ctx, s.rdb, []string{jobKey(j.ID)}, append([]any{j.Run}, values...)...).Err()
}

// Start records the stream size once the media playlist is read.
func (s *Store) Start(ctx context.Context, j Job, segments int, sizeEstimate int64) error {
	return s.setRun(ctx, j, "segments_total", segments, "size_estimate", sizeEstimate,
		"segments_done", 0, "bytes_done", 0)
}

// Progress records written segments and bytes.
func (s *Store) Progress(ctx context.Context, j Job, segments int, bytes int64) error {
	return s.setRun(ctx, j, "segments_done", segments, "bytes_done", bytes)
}

// Current reports whether run j is still the job's live run: the job exists,
// is Downloading and nobody claimed it again.
func (s *Store) Current(ctx context.Context, j Job) (bool, error) {
	cur, err := s.Get(ctx, j.ID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil && cur.Status == Downloading && cur.Run == j.Run, err
}

// Complete moves the job to history as Completed. ok is false when the run is
// no longer current (paused, resumed or deleted) and nothing changed. A retry
// after a lost reply reports ok.
func (s *Store) Complete(ctx context.Context, j Job, storage string, bytes int64) (ok bool, err error) {
	return s.finish(ctx, j, "status", Completed, "storage", storage, "bytes", bytes)
}

// Fail moves the job to history as Failed. ok is as in Complete.
func (s *Store) Fail(ctx context.Context, j Job, message string) (ok bool, err error) {
	return s.finish(ctx, j, "status", Failed, "fail_message", message)
}

// finishScript moves a job to history if run ARGV[1] is the live run. A job
// this run already finished reports success, so a retried call is safe.
var finishScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'run') ~= ARGV[1] then return 0 end
local st = redis.call('HGET', KEYS[1], 'status')
if st == 'Completed' or st == 'Failed' then return 1 end
if st ~= 'Downloading' then return 0 end
redis.call('HSET', KEYS[1], unpack(ARGV, 4))
redis.call('ZREM', KEYS[2], ARGV[2])
redis.call('ZREM', KEYS[3], ARGV[2])
redis.call('ZADD', KEYS[4], ARGV[3], ARGV[2])
if redis.call('GET', KEYS[5]) == ARGV[2] then redis.call('DEL', KEYS[5]) end
return 1
`)

func (s *Store) finish(ctx context.Context, j Job, values ...any) (bool, error) {
	cur, err := s.Get(ctx, j.ID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	now := s.now().UnixMilli()
	args := append([]any{j.Run, j.ID, now}, append(values, "finished", now)...)
	return finishScript.Run(ctx, s.rdb,
		[]string{jobKey(j.ID), activeKey, queueKey, historyKey, dedupKey(cur.Category, cur.Release)},
		args...).Bool()
}

// Retry queues a failed job again as a new job and drops the failed one from
// history, like SABnzbd. The new job resolves the stream from scratch. When
// the release is already active in the category, the active job's ID comes
// back, as in Add. It returns ErrNotFound unless id is a Failed job.
func (s *Store) Retry(ctx context.Context, id string) (string, error) {
	j, err := s.Get(ctx, id)
	if err != nil {
		return "", err
	}
	if j.Status != Failed {
		return "", ErrNotFound
	}
	newID, err := s.Add(ctx, j.Release, j.Title, j.Category, j.Priority)
	if err != nil {
		return "", err
	}
	_, err = s.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.ZRem(ctx, historyKey, id)
		p.Del(ctx, jobKey(id))
		return nil
	})
	return newID, err
}

// deleteActiveScript drops a Queued, Paused or Downloading job. The worker sees
// the job gone and stops the run.
var deleteActiveScript = redis.NewScript(`
local st = redis.call('HGET', KEYS[1], 'status')
if not st then return 1 end
if st ~= 'Queued' and st ~= 'Paused' and st ~= 'Downloading' then return 0 end
redis.call('ZREM', KEYS[2], ARGV[1])
redis.call('ZREM', KEYS[3], ARGV[1])
if redis.call('GET', KEYS[4]) == ARGV[1] then redis.call('DEL', KEYS[4]) end
redis.call('DEL', KEYS[1])
return 1
`)

// DeleteQueued drops an active job. gone is true when the job no longer
// exists: dropped now or before. A finished job stays and gone is false. The
// API calls it.
func (s *Store) DeleteQueued(ctx context.Context, id string) (gone bool, err error) {
	j, err := s.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return deleteActiveScript.Run(ctx, s.rdb,
		[]string{jobKey(id), queueKey, activeKey, dedupKey(j.Category, j.Release)}, id).Bool()
}

// deleteHistoryScript archives (ARGV[2] = 1) or deletes a finished job.
var deleteHistoryScript = redis.NewScript(`
local st = redis.call('HGET', KEYS[1], 'status')
if not st then return 1 end
if st ~= 'Completed' and st ~= 'Failed' then return 0 end
if ARGV[2] == '1' then
  redis.call('HSET', KEYS[1], 'archived', 1)
else
  redis.call('ZREM', KEYS[2], ARGV[1])
  redis.call('DEL', KEYS[1])
end
return 1
`)

// DeleteHistory archives or deletes a finished job. An archived entry leaves
// the default History list. done is true when the job was finished or is
// gone. An active job stays and done is false. The API calls it.
func (s *Store) DeleteHistory(ctx context.Context, id string, archive bool) (done bool, err error) {
	a := 0
	if archive {
		a = 1
	}
	return deleteHistoryScript.Run(ctx, s.rdb, []string{jobKey(id), historyKey}, id, a).Bool()
}

// ErrNotFound marks an unknown job ID.
var ErrNotFound = errors.New("job not found")

// Get loads one job.
func (s *Store) Get(ctx context.Context, id string) (Job, error) {
	m, err := s.rdb.HGetAll(ctx, jobKey(id)).Result()
	if err != nil {
		return Job{}, err
	}
	if len(m) == 0 {
		return Job{}, ErrNotFound
	}
	return parse(m), nil
}

// Queue lists queued and running jobs, oldest first. An empty category means all.
func (s *Store) Queue(ctx context.Context, category string) ([]Job, error) {
	js, err := s.list(ctx, activeKey, false)
	return filter(js, category), err
}

// History lists finished jobs, newest first, filtered by category and status
// (case-insensitive) and then paged. Empty filters match all. limit 0 means all.
// archived selects archived entries instead of the others.
func (s *Store) History(ctx context.Context, category, status string, archived bool, start, limit int) (jobs []Job, total int, err error) {
	js, err := s.list(ctx, historyKey, true)
	if err != nil {
		return nil, 0, err
	}
	all := filter(js, category)
	all = slices.DeleteFunc(all, func(j Job) bool { return j.Archived != archived })
	if status != "" {
		all = slices.DeleteFunc(all, func(j Job) bool { return !strings.EqualFold(j.Status, status) })
	}
	total = len(all)
	if start >= len(all) {
		return []Job{}, total, nil
	}
	all = all[start:]
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}
	return all, total, nil
}

func filter(js []Job, category string) []Job {
	out := []Job{}
	for _, j := range js {
		if category == "" || j.Category == category {
			out = append(out, j)
		}
	}
	return out
}

// list loads every job in a sorted set, ascending or descending by score.
func (s *Store) list(ctx context.Context, key string, desc bool) ([]Job, error) {
	var ids []string
	var err error
	if desc {
		ids, err = s.rdb.ZRevRange(ctx, key, 0, -1).Result()
	} else {
		ids, err = s.rdb.ZRange(ctx, key, 0, -1).Result()
	}
	if err != nil {
		return nil, err
	}
	cmds := make([]*redis.MapStringStringCmd, len(ids))
	_, err = s.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, id := range ids {
			cmds[i] = p.HGetAll(ctx, jobKey(id))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]Job, 0, len(ids))
	for _, c := range cmds {
		if m := c.Val(); len(m) > 0 {
			out = append(out, parse(m))
		}
	}
	return out, nil
}

// fields encodes the creation fields of a job as HSET arguments.
func fields(j Job) []any {
	return []any{
		"id", j.ID, "release", j.Release, "title", j.Title, "category", j.Category,
		"priority", j.Priority, "status", j.Status, "created", j.Created.UnixMilli(),
	}
}

func parse(m map[string]string) Job {
	i := func(k string) int64 { n, _ := strconv.ParseInt(m[k], 10, 64); return n }
	t := func(k string) time.Time {
		if m[k] == "" {
			return time.Time{}
		}
		return time.UnixMilli(i(k))
	}
	return Job{
		ID: m["id"], Release: m["release"], Title: m["title"], Category: m["category"],
		Priority: int(i("priority")), Status: m["status"], Run: i("run"),
		Created: t("created"), Started: t("started"), Finished: t("finished"),
		SegmentsTotal: int(i("segments_total")), SegmentsDone: int(i("segments_done")),
		BytesDone: i("bytes_done"), SizeEstimate: i("size_estimate"),
		Bytes: i("bytes"), Storage: m["storage"], FailMessage: m["fail_message"],
		Archived: m["archived"] == "1",
	}
}
