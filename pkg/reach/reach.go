// Package reach keeps a local copy of the Reporting API reach reports, the
// only API source of thumbnail impressions and impressions click-through
// rate. YouTube generates one CSV per day per report type once a job exists,
// backfills the 30 days before the job was created, and deletes files after
// 30-60 days, so this cache is what builds a longer history.
package reach

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gomcpgo/youtube_analytics/pkg/youtube"
)

// Report types.
const (
	Basic    = "channel_reach_basic_a1"    // date, video
	Combined = "channel_reach_combined_a1" // + traffic source, device, OS
)

// ReportTypes are the jobs this server schedules for every connected channel.
var ReportTypes = []string{Basic, Combined}

// API is the Reporting API surface the cache needs.
type API interface {
	Jobs(ctx context.Context) ([]youtube.Job, error)
	CreateJob(ctx context.Context, reportTypeID, name string) (*youtube.Job, error)
	Reports(ctx context.Context, jobID string) ([]youtube.Report, error)
	Download(ctx context.Context, url string) ([]byte, error)
}

// Cache is the on-disk reach report store for one channel.
type Cache struct {
	dir string
	mu  sync.Mutex
	idx index
	now func() time.Time
}

type index struct {
	Jobs     map[string]JobInfo            `json:"jobs"`
	Days     map[string]map[string]DayFile `json:"days"` // report type -> YYYY-MM-DD -> file
	LastSync time.Time                     `json:"last_sync"`
	// PercentCTR is set once any row shows a CTR above 1, which proves the
	// column is a percentage rather than a 0-1 ratio.
	PercentCTR bool `json:"percent_ctr,omitempty"`
}

// JobInfo is a scheduled reporting job.
type JobInfo struct {
	ID         string    `json:"id"`
	CreateTime time.Time `json:"create_time"`
}

// DayFile is one cached daily report.
type DayFile struct {
	ReportID   string    `json:"report_id"`
	CreateTime time.Time `json:"create_time"`
}

// Open loads or creates the cache in dir.
func Open(dir string) (*Cache, error) {
	c := &Cache{dir: dir, now: time.Now, idx: index{Jobs: map[string]JobInfo{}, Days: map[string]map[string]DayFile{}}}
	b, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err == nil {
		if err := json.Unmarshal(b, &c.idx); err != nil {
			return nil, fmt.Errorf("parse reach index: %w", err)
		}
		if c.idx.Jobs == nil {
			c.idx.Jobs = map[string]JobInfo{}
		}
		if c.idx.Days == nil {
			c.idx.Days = map[string]map[string]DayFile{}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return c, nil
}

func (c *Cache) saveLocked() error {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c.idx, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(c.dir, "index.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(c.dir, "index.json"))
}

// EnsureJobs schedules any missing reach jobs and returns the ones it created.
func (c *Cache) EnsureJobs(ctx context.Context, api API) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ensureJobsLocked(ctx, api)
}

func (c *Cache) ensureJobsLocked(ctx context.Context, api API) ([]string, error) {
	jobs, err := api.Jobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list reporting jobs: %w", err)
	}
	// Rebuild from the server so a job deleted elsewhere is recreated; when
	// several jobs share a type, the oldest one has the longest history.
	c.idx.Jobs = map[string]JobInfo{}
	for _, j := range jobs {
		cur, ok := c.idx.Jobs[j.ReportTypeID]
		if !ok || j.CreateTime.Before(cur.CreateTime) {
			c.idx.Jobs[j.ReportTypeID] = JobInfo{ID: j.ID, CreateTime: j.CreateTime}
		}
	}
	var created []string
	var errs []string
	for _, rt := range ReportTypes {
		if _, ok := c.idx.Jobs[rt]; ok {
			continue
		}
		j, err := api.CreateJob(ctx, rt, "gomcpgo youtube_analytics "+rt)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", rt, err))
			continue
		}
		ct := j.CreateTime
		if ct.IsZero() {
			ct = c.now().UTC()
		}
		c.idx.Jobs[rt] = JobInfo{ID: j.ID, CreateTime: ct}
		created = append(created, rt)
	}
	if err := c.saveLocked(); err != nil {
		return created, err
	}
	if len(errs) > 0 {
		return created, fmt.Errorf("could not schedule reach reports: %s", strings.Join(errs, "; "))
	}
	return created, nil
}

