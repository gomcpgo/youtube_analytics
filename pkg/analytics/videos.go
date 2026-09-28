package analytics

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/gomcpgo/youtube_analytics/pkg/reach"
	"github.com/gomcpgo/youtube_analytics/pkg/youtube"
)

// VideoStat is one video's performance in a period.
type VideoStat struct {
	ID                 string   `json:"video_id"`
	Title              string   `json:"title"`
	Published          string   `json:"published,omitempty"`
	DurationSec        int      `json:"duration_seconds,omitempty"`
	Format             string   `json:"format,omitempty"`
	Views              float64  `json:"views"`
	EngagedViews       float64  `json:"engaged_views,omitempty"`
	WatchMinutes       float64  `json:"watch_minutes"`
	AvgViewDurationSec float64  `json:"avg_view_duration_seconds"`
	AvgViewPct         float64  `json:"avg_view_percentage"`
	SubsGained         float64  `json:"subscribers_gained"`
	Likes              float64  `json:"likes"`
	Comments           float64  `json:"comments"`
	Shares             float64  `json:"shares"`
	EngagementRate     float64  `json:"engagement_rate_pct"`
	SubsPer1K          float64  `json:"subscribers_per_1k_views"`
	Impressions        *float64 `json:"impressions,omitempty"`
	CTR                *float64 `json:"ctr_pct,omitempty"`
}

// PerformanceOptions select and order videos.
type PerformanceOptions struct {
	// Sort: views, watch_time, subscribers, ctr, impressions,
	// avg_view_percentage, avg_view_duration, engagement, likes, comments, shares.
	Sort        string
	Limit       int
	VideoIDs    []string
	ContentType string // creatorContentType value, "" for all
}

// Performance is a per-video table for a period.
type Performance struct {
	Period Period      `json:"period"`
	Sort   string      `json:"sort"`
	Videos []VideoStat `json:"videos"`
	Reach  Coverage    `json:"reach"`
}

// SortKeys lists the accepted PerformanceOptions.Sort values.
var SortKeys = []string{"views", "watch_time", "subscribers", "ctr", "impressions", "avg_view_percentage", "avg_view_duration", "engagement", "likes", "comments", "shares"}

var videoMetrics = []string{"views", "engagedViews", "estimatedMinutesWatched", "averageViewDuration", "averageViewPercentage",
	"subscribersGained", "likes", "comments", "shares"}

// minImpressionsForCTR is the volume below which a CTR ranking is noise.
const minImpressionsForCTR = 100

var apiSorts = map[string]string{"views": "-views", "watch_time": "-estimatedMinutesWatched", "subscribers": "-subscribersGained"}

// VideoPerformance ranks videos by a metric, or reports the given videos.
func (s *Service) VideoPerformance(ctx context.Context, spec PeriodSpec, opt PerformanceOptions) (*Performance, error) {
	cur, err := s.resolve(ctx, spec)
	if err != nil {
		return nil, err
	}
	if opt.Sort == "" {
		opt.Sort = "views"
	}
	if !validSort(opt.Sort) {
		return nil, fmt.Errorf("unknown sort %q (use one of %s)", opt.Sort, strings.Join(SortKeys, ", "))
	}
	stats, err := s.videoStats(ctx, cur, opt)
	if err != nil {
		return nil, err
	}
	rows, cov := s.reachRows(ctx, reach.Basic, cur)
	if cov.Available {
		byVideo := reach.GroupBy(rows, func(r reach.Row) string { return r.VideoID })
		for i := range stats {
			if a, ok := byVideo[stats[i].ID]; ok {
				stats[i].Impressions, stats[i].CTR = fptr(a.Impressions), fptr(a.CTR())
			}
		}
	}
	sortStats(stats, opt.Sort)
	if opt.Limit > 0 && len(stats) > opt.Limit {
		stats = stats[:opt.Limit]
	}
	return &Performance{Period: cur, Sort: opt.Sort, Videos: stats, Reach: cov}, nil
}

// resolve resolves a spec; lifetime needs the channel's creation date.
func (s *Service) resolve(ctx context.Context, spec PeriodSpec) (Period, error) {
	p, _, err := s.resolveWithPrev(ctx, spec)
	return p, err
}

