package analytics

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/gomcpgo/youtube_analytics/pkg/reach"
	"github.com/gomcpgo/youtube_analytics/pkg/youtube"
)

// SourceRow is one traffic source's views in a period.
type SourceRow struct {
	Source             string   `json:"source"`
	Label              string   `json:"label"`
	Views              float64  `json:"views"`
	WatchMinutes       float64  `json:"watch_minutes"`
	AvgViewDurationSec float64  `json:"avg_view_duration_seconds"`
	ViewShare          float64  `json:"view_share_pct"`
	PrevViews          *float64 `json:"previous_views,omitempty"`
	ChangePct          *float64 `json:"views_change_pct,omitempty"`
}

// ReachSource is impressions and CTR for one traffic source.
type ReachSource struct {
	Source          string  `json:"source"`
	Impressions     float64 `json:"impressions"`
	ImpressionShare float64 `json:"impression_share_pct"`
	CTR             float64 `json:"ctr_pct"`
}

// Traffic is where views came from.
type Traffic struct {
	Period   Period        `json:"period"`
	Previous *Period       `json:"previous_period,omitempty"`
	VideoID  string        `json:"video_id,omitempty"`
	Sources  []SourceRow   `json:"sources"`
	Reach    []ReachSource `json:"impressions_by_source,omitempty"`
	Coverage Coverage      `json:"reach"`
}

// periodFor resolves a period; a video's "lifetime" starts at its publish date.
func (s *Service) periodFor(ctx context.Context, spec PeriodSpec, videoID string) (Period, *Period, error) {
	if videoID != "" && spec.Start == "" && spec.End == "" {
		switch strings.ToLower(spec.Preset) {
		case "lifetime", "all", "max":
			v, err := s.yt.Video(ctx, videoID)
			if err != nil {
				return Period{}, nil, err
			}
			return SincePublish(v.PublishedAt, s.now()), nil, nil
		}
	}
	return s.resolveWithPrev(ctx, spec)
}

// TrafficSources breaks views down by traffic source, with impressions and
// CTR per source from the reach reports.
func (s *Service) TrafficSources(ctx context.Context, spec PeriodSpec, videoID string, compare bool) (*Traffic, error) {
	cur, prev, err := s.periodFor(ctx, spec, videoID)
	if err != nil {
		return nil, err
	}
	if !compare {
		prev = nil
	}
	out := &Traffic{Period: cur, Previous: prev, VideoID: videoID}
	var curT, prevT *youtube.Table
	var curErr, prevErr error
	var rows []reach.Row
	q := func(p Period) youtube.Query {
		return youtube.Query{Start: p.Start, End: p.End, Metrics: []string{"views", "estimatedMinutesWatched"},
			Dimensions: []string{"insightTrafficSourceType"}, Filters: filterVideo(videoID), Sort: "-views"}
	}
	fns := []func(){
		func() { curT, curErr = s.yt.Query(ctx, q(cur)) },
		func() { rows, out.Coverage = s.reachRows(ctx, reach.Combined, cur) },
	}
	if prev != nil {
		fns = append(fns, func() { prevT, prevErr = s.yt.Query(ctx, q(*prev)) })
	}
	parallel(fns...)
	if curErr != nil {
		return nil, curErr
	}
	out.Sources = sourceRows(curT, prevT, prevErr)
	out.Reach = reachBySource(rows, videoID)
	return out, nil
}

func sourceRows(cur, prev *youtube.Table, prevErr error) []SourceRow {
	prevViews := map[string]float64{}
	if prev != nil && prevErr == nil {
		for _, r := range prev.Rows {
			prevViews[prev.Str(r, "insightTrafficSourceType")] = prev.Num(r, "views")
		}
	}
	var total float64
	for _, r := range cur.Rows {
		total += cur.Num(r, "views")
	}
	var out []SourceRow
	for _, r := range cur.Rows {
		src := cur.Str(r, "insightTrafficSourceType")
		row := SourceRow{Source: src, Label: Label("insightTrafficSourceType", src), Views: cur.Num(r, "views"), WatchMinutes: cur.Num(r, "estimatedMinutesWatched")}
		row.ViewShare = pct(row.Views, total)
		if row.Views > 0 {
			row.AvgViewDurationSec = row.WatchMinutes * 60 / row.Views
		}
		if prev != nil && prevErr == nil {
			p := prevViews[src]
			row.PrevViews, row.ChangePct = &p, change(row.Views, p)
		}
		out = append(out, row)
	}
	return out
}

