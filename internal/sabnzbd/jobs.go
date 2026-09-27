package sabnzbd

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/vmorshc/hls-indexer/internal/jobs"
	"github.com/vmorshc/hls-indexer/internal/release"
)

var errBadNZB = errors.New("Unknown release")

// addfile accepts an HLS Indexer NZB and creates a job. It never waits for the download.
func (h *Handler) addfile(w http.ResponseWriter, r *http.Request) {
	if h.jobs == nil {
		http.Error(w, "job store unavailable", http.StatusServiceUnavailable)
		return
	}
	f, hdr, err := formFile(r)
	if err != nil {
		writeError(w, "Unknown release")
		return
	}
	defer f.Close()
	rid, err := parseNZB(f)
	if err != nil || !h.hasSource(rid.Source) {
		writeError(w, "Unknown release")
		return
	}
	cat := r.FormValue("cat")
	title := r.FormValue("nzbname")
	if title == "" {
		title = hdr.Filename
	}
	id, err := h.jobs.Add(r.Context(), rid.String(), jobTitle(title, rid), cat, priority(r.FormValue("priority")))
	if err != nil {
		h.log.Error("addfile", "err", err)
		http.Error(w, "job store unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]any{"status": true, "nzo_ids": []string{id}})
}

// retry queues a Failed job again as a new job with a fresh resolve.
func (h *Handler) retry(w http.ResponseWriter, r *http.Request) {
	if h.jobs == nil {
		http.Error(w, "job store unavailable", http.StatusServiceUnavailable)
		return
	}
	id, err := h.jobs.Retry(r.Context(), r.FormValue("value"))
	if errors.Is(err, jobs.ErrNotFound) {
		writeError(w, "Unknown job")
		return
	}
	if err != nil {
		h.log.Error("retry", "err", err)
		http.Error(w, "job store unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]any{"status": true, "nzo_id": id})
}

// formFile returns the uploaded NZB. SABnzbd names the field "name"; "nzbfile" is also accepted.
func formFile(r *http.Request) (io.ReadCloser, fileHeader, error) {
	for _, k := range []string{"name", "nzbfile"} {
		if f, h, err := r.FormFile(k); err == nil {
			return f, fileHeader{Filename: h.Filename}, nil
		}
	}
	return nil, fileHeader{}, errBadNZB
}

type fileHeader struct{ Filename string }

// parseNZB reads the release ID from an HLS Indexer NZB. Any DTD or entity
// declaration rejects the document.
func parseNZB(r io.Reader) (release.ID, error) {
	d := xml.NewDecoder(r)
	var files int
	var subject string
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return release.ID{}, err
		}
		switch t := tok.(type) {
		case xml.Directive:
			return release.ID{}, errBadNZB
		case xml.StartElement:
			if t.Name.Local != "file" {
				continue
			}
			files++
			for _, a := range t.Attr {
				if a.Name.Local == "subject" {
					subject = a.Value
				}
			}
		}
	}
	if files != 1 || !strings.HasPrefix(subject, release.NZBSubjectPrefix) {
		return release.ID{}, errBadNZB
	}
	return release.Parse(strings.TrimPrefix(subject, release.NZBSubjectPrefix))
}

// jobTitle turns the uploaded file name into a safe output name. *arr names the
// upload after the release title. Without one the release ID is used.
func jobTitle(name string, rid release.ID) string {
	name = strings.TrimSuffix(path.Base(strings.ReplaceAll(name, `\`, "/")), ".nzb")
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return -1
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if name == "" {
		return strings.ReplaceAll(rid.String(), ":", "_")
	}
	return name
}

var priorities = map[int]string{
	jobs.PriorityDefault: "Normal", jobs.PriorityPaused: "Paused", jobs.PriorityLow: "Low", jobs.PriorityNormal: "Normal",
	jobs.PriorityHigh: "High", jobs.PriorityForce: "Force",
}

func priority(s string) int {
	n, err := strconv.Atoi(s)
	if _, ok := priorities[n]; err != nil || !ok {
		return jobs.PriorityDefault
	}
	return n
}

type queueSlot struct {
	NzoID      string `json:"nzo_id"`
	Index      int    `json:"index"`
	Filename   string `json:"filename"`
	Cat        string `json:"cat"`
	Status     string `json:"status"`
	Priority   string `json:"priority"`
	MB         string `json:"mb"`
	MBLeft     string `json:"mbleft"`
	Percentage string `json:"percentage"`
	TimeLeft   string `json:"timeleft"`
}

func (h *Handler) queue(w http.ResponseWriter, r *http.Request) {
	if h.jobs == nil {
		http.Error(w, "job store unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.FormValue("name") {
	case "":
	case "pause":
		h.control(w, r, h.jobs.Pause)
		return
	case "resume":
		h.control(w, r, h.jobs.Resume)
		return
	case "delete":
		h.delete(w, r, h.jobs.DeleteQueued)
		return
	default:
		writeError(w, "not implemented")
		return
	}
	js, err := h.jobs.Queue(r.Context(), r.FormValue("category"))
	if err != nil {
		h.log.Error("queue", "err", err)
		http.Error(w, "job store unavailable", http.StatusServiceUnavailable)
		return
	}
	total := len(js)
	js = page(js, r)
	slots := make([]queueSlot, len(js))
	now := time.Now()
	for i, j := range js {
		size := float64(j.SizeEstimate)
		left := math.Max(size-float64(j.BytesDone), 0)
		pct := 0
		if j.SegmentsTotal > 0 {
			pct = j.SegmentsDone * 100 / j.SegmentsTotal
		}
		slots[i] = queueSlot{
			NzoID: j.ID, Index: i, Filename: j.Title, Cat: j.Category, Status: j.Status,
			Priority: priorities[j.Priority], MB: mib(size), MBLeft: mib(left),
			Percentage: strconv.Itoa(pct), TimeLeft: timeLeft(j, now),
		}
	}
	writeJSON(w, map[string]any{"queue": map[string]any{"paused": false, "noofslots": total, "slots": slots}})
}

// control pauses or resumes the job in value.
func (h *Handler) control(w http.ResponseWriter, r *http.Request, do func(context.Context, string) error) {
	id := r.FormValue("value")
	err := do(r.Context(), id)
	if errors.Is(err, jobs.ErrNotFound) {
		writeError(w, "Unknown job")
		return
	}
	if err != nil {
		h.log.Error("queue control", "err", err)
		http.Error(w, "job store unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, map[string]any{"status": true, "nzo_ids": []string{id}})
}

// jobID matches the IDs jobs.Add creates. Only such IDs name folders to delete.
var jobID = regexp.MustCompile(`^hls_[0-9a-f]{16}$`)

// delete drops the job in value with do. do reports false when the job is in
// the other list: nothing changes. Otherwise del_files=1 removes only the
// job's own folders in incomplete and downloads. A job already gone succeeds.
func (h *Handler) delete(w http.ResponseWriter, r *http.Request, do func(context.Context, string) (bool, error)) {
	id := r.FormValue("value")
	if !jobID.MatchString(id) {
		writeJSON(w, map[string]any{"status": true})
		return
	}
	ok, err := do(r.Context(), id)
	if err != nil {
		h.log.Error("delete", "err", err)
		http.Error(w, "job store unavailable", http.StatusServiceUnavailable)
		return
	}
	if ok && r.FormValue("del_files") == "1" {
		for _, dir := range []string{h.incompleteDir, h.completeDir} {
			if err := os.RemoveAll(filepath.Join(dir, id)); err != nil {
				h.log.Error("delete files", "job", id, "err", err)
				writeError(w, "Failed to delete files")
				return
			}
		}
	}
	writeJSON(w, map[string]any{"status": true})
}

func mib(b float64) string { return fmt.Sprintf("%.2f", b/(1<<20)) }

// timeLeft extrapolates the elapsed time over the remaining segments.
func timeLeft(j jobs.Job, now time.Time) string {
	var d time.Duration
	if j.Status == jobs.Downloading && j.SegmentsDone > 0 && j.SegmentsTotal > j.SegmentsDone {
		elapsed := now.Sub(j.Started)
		d = elapsed * time.Duration(j.SegmentsTotal-j.SegmentsDone) / time.Duration(j.SegmentsDone)
	}
	s := int(d.Seconds())
	return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
}

type historySlot struct {
	NzoID        string `json:"nzo_id"`
	Name         string `json:"name"`
	NzbName      string `json:"nzb_name"`
	Category     string `json:"category"`
	Status       string `json:"status"`
	FailMessage  string `json:"fail_message"`
	Bytes        int64  `json:"bytes"`
	DownloadTime int64  `json:"download_time"`
	Storage      string `json:"storage"`
	Completed    int64  `json:"completed"`
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	if h.jobs == nil {
		http.Error(w, "job store unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.FormValue("name") {
	case "":
	case "delete":
		// archive defaults to 1, as in SABnzbd 4.
		archive := r.FormValue("archive") != "0"
		h.delete(w, r, func(ctx context.Context, id string) (bool, error) {
			return h.jobs.DeleteHistory(ctx, id, archive)
		})
		return
	default:
		writeError(w, "not implemented")
		return
	}
	start, limit := atoi(r.FormValue("start")), atoi(r.FormValue("limit"))
	archived := r.FormValue("archive") == "1"
	js, total, err := h.jobs.History(r.Context(), r.FormValue("category"), r.FormValue("status"), archived, start, limit)
	if err != nil {
		h.log.Error("history", "err", err)
		http.Error(w, "job store unavailable", http.StatusServiceUnavailable)
		return
	}
	slots := make([]historySlot, len(js))
	for i, j := range js {
		var dl int64
		if !j.Started.IsZero() {
			dl = int64(j.Finished.Sub(j.Started).Seconds())
		}
		slots[i] = historySlot{
			NzoID: j.ID, Name: j.Title, NzbName: j.Title + ".nzb", Category: j.Category,
			Status: j.Status, FailMessage: j.FailMessage, Bytes: j.Bytes, DownloadTime: dl,
			Storage: j.Storage, Completed: j.Finished.Unix(),
		}
	}
	writeJSON(w, map[string]any{"history": map[string]any{"noofslots": total, "slots": slots}})
}

// page applies start and limit. limit 0 means all.
func page(js []jobs.Job, r *http.Request) []jobs.Job {
	start, limit := atoi(r.FormValue("start")), atoi(r.FormValue("limit"))
	if start >= len(js) {
		return js[:0]
	}
	js = js[start:]
	if limit > 0 && limit < len(js) {
		js = js[:limit]
	}
	return js
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