func (s *Service) resolveWithPrev(ctx context.Context, spec PeriodSpec) (Period, *Period, error) {
	switch strings.ToLower(strings.TrimSpace(spec.Preset)) {
	case "lifetime", "all", "max":
		if spec.Start == "" && spec.End == "" {
			ch, err := s.yt.MyChannel(ctx)
			if err != nil {
				return Period{}, nil, err
			}
			return ResolvePeriod(spec, s.now(), ch.PublishedAt)
		}
	}
	return ResolvePeriod(spec, s.now(), s.now())
}

// videoStats queries per-video metrics and joins titles and formats.
func (s *Service) videoStats(ctx context.Context, p Period, opt PerformanceOptions) ([]VideoStat, error) {
	sortExpr, max := apiSorts[opt.Sort], opt.Limit
	if sortExpr == "" || opt.ContentType != "" || max <= 0 || max > 200 {
		if sortExpr == "" {
			sortExpr = "-views"
		}
		max = 200
	}
	q := youtube.Query{Start: p.Start, End: p.End, Metrics: videoMetrics, Dimensions: []string{"video", "creatorContentType"}, Sort: sortExpr, MaxResults: max}
	if len(opt.VideoIDs) > 0 {
		q.Filters = []string{"video==" + strings.Join(opt.VideoIDs, ",")}
		q.MaxResults = 200
	}
	t, err := s.query(ctx, q, "engagedViews")
	if err != nil && youtube.IsBadRequest(err) {
		q.Dimensions = []string{"video"}
		t, err = s.query(ctx, q, "engagedViews")
	}
	var stats []VideoStat
	switch {
	case err != nil && len(opt.VideoIDs) > 0 && youtube.IsBadRequest(err):
		if stats, err = s.perVideoStats(ctx, p, opt.VideoIDs); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		stats = mergeVideoRows(t)
	}
	if opt.ContentType != "" {
		want := Label("creatorContentType", opt.ContentType)
		kept := stats[:0]
		for _, v := range stats {
			if v.Format == want {
				kept = append(kept, v)
			}
		}
		stats = kept
	}
	return stats, s.enrich(ctx, stats)
}

// mergeVideoRows folds rows (one per video and format) into VideoStats.
func mergeVideoRows(t *youtube.Table) []VideoStat {
	idx := map[string]int{}
	var out []VideoStat
	for _, r := range t.Rows {
		id := t.Str(r, "video")
		v := VideoStat{
			ID: id, Format: Label("creatorContentType", t.Str(r, "creatorContentType")),
			Views: t.Num(r, "views"), EngagedViews: t.Num(r, "engagedViews"), WatchMinutes: t.Num(r, "estimatedMinutesWatched"),
			AvgViewDurationSec: t.Num(r, "averageViewDuration"), AvgViewPct: t.Num(r, "averageViewPercentage"),
			SubsGained: t.Num(r, "subscribersGained"), Likes: t.Num(r, "likes"), Comments: t.Num(r, "comments"), Shares: t.Num(r, "shares"),
		}
		i, seen := idx[id]
		if !seen {
			idx[id] = len(out)
			out = append(out, v)
			continue
		}
		o := &out[i]
		total := o.Views + v.Views
		if total > 0 {
			o.AvgViewDurationSec = (o.AvgViewDurationSec*o.Views + v.AvgViewDurationSec*v.Views) / total
			o.AvgViewPct = (o.AvgViewPct*o.Views + v.AvgViewPct*v.Views) / total
		}
		if v.Views > o.Views/2 && v.Format != o.Format {
			o.Format += "/" + v.Format
		}
		o.Views, o.EngagedViews, o.WatchMinutes = total, o.EngagedViews+v.EngagedViews, o.WatchMinutes+v.WatchMinutes
		o.SubsGained, o.Likes, o.Comments, o.Shares = o.SubsGained+v.SubsGained, o.Likes+v.Likes, o.Comments+v.Comments, o.Shares+v.Shares
	}
	return out
}