// SyncResult summarizes a sync.
type SyncResult struct {
	Skipped     bool
	JobsCreated []string
	Downloaded  int
}

// Sync ensures jobs exist and downloads any new or replaced daily reports.
// It is a no-op when the last sync is younger than maxAge.
func (c *Cache) Sync(ctx context.Context, api API, maxAge time.Duration) (SyncResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if maxAge > 0 && c.now().Sub(c.idx.LastSync) < maxAge {
		return SyncResult{Skipped: true}, nil
	}
	var res SyncResult
	created, err := c.ensureJobsLocked(ctx, api)
	res.JobsCreated = created
	if err != nil && len(c.idx.Jobs) == 0 {
		return res, err
	}
	for _, rt := range ReportTypes {
		job, ok := c.idx.Jobs[rt]
		if !ok {
			continue
		}
		reports, err := api.Reports(ctx, job.ID)
		if err != nil {
			return res, fmt.Errorf("list %s reports: %w", rt, err)
		}
		// Backfills reuse a day with a newer createTime; keep the newest.
		best := map[string]youtube.Report{}
		for _, r := range reports {
			d := r.StartTime.In(youtube.Pacific).Format("2006-01-02")
			if cur, ok := best[d]; !ok || r.CreateTime.After(cur.CreateTime) {
				best[d] = r
			}
		}
		if c.idx.Days[rt] == nil {
			c.idx.Days[rt] = map[string]DayFile{}
		}
		for d, r := range best {
			have, ok := c.idx.Days[rt][d]
			if ok && (have.ReportID == r.ID || !r.CreateTime.After(have.CreateTime)) {
				continue
			}
			body, err := api.Download(ctx, r.DownloadURL)
			if err != nil {
				return res, fmt.Errorf("download %s %s: %w", rt, d, err)
			}
			if err := c.writeDay(rt, d, body); err != nil {
				return res, err
			}
			c.idx.Days[rt][d] = DayFile{ReportID: r.ID, CreateTime: r.CreateTime}
			res.Downloaded++
		}
	}
	c.idx.LastSync = c.now().UTC()
	return res, c.saveLocked()
}

func (c *Cache) writeDay(rt, day string, body []byte) error {
	rows, err := parseCSV(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("parse %s %s: %w", rt, day, err)
	}
	for _, r := range rows {
		if r.rawCTR > 1 {
			c.idx.PercentCTR = true
			break
		}
	}
	dir := filepath.Join(c.dir, rt)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, day+".csv"), body, 0o600)
}

// Status describes what the cache holds.
type Status struct {
	Jobs     map[string]JobInfo `json:"jobs"`
	First    string             `json:"first_day,omitempty"`
	Last     string             `json:"last_day,omitempty"`
	Days     int                `json:"days"`
	LastSync time.Time          `json:"last_sync,omitempty"`
	// FirstDataBy is when YouTube should have produced the first reports
	// (about 48 hours after the job was created).
	FirstDataBy time.Time `json:"first_data_by,omitempty"`
}

// Status returns the cache state for the basic report.
func (c *Cache) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Status{Jobs: map[string]JobInfo{}, LastSync: c.idx.LastSync}
	for k, v := range c.idx.Jobs {
		s.Jobs[k] = v
	}
	days := sortedDays(c.idx.Days[Basic])
	if len(days) > 0 {
		s.First, s.Last, s.Days = days[0], days[len(days)-1], len(days)
	}
	if j, ok := c.idx.Jobs[Basic]; ok {
		s.FirstDataBy = j.CreateTime.Add(48 * time.Hour)
	}
	return s
}

// Row is one reach record. Clicks is derived as impressions x CTR so that
// CTR can be re-aggregated correctly across rows.
type Row struct {
	Date          string
	VideoID       string
	TrafficSource int // -1 in the basic report
	DeviceType    int // -1 in the basic report
	Impressions   float64
	Clicks        float64
	rawCTR        float64
}

