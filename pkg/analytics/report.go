package analytics

import (
	"context"
	"strings"

	"github.com/gomcpgo/youtube_analytics/pkg/reach"
	"github.com/gomcpgo/youtube_analytics/pkg/youtube"
)

// Launch summarizes the daily curve of a video.
type Launch struct {
	FirstDayViews   float64 `json:"first_day_views"`
	First7DaysViews float64 `json:"first_7_days_views"`
	Last7DaysViews  float64 `json:"last_7_days_views"`
	PeakDate        string  `json:"peak_date"`
	PeakViews       float64 `json:"peak_views"`
	DaysWithData    int     `json:"days_with_data"`
	// FromPublish is true when the series starts on the publish date, so
	// the first day and first 7 days describe the launch.
	FromPublish bool `json:"from_publish"`
}

// Benchmark is the channel's typical performance for comparison.
type Benchmark struct {
	Period             Period   `json:"period"`
	Format             string   `json:"format"`
	AvgViewPct         float64  `json:"avg_view_percentage"`
	AvgViewDurationSec float64  `json:"avg_view_duration_seconds"`
	CTR                *float64 `json:"ctr_pct,omitempty"`
}

// VideoReach is a video's impressions funnel.
type VideoReach struct {
	Impressions float64       `json:"impressions"`
	CTR         float64       `json:"ctr_pct"`
	BySource    []ReachSource `json:"by_source,omitempty"`
}

// VideoReport is a one-call deep dive into a single video.
type VideoReport struct {
	Video       *youtube.Video    `json:"video"`
	Period      Period            `json:"period"`
	Stats       *VideoStat        `json:"stats,omitempty"`
	Launch      *Launch           `json:"launch,omitempty"`
	Traffic     []SourceRow       `json:"traffic_sources,omitempty"`
	SearchTerms []DetailRow       `json:"search_terms,omitempty"`
	Subscribed  *Breakdown        `json:"subscribed_vs_not,omitempty"`
	Retention   *Retention        `json:"retention,omitempty"`
	Reach       *VideoReach       `json:"reach,omitempty"`
	Coverage    Coverage          `json:"reach_coverage"`
	Benchmark   *Benchmark        `json:"channel_benchmark,omitempty"`
	Daily       []TimelineRow     `json:"daily,omitempty"`
	Errors      map[string]string `json:"errors,omitempty"`
}