func reachBySource(rows []reach.Row, videoID string) []ReachSource {
	if len(rows) == 0 {
		return nil
	}
	if videoID != "" {
		var kept []reach.Row
		for _, r := range rows {
			if r.VideoID == videoID {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	groups := reach.GroupBy(rows, func(r reach.Row) string { return reach.TrafficSourceName(r.TrafficSource) })
	total := reach.Total(rows, "")
	var out []ReachSource
	for name, a := range groups {
		out = append(out, ReachSource{Source: name, Impressions: a.Impressions, ImpressionShare: pct(a.Impressions, total.Impressions), CTR: a.CTR()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Impressions > out[j].Impressions })
	return out
}

// DetailRow is one entry of a traffic source's detail (search term, video, site).
type DetailRow struct {
	Detail       string  `json:"detail"`
	Title        string  `json:"title,omitempty"`
	Views        float64 `json:"views"`
	WatchMinutes float64 `json:"watch_minutes"`
}

// TrafficDetail is the top entries within one traffic source.
type TrafficDetail struct {
	Period      Period      `json:"period"`
	Source      string      `json:"source"`
	SourceLabel string      `json:"source_label"`
	VideoID     string      `json:"video_id,omitempty"`
	Rows        []DetailRow `json:"rows"`
}

// DetailSources are traffic sources the API can break down further.
var DetailSources = []string{"YT_SEARCH", "RELATED_VIDEO", "EXT_URL", "YT_CHANNEL", "SUBSCRIBER", "PLAYLIST", "YT_OTHER_PAGE", "SHORTS", "ADVERTISING", "HASHTAGS", "SOUND_PAGE", "YT_PLAYLIST_PAGE", "NO_LINK_OTHER"}

// TrafficDetailFor lists the top search terms, suggesting videos, external
// sites etc. for one traffic source (max 25, an API limit).
func (s *Service) TrafficDetailFor(ctx context.Context, spec PeriodSpec, videoID, source string, limit int) (*TrafficDetail, error) {
	source = strings.ToUpper(strings.TrimSpace(source))
	if !contains(DetailSources, source) {
		return nil, fmt.Errorf("detail is not available for %q; use one of %s", source, strings.Join(DetailSources, ", "))
	}
	cur, _, err := s.periodFor(ctx, spec, videoID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 25 {
		limit = 25
	}
	t, err := s.yt.Query(ctx, youtube.Query{
		Start: cur.Start, End: cur.End, Metrics: []string{"views", "estimatedMinutesWatched"},
		Dimensions: []string{"insightTrafficSourceDetail"}, Filters: append([]string{"insightTrafficSourceType==" + source}, filterVideo(videoID)...),
		Sort: "-views", MaxResults: limit,
	})
	if err != nil {
		return nil, err
	}
	out := &TrafficDetail{Period: cur, Source: source, SourceLabel: Label("insightTrafficSourceType", source), VideoID: videoID}
	var ids []string
	for _, r := range t.Rows {
		d := t.Str(r, "insightTrafficSourceDetail")
		out.Rows = append(out.Rows, DetailRow{Detail: d, Views: t.Num(r, "views"), WatchMinutes: t.Num(r, "estimatedMinutesWatched")})
		if id, err := youtube.ParseVideoID(d); err == nil && id == d {
			ids = append(ids, d)
		}
	}
	if len(ids) > 0 {
		if meta, err := s.yt.Videos(ctx, ids); err == nil {
			for i := range out.Rows {
				if m, ok := meta[out.Rows[i].Detail]; ok {
					out.Rows[i].Title = m.Title
				}
			}
		}
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
