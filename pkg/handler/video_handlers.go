package handler

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/gomcpgo/mcp/pkg/protocol"
	"github.com/gomcpgo/youtube_analytics/pkg/analytics"
	"github.com/gomcpgo/youtube_analytics/pkg/youtube"
)

func (h *Handler) videoPerformance(ctx context.Context, a args) (*protocol.CallToolResponse, error) {
	svc, _, err := h.session(a)
	if err != nil {
		return nil, err
	}
	ct, ok := analytics.ContentTypeFilter(a.str("content_type"))
	if !ok {
		return nil, fmt.Errorf("content_type must be all, shorts, videos or live")
	}
	var ids []string
	for _, s := range a.strs("video_ids") {
		id, err := youtube.ParseVideoID(s)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if len(ids) > 50 {
		return nil, fmt.Errorf("at most 50 video_ids")
	}
	p, err := svc.VideoPerformance(ctx, a.period(), analytics.PerformanceOptions{
		Sort: a.str("sort"), Limit: a.intv("limit", 25, 1, 200), VideoIDs: ids, ContentType: ct,
	})
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d videos, %s, sorted by %s.\n\n", len(p.Videos), periodLine(p.Period), p.Sort)
	if len(p.Videos) == 0 {
		b.WriteString("No videos had views matching these filters in this period.\n")
		return textResponse(b.String()), nil
	}
	var rows [][]string
	for i, v := range p.Videos {
		rows = append(rows, []string{fmt.Sprint(i + 1), truncate(v.Title, 55), v.ID, v.Published, v.Format, count(v.Views), hours(v.WatchMinutes),
			clock(v.AvgViewDurationSec), percent(v.AvgViewPct), optCount(v.Impressions), optPct(v.CTR), count(v.SubsGained),
			strings.TrimSuffix(fmt.Sprintf("%.2f", v.SubsPer1K), ".00"), percent(v.EngagementRate)})
	}
	b.WriteString(table([]string{"#", "Title", "ID", "Published", "Format", "Views", "Watch time", "Avg duration", "Avg % viewed", "Impressions", "CTR", "Subs gained", "Subs/1K views", "Engagement"}, rows))
	b.WriteString("\n" + p.Reach.Note + "\n")
	return textResponse(b.String()), nil
}

func (h *Handler) videoReport(ctx context.Context, a args) (*protocol.CallToolResponse, error) {
	svc, _, err := h.session(a)
	if err != nil {
		return nil, err
	}
	vid, err := a.videoID(true)
	if err != nil {
		return nil, err
	}
	r, err := svc.VideoReport(ctx, vid, a.period())
	if err != nil {
		return nil, err
	}
	v := r.Video
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", v.Title)
	fmt.Fprintf(&b, "ID %s · published %s · length %s · %s", v.ID, v.PublishedAt.In(youtube.Pacific).Format("2006-01-02 15:04 MST"), clock(float64(v.DurationSec)), v.Privacy)
	if len(v.Tags) > 0 {
		fmt.Fprintf(&b, " · %d tags", len(v.Tags))
	}
	fmt.Fprintf(&b, "\nLifetime public counts: %s views, %s likes, %s comments.\n", count(float64(v.Views)), count(float64(v.Likes)), count(float64(v.Comments)))
	fmt.Fprintf(&b, "Period: %s.\n", periodLine(r.Period))

	if s := r.Stats; s != nil {
		b.WriteString("\n## Key metrics\n")
		rows := [][]string{
			{"Views", count(s.Views)}, {"Engaged views", count(s.EngagedViews)}, {"Watch time", hours(s.WatchMinutes)},
			{"Avg view duration", clock(s.AvgViewDurationSec)}, {"Avg % viewed", percent(s.AvgViewPct)},
			{"Subscribers gained", count(s.SubsGained)}, {"Subscribers per 1K views", fmt.Sprintf("%.2f", s.SubsPer1K)},
			{"Likes / comments / shares", fmt.Sprintf("%s / %s / %s", count(s.Likes), count(s.Comments), count(s.Shares))},
			{"Engagement rate", percent(s.EngagementRate)}, {"Format", s.Format},
		}
		if r.Reach != nil {
			rows = append(rows, []string{"Impressions", count(math.Round(r.Reach.Impressions))}, []string{"Impressions CTR", percent(r.Reach.CTR)})
		}
		b.WriteString(table([]string{"Metric", "Value"}, rows))
	}
	if bm := r.Benchmark; bm != nil {
		fmt.Fprintf(&b, "\nChannel benchmark (%s, %s to %s): avg %% viewed %s, avg duration %s", bm.Format, bm.Period.Start, bm.Period.End, percent(bm.AvgViewPct), clock(bm.AvgViewDurationSec))
		if bm.CTR != nil {
			fmt.Fprintf(&b, ", CTR %s", percent(*bm.CTR))
		}
		b.WriteString(".\n")
	}
	if l := r.Launch; l != nil && l.FromPublish {
		fmt.Fprintf(&b, "\n## Launch curve\nFirst day %s views; first 7 days %s; peak %s on %s; last 7 days %s (%d days of data).\n",
			count(l.FirstDayViews), count(l.First7DaysViews), count(l.PeakViews), l.PeakDate, count(l.Last7DaysViews), l.DaysWithData)
	} else if l != nil {
		fmt.Fprintf(&b, "\n## Daily views\nPeak %s on %s; last 7 days %s (%d days in the period). Use period lifetime for the launch curve.\n",
			count(l.PeakViews), l.PeakDate, count(l.Last7DaysViews), l.DaysWithData)
	}
	if len(r.Traffic) > 0 {
		b.WriteString("\n## Traffic sources\n")
		b.WriteString(sourceTable(r.Traffic, false))
	}
	if r.Reach != nil && len(r.Reach.BySource) > 0 {
		b.WriteString("\n## Impressions and CTR by source\n")
		b.WriteString(reachSourceTable(r.Reach.BySource))
	}
	if len(r.SearchTerms) == 0 && searchViews(r.Traffic) > 0 {
		fmt.Fprintf(&b, "\n## Top YouTube search terms\nYouTube withheld the individual terms behind the %s search views (each term had too few views to report).\n", count(searchViews(r.Traffic)))
	}
	if len(r.SearchTerms) > 0 {
		b.WriteString("\n## Top YouTube search terms\n")
		var rows [][]string
		for _, d := range r.SearchTerms {
			rows = append(rows, []string{d.Detail, count(d.Views), hours(d.WatchMinutes)})
		}
		b.WriteString(table([]string{"Search term", "Views", "Watch time"}, rows))
	}
	if r.Subscribed != nil {
		b.WriteString("\n## Subscribers vs non-subscribers\n")
		b.WriteString(breakdownTable(*r.Subscribed))
	}
	if rt := r.Retention; rt != nil {
		b.WriteString("\n## Retention summary\n")
		b.WriteString(retentionSummary(rt))
		b.WriteString("Call video_retention for the full curve.\n")
	}
	b.WriteString("\n" + r.Coverage.Note + "\n")
	b.WriteString(errorsSection(r.Errors))
	return textResponse(b.String()), nil
}

func (h *Handler) videoRetention(ctx context.Context, a args) (*protocol.CallToolResponse, error) {
	svc, _, err := h.session(a)
	if err != nil {
		return nil, err
	}
	vid, err := a.videoID(true)
	if err != nil {
		return nil, err
	}
	at, ok := analytics.AudienceTypeFilter(a.str("audience_type"))
	if !ok {
		return nil, fmt.Errorf("audience_type must be all, organic, ad_instream or ad_indisplay")
	}
	ss, ok := analytics.SubscribedFilter(a.str("subscribed_status"))
	if !ok {
		return nil, fmt.Errorf("subscribed_status must be all, subscribed or unsubscribed")
	}
	r, err := svc.Retention(ctx, vid, analytics.RetentionOptions{AudienceType: at, SubscribedStatus: ss})
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Retention: %s\n%s · length %s · %s", r.Title, r.VideoID, clock(r.DurationSec), periodLine(r.Period))
	for k, v := range r.Filters {
		fmt.Fprintf(&b, " · %s=%s", k, v)
	}
	b.WriteString("\n\n")
	b.WriteString(retentionSummary(r))
	if len(r.WeakSpans) > 0 {
		b.WriteString("\nBelow typical for similar-length videos: " + spanList(r.WeakSpans, false) + "\n")
	}
	if len(r.StrongSpans) > 0 {
		b.WriteString("Above typical: " + spanList(r.StrongSpans, false) + "\n")
	}

	step := 100 / a.intv("points", 20, 10, 100)
	b.WriteString("\n## Curve\n")
	var rows [][]string
	for i, p := range r.Points {
		if (i+1)%step != 0 && i != 0 {
			continue
		}
		rows = append(rows, []string{clock(p.Second), fmt.Sprintf("%.0f%%", p.Ratio*100), percent(p.Watch * 100), fmt.Sprintf("%.2f", p.Relative)})
	}
	b.WriteString(table([]string{"Time", "Position", "Still watching", "Relative (0-1, 0.5 = typical)"}, rows))
	b.WriteString("\n'Still watching' is audienceWatchRatio: values above 100% mean parts were rewatched. Relative retention compares with all YouTube videos of similar length.\n")
	return textResponse(b.String()), nil
}

func searchViews(rows []analytics.SourceRow) float64 {
	for _, r := range rows {
		if r.Source == "YT_SEARCH" {
			return r.Views
		}
	}
	return 0
}

func retentionSummary(r *analytics.Retention) string {
	var b strings.Builder
	if r.Smoothed {
		fmt.Fprintf(&b, "- Few views (%s lifetime): the curve is noisy, so drops and spikes below use a 5-point moving average. Treat them as directional.\n", count(float64(r.Views)))
	}
	fmt.Fprintf(&b, "- Average percentage viewed ≈ %s; relative retention avg %.2f (0.5 = typical for this length)\n", percent(r.AvgWatchPct), r.RelativeAvg)
	checkpoints := fmt.Sprintf("- Retention: start %s", percent(r.StartPct))
	if r.At30sPct != nil {
		checkpoints += fmt.Sprintf(", 0:30 %s", percent(*r.At30sPct))
	}
	checkpoints += fmt.Sprintf(", 25%% %s, 50%% %s, 75%% %s, end %s\n", percent(r.At25Pct), percent(r.At50Pct), percent(r.At75Pct), percent(r.EndPct))
	b.WriteString(checkpoints)
	fmt.Fprintf(&b, "- Intro (0:00 to %s): %+.1f points\n", clock(r.Intro.ToSec), r.Intro.PointsPP)
	if len(r.Drops) > 0 {
		b.WriteString("- Sharpest drop-offs after the intro (in time order): " + spanList(r.Drops, true) + "\n")
	}
	if len(r.Rewatches) > 0 {
		b.WriteString("- Rewatch spikes, where viewers seek back or replay (in time order): " + spanList(r.Rewatches, true) + "\n")
	}
	if len(r.Exits) > 0 {
		var parts []string
		for _, e := range r.Exits {
			parts = append(parts, fmt.Sprintf("%s (%.1f%% of exits)", clock(e.Second), e.Value))
		}
		b.WriteString("- Most common exit points: " + strings.Join(parts, ", ") + "\n")
	}
	if !r.ExitData {
		b.WriteString("- Exit points: YouTube withholds exit counts for this video (too few views).\n")
	}
	switch {
	case r.OutroLossPP >= 0.05:
		fmt.Fprintf(&b, "- Outro (last 5%%): %.1f points lost\n", r.OutroLossPP)
	case r.OutroLossPP <= -0.05:
		fmt.Fprintf(&b, "- Outro (last 5%%): retention rose %.1f points\n", -r.OutroLossPP)
	default:
		b.WriteString("- Outro (last 5%): no loss\n")
	}
	return b.String()
}

func spanList(spans []analytics.Span, withChange bool) string {
	var parts []string
	for _, s := range spans {
		p := clock(s.FromSec) + "–" + clock(s.ToSec)
		if withChange {
			p += fmt.Sprintf(" (%s → %s, %+.1f pts)", percent(s.FromPct), percent(s.ToPct), s.PointsPP)
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "; ")
}

func sourceTable(rows []analytics.SourceRow, compare bool) string {
	var out [][]string
	for _, r := range rows {
		row := []string{r.Label, count(r.Views), percent(r.ViewShare), hours(r.WatchMinutes), clock(r.AvgViewDurationSec)}
		if compare {
			row = append(row, changeStr(r.ChangePct))
		}
		out = append(out, row)
	}
	headers := []string{"Source", "Views", "Share", "Watch time", "Avg duration"}
	if compare {
		headers = append(headers, "Views change")
	}
	return table(headers, out)
}

func reachSourceTable(rows []analytics.ReachSource) string {
	var out [][]string
	for _, r := range rows {
		out = append(out, []string{r.Source, count(math.Round(r.Impressions)), percent(r.ImpressionShare), percent(r.CTR)})
	}
	return table([]string{"Source", "Impressions", "Share", "CTR"}, out)
}

func breakdownTable(bd analytics.Breakdown) string {
	headers := []string{bd.Title}
	for _, c := range bd.Columns {
		headers = append(headers, metricLabel(c))
	}
	var rows [][]string
	for _, r := range bd.Rows {
		row := []string{r.Label}
		for _, c := range bd.Columns {
			row = append(row, metricValue(c, r.Values[c]))
		}
		rows = append(rows, row)
	}
	return table(headers, rows)
}
