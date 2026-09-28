// Package analytics is the channel-analysis logic: it combines Data API
// metadata, Analytics API queries and cached reach reports into strongly
// typed results. It has no MCP dependencies.
package analytics

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/gomcpgo/youtube_analytics/pkg/reach"
	"github.com/gomcpgo/youtube_analytics/pkg/youtube"
)

// reachSyncInterval throttles Reporting API syncs; new files arrive daily.
const reachSyncInterval = time.Hour

// Service analyzes one connected channel.
type Service struct {
	yt    *youtube.Client
	reach *reach.Cache // nil when the reach cache could not be opened
	now   func() time.Time
}

// New creates a service for one channel's client and reach cache.
func New(yt *youtube.Client, rc *reach.Cache) *Service {
	return &Service{yt: yt, reach: rc, now: time.Now}
}

// Coverage explains how much of a period the impressions/CTR data covers.
type Coverage struct {
	Available     bool   `json:"available"`
	DaysCovered   int    `json:"days_covered"`
	DaysRequested int    `json:"days_requested"`
	First         string `json:"first_day,omitempty"`
	Last          string `json:"last_day,omitempty"`
	Note          string `json:"note"`
}

// reachRows syncs the reach cache (at most hourly) and loads rows for p.
// Failures never fail the caller; they are explained in the coverage note.
func (s *Service) reachRows(ctx context.Context, reportType string, p Period) ([]reach.Row, Coverage) {
	cov := Coverage{DaysRequested: p.Days()}
	if s.reach == nil {
		cov.Note = "Impressions and CTR are unavailable: the local reach cache could not be opened."
		return nil, cov
	}
	_, syncErr := s.reach.Sync(ctx, s.yt, reachSyncInterval)
	rows, days, err := s.reach.Rows(reportType, p.Start, p.End)
	if err != nil {
		cov.Note = "Impressions and CTR are unavailable: " + err.Error()
		return nil, cov
	}
	cov.DaysCovered = len(days)
	if len(days) > 0 {
		cov.Available = true
		cov.First, cov.Last = days[0], days[len(days)-1]
	}
	st := s.reach.Status()
	switch {
	case cov.Available && cov.DaysCovered < cov.DaysRequested:
		cov.Note = fmt.Sprintf("Impressions and CTR cover %d of %d days (%s to %s). The Reporting API only has data from 30 days before reach reports were scheduled, and the newest days arrive about 2 days late.",
			cov.DaysCovered, cov.DaysRequested, cov.First, cov.Last)
	case cov.Available:
		cov.Note = fmt.Sprintf("Impressions and CTR cover the whole period (%s to %s).", cov.First, cov.Last)
	case syncErr != nil:
		cov.Note = "Impressions and CTR are unavailable: " + syncErr.Error()
	case st.Days == 0 && !st.FirstDataBy.IsZero() && s.now().Before(st.FirstDataBy):
		cov.Note = fmt.Sprintf("Impressions and CTR reports were scheduled on %s; YouTube delivers the first files by about %s (with 30 days of history).",
			st.Jobs[reach.Basic].CreateTime.In(youtube.Pacific).Format("2006-01-02 15:04 MST"), st.FirstDataBy.In(youtube.Pacific).Format("2006-01-02 15:04 MST"))
	case st.Days == 0:
		cov.Note = "Impressions and CTR: YouTube has not delivered any reach report files yet."
	default:
		cov.Note = fmt.Sprintf("Impressions and CTR are not available for this period; cached reach data spans %s to %s.", st.First, st.Last)
	}
	if syncErr != nil && cov.Available {
		cov.Note += " (Refreshing reach data failed: " + syncErr.Error() + ")"
	}
	return rows, cov
}

// query runs q; on HTTP 400 it retries once without the optional metrics,
// since metric support differs between report shapes.
func (s *Service) query(ctx context.Context, q youtube.Query, optional ...string) (*youtube.Table, error) {
	t, err := s.yt.Query(ctx, q)
	if err == nil || len(optional) == 0 || !youtube.IsBadRequest(err) {
		return t, err
	}
	drop := map[string]bool{}
	for _, m := range optional {
		drop[m] = true
	}
	var keep []string
	for _, m := range q.Metrics {
		if !drop[m] {
			keep = append(keep, m)
		}
	}
	q.Metrics = keep
	return s.yt.Query(ctx, q)
}

// parallel runs fns concurrently and waits for all of them.
func parallel(fns ...func()) {
	var wg sync.WaitGroup
	for _, f := range fns {
		wg.Add(1)
		go func(f func()) {
			defer wg.Done()
			f()
		}(f)
	}
	wg.Wait()
}

// errs collects per-section errors from parallel work.
type errs struct {
	mu sync.Mutex
	m  map[string]string
}

func (e *errs) set(section string, err error) {
	if err == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.m == nil {
		e.m = map[string]string{}
	}
	e.m[section] = err.Error()
}

func pct(part, whole float64) float64 {
	if whole == 0 {
		return 0
	}
	return part / whole * 100
}

func change(cur, prev float64) *float64 {
	if prev == 0 {
		return nil
	}
	c := (cur - prev) / prev * 100
	return &c
}

func fptr(f float64) *float64 { return &f }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func filterVideo(videoID string) []string {
	if videoID == "" {
		return nil
	}
	return []string{"video==" + videoID}
}