// Rows loads rows of a report type for days in [from, to] (YYYY-MM-DD).
// It also returns the days that were present.
func (c *Cache) Rows(reportType, from, to string) ([]Row, []string, error) {
	c.mu.Lock()
	days := sortedDays(c.idx.Days[reportType])
	percent := c.idx.PercentCTR
	c.mu.Unlock()
	var out []Row
	var present []string
	for _, d := range days {
		if d < from || d > to {
			continue
		}
		f, err := os.Open(filepath.Join(c.dir, reportType, d+".csv"))
		if err != nil {
			continue
		}
		rows, err := parseCSV(f)
		f.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("parse cached %s %s: %w", reportType, d, err)
		}
		for _, r := range rows {
			if r.Date == "" {
				r.Date = d
			}
			ratio := r.rawCTR
			if percent {
				ratio /= 100
			}
			r.Clicks = r.Impressions * ratio
			out = append(out, r)
		}
		present = append(present, d)
	}
	return out, present, nil
}

func sortedDays(m map[string]DayFile) []string {
	out := make([]string, 0, len(m))
	for d := range m {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func parseCSV(r io.Reader) ([]Row, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err == io.EOF {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))] = i
	}
	ii, ok1 := col["video_thumbnail_impressions"]
	ci, ok2 := col["video_thumbnail_impressions_ctr"]
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("unexpected columns %v", header)
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	intOr := func(s string, def int) int {
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
		return def
	}
	var out []Row
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		imp, _ := strconv.ParseFloat(strings.TrimSpace(rec[ii]), 64)
		ctr, _ := strconv.ParseFloat(strings.TrimSpace(rec[ci]), 64)
		out = append(out, Row{
			Date:          normDate(get(rec, "date")),
			VideoID:       get(rec, "video_id"),
			TrafficSource: intOr(get(rec, "traffic_source_type"), -1),
			DeviceType:    intOr(get(rec, "device_type"), -1),
			Impressions:   imp,
			rawCTR:        ctr,
		})
	}
}

func normDate(s string) string {
	if len(s) == 8 && !strings.Contains(s, "-") {
		return s[:4] + "-" + s[4:6] + "-" + s[6:]
	}
	return s
}

// Agg is summed impressions and clicks.
type Agg struct {
	Impressions float64 `json:"impressions"`
	Clicks      float64 `json:"clicks"`
}

// CTR is the aggregate click-through rate in percent.
func (a Agg) CTR() float64 {
	if a.Impressions == 0 {
		return 0
	}
	return a.Clicks / a.Impressions * 100
}

// Add accumulates a row.
func (a *Agg) Add(r Row) {
	a.Impressions += r.Impressions
	a.Clicks += r.Clicks
}

// Total sums rows, optionally only for one video.
func Total(rows []Row, videoID string) Agg {
	var a Agg
	for _, r := range rows {
		if videoID == "" || r.VideoID == videoID {
			a.Add(r)
		}
	}
	return a
}

// GroupBy sums rows by key.
func GroupBy(rows []Row, key func(Row) string) map[string]*Agg {
	out := map[string]*Agg{}
	for _, r := range rows {
		k := key(r)
		a, ok := out[k]
		if !ok {
			a = &Agg{}
			out[k] = a
		}
		a.Add(r)
	}
	return out
}

// TrafficSourceName labels the Reporting API traffic_source_type codes.
func TrafficSourceName(code int) string {
	if s, ok := trafficSources[code]; ok {
		return s
	}
	return "Other (" + strconv.Itoa(code) + ")"
}

var trafficSources = map[int]string{
	0: "Direct or unknown", 1: "YouTube advertising", 3: "Browse features", 4: "Channel pages",
	5: "YouTube search", 7: "Suggested videos", 8: "Other YouTube features", 9: "External",
	11: "Video cards and annotations", 14: "Playlists", 17: "Notifications", 18: "Playlist pages",
	19: "Programming from claimed content", 20: "End screens", 23: "Stories", 24: "Shorts feed",
	25: "Product pages", 26: "Hashtag pages", 27: "Sound pages", 28: "Live redirect", 29: "Podcasts",
	30: "Remixed video", 31: "Vertical live feed", 32: "Related video",
}

// DeviceName labels the Reporting API device_type codes.
func DeviceName(code int) string {
	switch code {
	case 101:
		return "Computer"
	case 102:
		return "TV"
	case 103:
		return "Game console"
	case 104:
		return "Mobile phone"
	case 105:
		return "Tablet"
	}
	return "Unknown"
}