// VideoReport gathers stats, launch curve, traffic, search terms, audience,
// retention, impressions/CTR and a channel benchmark for one video. The
// default period is since publish.
func (s *Service) VideoReport(ctx context.Context, videoID string, spec PeriodSpec) (*VideoReport, error) {
	v, err := s.yt.Video(ctx, videoID)
	if err != nil {
		return nil, err
	}
	if spec.Preset == "" && spec.Start == "" && spec.End == "" {
		spec.Preset = "lifetime"
	}
	p, _, err := s.periodFor(ctx, spec, videoID)
	if err != nil {
		return nil, err
	}
	r := &VideoReport{Video: v, Period: p}
	var e errs
	fmtType := ""
	parallel(
		func() {
			st, err := s.videoStats(ctx, p, PerformanceOptions{VideoIDs: []string{videoID}})
			if err == nil && len(st) > 0 {
				r.Stats = &st[0]
				fmtType = st[0].Format
			}
			e.set("stats", err)
		},
		func() {
			t, err := s.yt.Query(ctx, youtube.Query{Start: p.Start, End: p.End, Dimensions: []string{"day"}, Sort: "day",
				Metrics: []string{"views", "estimatedMinutesWatched", "subscribersGained"}, Filters: filterVideo(videoID)})
			if err != nil {
				e.set("daily", err)
				return
			}
			for _, row := range t.Rows {
				r.Daily = append(r.Daily, TimelineRow{Date: t.Str(row, "day"), Values: map[string]float64{
					"views": t.Num(row, "views"), "estimatedMinutesWatched": t.Num(row, "estimatedMinutesWatched"), "subscribersGained": t.Num(row, "subscribersGained")}})
			}
			r.Launch = launch(r.Daily)
			if r.Launch != nil {
				r.Launch.FromPublish = p.Start == v.PublishedAt.In(youtube.Pacific).Format(dateLayout)
			}
		},
		func() {
			t, err := s.yt.Query(ctx, youtube.Query{Start: p.Start, End: p.End, Metrics: []string{"views", "estimatedMinutesWatched"},
				Dimensions: []string{"insightTrafficSourceType"}, Filters: filterVideo(videoID), Sort: "-views"})
			if err == nil {
				r.Traffic = sourceRows(t, nil, nil)
			}
			e.set("traffic", err)
		},
		func() {
			d, err := s.TrafficDetailFor(ctx, PeriodSpec{Start: p.Start, End: p.End}, videoID, "YT_SEARCH", 10)
			if err == nil {
				r.SearchTerms = d.Rows
			}
			e.set("search terms", err)
		},
		func() {
			b := s.breakdown(ctx, p, videoID, "subscription", breakdownSpecs["subscription"])
			if b.Error != "" {
				e.set("subscribed", errString(b.Error))
				return
			}
			r.Subscribed = &b
		},
		func() {
			ret, err := s.Retention(ctx, videoID, RetentionOptions{})
			if err == nil {
				ret.Points = nil // summary only; video_retention returns the curve
				r.Retention = ret
			}
			e.set("retention", err)
		},
		func() {
			rows, cov := s.reachRows(ctx, reach.Combined, p)
			r.Coverage = cov
			if !cov.Available {
				return
			}
			tot := reach.Total(rows, videoID)
			if tot.Impressions > 0 {
				r.Reach = &VideoReach{Impressions: tot.Impressions, CTR: tot.CTR(), BySource: reachBySource(rows, videoID)}
			}
		},
	)
	r.Benchmark = s.benchmark(ctx, fmtType)
	r.Errors = e.m
	return r, nil
}

// benchmark is the channel's last-90-day average for the video's format.
func (s *Service) benchmark(ctx context.Context, format string) *Benchmark {
	p, _, err := ResolvePeriod(PeriodSpec{Preset: "90d"}, s.now(), s.now())
	if err != nil {
		return nil
	}
	b := &Benchmark{Period: p, Format: "all formats"}
	var t *youtube.Table
	var qErr error
	var rows []reach.Row
	var cov Coverage
	parallel(
		func() {
			t, qErr = s.query(ctx, youtube.Query{Start: p.Start, End: p.End, Metrics: formatMetrics, Dimensions: []string{"creatorContentType"}}, "engagedViews")
		},
		func() { rows, cov = s.reachRows(ctx, reach.Basic, p) },
	)
	if qErr != nil {
		return nil
	}
	var views, dur float64
	for _, row := range t.Rows {
		if format != "" && !strings.Contains(format, Label("creatorContentType", t.Str(row, "creatorContentType"))) {
			continue
		}
		v := t.Num(row, "views")
		views += v
		dur += t.Num(row, "averageViewDuration") * v
		b.AvgViewPct += t.Num(row, "averageViewPercentage") * v
	}
	if views == 0 {
		return nil
	}
	if format != "" {
		b.Format = format
	}
	b.AvgViewPct /= views
	b.AvgViewDurationSec = dur / views
	if cov.Available {
		b.CTR = fptr(reach.Total(rows, "").CTR())
	}
	return b
}

func launch(daily []TimelineRow) *Launch {
	if len(daily) == 0 {
		return nil
	}
	l := &Launch{DaysWithData: len(daily)}
	for i, d := range daily {
		v := d.Values["views"]
		if i == 0 {
			l.FirstDayViews = v
		}
		if i < 7 {
			l.First7DaysViews += v
		}
		if i >= len(daily)-7 {
			l.Last7DaysViews += v
		}
		if v > l.PeakViews {
			l.PeakViews, l.PeakDate = v, d.Date
		}
	}
	return l
}

type errString string

func (e errString) Error() string { return string(e) }
