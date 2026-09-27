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
}

// Store keeps jobs in Redis.
type Store struct {
	rdb *redis.Client
	now func() time.Time
}

func New(rdb *redis.Client) *Store { return &Store{rdb: rdb, now: time.Now} }

// createScript stores a job unless the same release is active in the category.
// It returns the ID of the job that owns the release.
var createScript = redis.NewScript(`
local existing = redis.call('GET', KEYS[1])
if existing then return existing end
redis.call('SET', KEYS[1], ARGV[1])
redis.call('HSET', KEYS[2], unpack(ARGV, 4))
redis.call('ZADD', KEYS[3], ARGV[2], ARGV[1])
redis.call('ZADD', KEYS[4], ARGV[3], ARGV[1])
return ARGV[1]
`)

// Add persists a new job and enqueues it. When the same release is already
// active in the same category it returns the existing job ID.
func (s *Store) Add(ctx context.Context, rel, title, category string, priority int) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	if priority == PriorityDefault {
		priority = PriorityNormal
	}
	now := s.now()
	args := []any{id, queueScore(priority, now), now.UnixMilli()}
	args = append(args, fields(Job{
		ID: id, Release: rel, Title: title, Category: category,
		Priority: priority, Status: Queued, Created: now,
	})...)
	return createScript.Run(ctx, s.rdb,
		[]string{dedupKey(category, rel), jobKey(id), queueKey, activeKey}, args...).Text()
}

// queueScore orders the queue: higher priority first, then oldest first.
func queueScore(priority int, created time.Time) float64 {
	return float64(-int64(priority)*1e13 + created.UnixMilli())
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "hls_" + hex.EncodeToString(b), nil
}

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
	if err := s.rdb.HSet(ctx, jobKey(id), "status", Downloading, "started", s.now().UnixMilli()).Err(); err != nil {
		return Job{}, false, err
	}
	j, err := s.Get(ctx, id)
	return j, err == nil, err
}

// Requeue puts every Downloading job back in the queue. The worker calls it at start.
func (s *Store) Requeue(ctx context.Context) error {
	active, err := s.list(ctx, activeKey, false)
	if err != nil {
		return err
	}
	for _, j := range active {
		if j.Status != Downloading {
			continue
		}
		_, err := s.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
			p.HSet(ctx, jobKey(j.ID), "status", Queued, "segments_done", 0, "bytes_done", 0)
			p.ZAdd(ctx, queueKey, redis.Z{Score: queueScore(j.Priority, j.Created), Member: j.ID})
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// Start records the stream size once the media playlist is read.
func (s *Store) Start(ctx context.Context, id string, segments int, sizeEstimate int64) error {
	return s.rdb.HSet(ctx, jobKey(id), "segments_total", segments, "size_estimate", sizeEstimate,
		"segments_done", 0, "bytes_done", 0).Err()
}

// Progress records written segments and bytes.
func (s *Store) Progress(ctx context.Context, id string, segments int, bytes int64) error {
	return s.rdb.HSet(ctx, jobKey(id), "segments_done", segments, "bytes_done", bytes).Err()
}

// Complete moves the job to history as Completed.
func (s *Store) Complete(ctx context.Context, id, storage string, bytes int64) error {
	return s.finish(ctx, id, "status", Completed, "storage", storage, "bytes", bytes)
}

// Fail moves the job to history as Failed.
func (s *Store) Fail(ctx context.Context, id, message string) error {
	return s.finish(ctx, id, "status", Failed, "fail_message", message)
}

func (s *Store) finish(ctx context.Context, id string, values ...any) error {
	j, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	now := s.now()
	_, err = s.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.HSet(ctx, jobKey(id), append(values, "finished", now.UnixMilli())...)
		p.ZRem(ctx, activeKey, id)
		p.ZRem(ctx, queueKey, id)
		p.ZAdd(ctx, historyKey, redis.Z{Score: float64(now.UnixMilli()), Member: id})
		p.Del(ctx, dedupKey(j.Category, j.Release))
		return nil
	})
	return err
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
func (s *Store) History(ctx context.Context, category, status string, start, limit int) (jobs []Job, total int, err error) {
	js, err := s.list(ctx, historyKey, true)
	if err != nil {
		return nil, 0, err
	}
	all := filter(js, category)
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
		Priority: int(i("priority")), Status: m["status"],
		Created: t("created"), Started: t("started"), Finished: t("finished"),
		SegmentsTotal: int(i("segments_total")), SegmentsDone: int(i("segments_done")),
		BytesDone: i("bytes_done"), SizeEstimate: i("size_estimate"),
		Bytes: i("bytes"), Storage: m["storage"], FailMessage: m["fail_message"],
	}
}
