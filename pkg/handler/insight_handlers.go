package handler

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/gomcpgo/mcp/pkg/protocol"
	"github.com/gomcpgo/youtube_analytics/pkg/analytics"
)

func (h *Handler) trafficSources(ctx context.Context, a args) (*protocol.CallToolResponse, error) {
	svc, _, err := h.session(a)
	if err != nil {
		return nil, err
	}
	vid, err := a.videoID(false)
	if err != nil {
		return nil, err
	}
	scope := "channel"
	if vid != "" {
		scope = "video " + vid
	}
	if src := a.str("detail_for"); src != "" {
		d, err := svc.TrafficDetailFor(ctx, a.period(), vid, src, a.intv("limit", 25, 1, 25))
		if err != nil {
			return nil, err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Top %s for the %s, %s.\n\n", d.SourceLabel, scope, periodLine(d.Period))
		if len(d.Rows) == 0 {
			b.WriteString("YouTube returned no detail rows: entries with very few views are withheld for privacy.\n")
			return textResponse(b.String()), nil
		}
		var rows [][]string
		for _, r := range d.Rows {
			detail := r.Detail
			if r.Title != "" {
				detail = truncate(r.Title, 60) + " (" + r.Detail + ")"
			}
			rows = append(rows, []string{detail, count(r.Views), hours(r.WatchMinutes)})
		}
		b.WriteString(table([]string{"Detail", "Views", "Watch time"}, rows))
		return textResponse(b.String()), nil
	}
	t, err := svc.TrafficSources(ctx, a.period(), vid, a.boolv("compare", true))
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Traffic sources for the %s, %s.\n\n", scope, periodLine(t.Period))
	b.WriteString(sourceTable(t.Sources, t.Previous != nil))
	if len(t.Reach) > 0 {
		b.WriteString("\n## Impressions and CTR by source\n")
		b.WriteString(reachSourceTable(t.Reach))
	}
	b.WriteString("\n" + t.Coverage.Note + "\nUse detail_for (e.g. YT_SEARCH, RELATED_VIDEO, EXT_URL) to drill into a source.\n")
	return textResponse(b.String()), nil
}

func (h *Handler) audience(ctx context.Context, a args) (*protocol.CallToolResponse, error) {
	svc, _, err := h.session(a)
	if err != nil {
		return nil, err
	}
	vid, err := a.videoID(false)
	if err != nil {
		return nil, err
	}
	au, err := svc.Audience(ctx, a.period(), vid, a.strs("breakdowns"))
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	scope := "channel"
	if vid != "" {
		scope = "video " + vid
	}
	fmt.Fprintf(&b, "Audience of the %s, %s.\n", scope, periodLine(au.Period))
	for _, bd := range au.Breakdowns {
		fmt.Fprintf(&b, "\n## %s\n", bd.Title)
		if bd.Error != "" {
			b.WriteString("Unavailable: " + bd.Error + "\n")
			continue
		}
		if len(bd.Rows) == 0 {
			b.WriteString("No data (YouTube withholds small audiences for privacy).\n")
			continue
		}
		b.WriteString(breakdownTable(bd))
		if bd.Note != "" {
			b.WriteString(bd.Note + "\n")
		}
	}
	return textResponse(b.String()), nil
}

func (h *Handler) timeline(ctx context.Context, a args) (*protocol.CallToolResponse, error) {
	svc, _, err := h.session(a)
	if err != nil {
		return nil, err
	}
	vid, err := a.videoID(false)
	if err != nil {
		return nil, err
	}
	t, err := svc.Timeline(ctx, a.period(), vid, a.str("granularity"), a.strs("metrics"))
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	scope := "channel"
	if vid != "" {
		scope = "video " + vid
	}
	fmt.Fprintf(&b, "%s series for the %s, %s: %d rows.\n\n", capitalize(t.Granularity), scope, periodLine(t.Period), len(t.Rows))

	var srows [][]string
	for _, s := range t.Summary {
		total := "—"
		if s.Total != nil {
			total = metricValue(s.Metric, *s.Total)
		}
		srows = append(srows, []string{metricLabel(s.Metric), total, metricValue(s.Metric, s.Mean),
			metricValue(s.Metric, s.Max) + " (" + s.MaxDate + ")", metricValue(s.Metric, s.Min) + " (" + s.MinDate + ")", changeStr(s.TrendPct)})
	}
	b.WriteString(table([]string{"Metric", "Total", "Mean per " + t.Granularity, "Max", "Min", "Trend (2nd half vs 1st)"}, srows))

	rows, note := t.Rows, ""
	if len(rows) > 120 {
		rows, note = weekly(t), "Shown as weekly totals (averages for non-additive metrics); ask for a shorter period to see single days.\n"
	}
	b.WriteString("\n" + note)
	headers := []string{"Date"}
	for _, m := range t.Metrics {
		headers = append(headers, metricLabel(m))
	}
	var out [][]string
	for _, r := range rows {
		row := []string{r.Date}
		for _, m := range t.Metrics {
			if v, ok := r.Values[m]; ok {
				row = append(row, metricValue(m, v))
			} else {
				row = append(row, "—")
			}
		}
		out = append(out, row)
	}
	b.WriteString(table(headers, out))
	if t.Reach != nil && !t.Reach.Available {
		b.WriteString("\n" + t.Reach.Note + "\n")
	}
	return textResponse(b.String()), nil
}

// weekly folds daily rows into 7-day buckets for display.
func weekly(t *analytics.Timeline) []analytics.TimelineRow {
	var out []analytics.TimelineRow
	for i := 0; i < len(t.Rows); i += 7 {
		end := min(i+7, len(t.Rows))
		row := analytics.TimelineRow{Date: t.Rows[i].Date + "+", Values: map[string]float64{}}
		for _, m := range t.Metrics {
			var sum, n float64
			for _, r := range t.Rows[i:end] {
				if v, ok := r.Values[m]; ok {
					sum += v
					n++
				}
			}
			if n == 0 {
				continue
			}
			if analytics.Additive(m) {
				row.Values[m] = sum
			} else {
				row.Values[m] = sum / n
			}
		}
		out = append(out, row)
	}
	return out
}

func (h *Handler) impressions(ctx context.Context, a args) (*protocol.CallToolResponse, error) {
	svc, _, err := h.session(a)
	if err != nil {
		return nil, err
	}
	vid, err := a.videoID(false)
	if err != nil {
		return nil, err
	}
	r, err := svc.Impressions(ctx, a.period(), a.str("group_by"), vid, a.intv("limit", 50, 1, 200), a.boolv("refresh", false))
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	scope := "channel"
	if vid != "" {
		scope = "video " + vid
	}
	fmt.Fprintf(&b, "Impressions and CTR for the %s by %s, %s.\n", scope, r.GroupBy, periodLine(r.Period))
	b.WriteString(r.Coverage.Note + "\n")
	if r.Downloaded > 0 {
		fmt.Fprintf(&b, "Downloaded %d new report files.\n", r.Downloaded)
	}
	if !r.Coverage.Available {
		return textResponse(b.String()), nil
	}
	fmt.Fprintf(&b, "\nTotal: %s impressions, CTR %s (about %s clicks).\n\n", count(math.Round(r.Impressions)), percent(r.CTR), count(math.Round(r.Impressions*r.CTR/100)))
	var rows [][]string
	for _, row := range r.Rows {
		label := row.Label
		if r.GroupBy == "video" {
			label = truncate(row.Label, 60)
		}
		cells := []string{label}
		if r.GroupBy == "video" {
			cells = append(cells, row.Key)
		}
		cells = append(cells, count(math.Round(row.Impressions)), percent(row.Share), percent(row.CTR), count(math.Round(row.Clicks)))
		rows = append(rows, cells)
	}
	headers := []string{groupHeader(r.GroupBy)}
	if r.GroupBy == "video" {
		headers = append(headers, "ID")
	}
	b.WriteString(table(append(headers, "Impressions", "Share", "CTR", "Clicks"), rows))
	return textResponse(b.String()), nil
}

func groupHeader(g string) string {
	switch g {
	case "video":
		return "Video"
	case "day":
		return "Date"
	case "traffic_source":
		return "Traffic source"
	}
	return "Device"
}