// perVideoStats is the fallback when a multi-video filter is rejected.
func (s *Service) perVideoStats(ctx context.Context, p Period, ids []string) ([]VideoStat, error) {
	out := make([]VideoStat, len(ids))
	errs := make([]error, len(ids))
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			t, err := s.query(ctx, youtube.Query{Start: p.Start, End: p.End, Metrics: videoMetrics, Filters: filterVideo(id)}, "engagedViews", "averageViewPercentage")
			if err != nil {
				errs[i] = err
				return
			}
			out[i] = VideoStat{ID: id}
			if len(t.Rows) > 0 {
				r := t.Rows[0]
				out[i] = VideoStat{ID: id, Views: t.Num(r, "views"), EngagedViews: t.Num(r, "engagedViews"), WatchMinutes: t.Num(r, "estimatedMinutesWatched"),
					AvgViewDurationSec: t.Num(r, "averageViewDuration"), AvgViewPct: t.Num(r, "averageViewPercentage"), SubsGained: t.Num(r, "subscribersGained"),
					Likes: t.Num(r, "likes"), Comments: t.Num(r, "comments"), Shares: t.Num(r, "shares")}
			}
		}(i, id)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// enrich adds titles, publish dates, durations and derived rates.
func (s *Service) enrich(ctx context.Context, stats []VideoStat) error {
	ids := make([]string, len(stats))
	for i, v := range stats {
		ids[i] = v.ID
	}
	meta, err := s.yt.Videos(ctx, ids)
	if err != nil {
		return fmt.Errorf("loading video titles: %w", err)
	}
	for i := range stats {
		v := &stats[i]
		if m, ok := meta[v.ID]; ok {
			v.Title, v.DurationSec = m.Title, m.DurationSec
			v.Published = m.PublishedAt.In(youtube.Pacific).Format(dateLayout)
		} else {
			v.Title = "(deleted or unavailable)"
		}
		v.EngagementRate = pct(v.Likes+v.Comments+v.Shares, v.Views)
		v.SubsPer1K = per1k(v.SubsGained, v.Views)
	}
	return nil
}

func validSort(s string) bool {
	for _, k := range SortKeys {
		if k == s {
			return true
		}
	}
	return false
}

func sortStats(v []VideoStat, key string) {
	val := func(x VideoStat) float64 {
		switch key {
		case "watch_time":
			return x.WatchMinutes
		case "subscribers":
			return x.SubsGained
		case "ctr":
			// Rank videos with too few impressions for a stable CTR last.
			if x.CTR == nil {
				return -2
			}
			if *x.Impressions < minImpressionsForCTR {
				return -1 + *x.CTR/1000
			}
			return *x.CTR
		case "impressions":
			if x.Impressions == nil {
				return -1
			}
			return *x.Impressions
		case "avg_view_percentage":
			return x.AvgViewPct
		case "avg_view_duration":
			return x.AvgViewDurationSec
		case "engagement":
			return x.EngagementRate
		case "likes":
			return x.Likes
		case "comments":
			return x.Comments
		case "shares":
			return x.Shares
		}
		return x.Views
	}
	sort.SliceStable(v, func(i, j int) bool { return val(v[i]) > val(v[j]) })
}

// VideoList is the channel's uploads with public lifetime counters.
type VideoList struct {
	Channel *youtube.Channel `json:"channel"`
	Videos  []*youtube.Video `json:"videos"`
	Scanned int              `json:"scanned"`
}

// ListVideos returns recent uploads, newest first, optionally filtered by a
// case-insensitive title substring (which scans up to 500 uploads).
func (s *Service) ListVideos(ctx context.Context, limit int, titleQuery string) (*VideoList, error) {
	ch, err := s.yt.MyChannel(ctx)
	if err != nil {
		return nil, err
	}
	scan := limit
	if titleQuery != "" {
		scan = 500
	}
	ids, err := s.yt.Uploads(ctx, ch.UploadsPlaylist, scan)
	if err != nil {
		return nil, err
	}
	meta, err := s.yt.Videos(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := &VideoList{Channel: ch, Scanned: len(ids)}
	q := strings.ToLower(strings.TrimSpace(titleQuery))
	for _, id := range ids {
		v, ok := meta[id]
		if !ok || q != "" && !strings.Contains(strings.ToLower(v.Title), q) {
			continue
		}
		out.Videos = append(out.Videos, v)
		if len(out.Videos) >= limit {
			break
		}
	}
	return out, nil
}
