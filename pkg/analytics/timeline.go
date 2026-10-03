package analytics

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gomcpgo/youtube_analytics/pkg/reach"
	"github.com/gomcpgo/youtube_analytics/pkg/youtube"
)

// TimelineRow is one day or month.
type TimelineRow struct {
	Date   string             `json:"date"`
	Values map[string]float64 `json:"values"`
}

// SeriesSummary summarizes one metric over the timeline.
type SeriesSummary struct {
	Metric   string   `json:"metric"`
	Total    *float64 `json:"total,omitempty"` // only for additive metrics
	Mean     float64  `json:"mean"`
	Max      float64  `json:"max"`
	MaxDate  string   `json:"max_date"`
	Min      float64  `json:"min"`
	MinDate  string   `json:"min_date"`
	TrendPct *float64 `json:"trend_pct,omitempty"` // mean of second half vs first half
}

// Timeline is a daily or monthly series.
type Timeline struct {
	Period      Period          `json:"period"`
	Granularity string          `json:"granularity"`
	VideoID     string          `json:"video_id,omitempty"`
	Metrics     []string        `json:"metrics"`
	Rows        []TimelineRow   `json:"rows"`
	Summary     []SeriesSummary `json:"summary"`
	Reach       *Coverage       `json:"reach,omitempty"`
}

// DefaultTimelineMetrics are used when none are requested.
var DefaultTimelineMetrics = []string{"views", "estimatedMinutesWatched", "subscribersGained", "subscribersLost"}

// Timeline returns a day or month series. Daily series also carry
// impressions and CTR from the reach cache when available.
func (s *Service) Timeline(ctx context.Context, spec PeriodSpec, videoID, granularity string, metrics []string) (*Timeline, error) {
	cur, _, err := s.periodFor(ctx, spec, videoID)
	if err != nil {
		return nil, err
	}
	if granularity == "" {
		granularity = "day"
		if cur.Days() > 400 {
			granularity = "month"
		}
	}
	if granularity != "day" && granularity != "month" {
		return nil, fmt.Errorf("granularity must be day or month")
	}
	if len(metrics) == 0 {
		metrics = DefaultTimelineMetrics
	}
	q := youtube.Query{Start: cur.Start, End: cur.End, Metrics: metrics, Dimensions: []string{granularity}, Filters: filterVideo(videoID), Sort: granularity}
	if granularity == "month" {
		// The month dimension needs both dates on the first of a month.
		q.Start, q.End = cur.Start[:8]+"01", cur.End[:8]+"01"
		cur.Start = q.Start
		if end := lastOfMonth(q.End); end < cur.End {
			cur.End = end
		}
	}
	out := &Timeline{Period: cur, Granularity: granularity, VideoID: videoID, Metrics: metrics}
	var t *youtube.Table
	var qErr error
	var rows []reach.Row
	var cov Coverage
	var created time.Time
	fns := []func(){
		func() { t, qErr = s.yt.Query(ctx, q) },
		func() { created = s.createdAt(ctx, videoID) },
	}
	if granularity == "day" {
		fns = append(fns, func() { rows, cov = s.reachRows(ctx, reach.Basic, cur) })
	}
	parallel(fns...)
	if qErr != nil {
		return nil, qErr
	}
	var byDay map[string]*reach.Agg
	if cov.Available {
		byDay = reach.GroupBy(rows, func(r reach.Row) string {
			if videoID != "" && r.VideoID != videoID {
				return ""
			}
			return r.Date
		})
		out.Metrics = append(append([]string{}, metrics...), "impressions", "ctr")
	}
	if granularity == "day" {
		out.Reach = &cov
	}
	// Drop the empty stretch before the channel or video existed.
	from := ""
	if !created.IsZero() {
		day := created.In(youtube.Pacific).Format(dateLayout)
		if day > out.Period.Start && day <= out.Period.End {
			out.Period.Start = day
		}
		from = day
		if granularity == "month" {
			from = day[:7]
		}
	}
	for _, r := range t.Rows {
		if t.Str(r, granularity) < from {
			continue
		}
		row := TimelineRow{Date: t.Str(r, granularity), Values: map[string]float64{}}
		for _, m := range metrics {
			row.Values[m] = t.Num(r, m)
		}
		if a, ok := byDay[row.Date]; ok {
			row.Values["impressions"], row.Values["ctr"] = a.Impressions, a.CTR()
		}
		out.Rows = append(out.Rows, row)
	}
	for _, m := range out.Metrics {
		out.Summary = append(out.Summary, summarize(out.Rows, m))
	}
	return out, nil
}

// createdAt is the publish time of the video, or the channel's creation
// time; zero when it cannot be read.
func (s *Service) createdAt(ctx context.Context, videoID string) time.Time {
	if videoID != "" {
		if v, err := s.yt.Video(ctx, videoID); err == nil {
			return v.PublishedAt
		}
		return time.Time{}
	}
	if ch, err := s.yt.MyChannel(ctx); err == nil {
		return ch.PublishedAt
	}
	return time.Time{}
}

func summarize(rows []TimelineRow, m string) SeriesSummary {
	s := SeriesSummary{Metric: m}
	var vals []float64
	var dates []string
	for _, r := range rows {
		v, ok := r.Values[m]
		if !ok {
			continue
		}
		vals = append(vals, v)
		dates = append(dates, r.Date)
	}
	if len(vals) == 0 {
		return s
	}
	var sum float64
	s.Max, s.Min, s.MaxDate, s.MinDate = vals[0], vals[0], dates[0], dates[0]
	for i, v := range vals {
		sum += v
		if v > s.Max {
			s.Max, s.MaxDate = v, dates[i]
		}
		if v < s.Min {
			s.Min, s.MinDate = v, dates[i]
		}
	}
	s.Mean = sum / float64(len(vals))
	if Additive(m) {
		s.Total = fptr(sum)
	}
	if h := len(vals) / 2; h >= 2 {
		s.TrendPct = change(mean(vals[len(vals)-h:]), mean(vals[:h]))
	}
	return s
}

// Additive reports whether summing a metric across days is meaningful.
func Additive(m string) bool {
	if strings.HasPrefix(m, "average") || strings.Contains(strings.ToLower(m), "cpm") {
		return false
	}
	switch m {
	case "ctr", "viewerPercentage", "audienceWatchRatio", "relativeRetentionPerformance", "cardClickRate", "cardTeaserClickRate",
		"annotationClickThroughRate", "annotationCloseRate", "averageConcurrentViewers", "peakConcurrentViewers":
		return false
	}
	return true
}

func mean(v []float64) float64 {
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func lastOfMonth(firstOfMonth string) string {
	t, err := time.Parse(dateLayout, firstOfMonth)
	if err != nil {
		return firstOfMonth
	}
	return t.AddDate(0, 1, -1).Format(dateLayout)
}
