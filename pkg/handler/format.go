package handler

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/gomcpgo/youtube_analytics/pkg/analytics"
)

// table renders a markdown table; cells must not contain newlines.
func table(headers []string, rows [][]string) string {
	var b strings.Builder
	b.WriteString("| " + strings.Join(headers, " | ") + " |\n|")
	for range headers {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, r := range rows {
		for i := range r {
			r[i] = strings.ReplaceAll(r[i], "|", "\\|")
		}
		b.WriteString("| " + strings.Join(r, " | ") + " |\n")
	}
	return b.String()
}

// count formats a number with thousands separators; non-integers keep 2 decimals.
func count(v float64) string {
	if v != math.Trunc(v) {
		return strconv.FormatFloat(v, 'f', 2, 64)
	}
	s := strconv.FormatInt(int64(math.Abs(v)), 10)
	var b strings.Builder
	if v < 0 {
		b.WriteByte('-')
	}
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// money formats with thousands separators and 2 decimals.
func money(v float64) string {
	r := math.Round(math.Abs(v) * 100)
	s := count(math.Trunc(r/100)) + fmt.Sprintf(".%02d", int(math.Mod(r, 100)))
	if v < 0 && r > 0 {
		return "-" + s
	}
	return s
}

func hours(minutes float64) string {
	h := minutes / 60
	if h >= 100 {
		return count(math.Round(h)) + " h"
	}
	return strconv.FormatFloat(h, 'f', 1, 64) + " h"
}

// clock formats seconds as m:ss or h:mm:ss.
func clock(sec float64) string {
	s := int(math.Round(sec))
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func percent(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) + "%" }

func changeStr(c *float64) string {
	if c == nil {
		return "—"
	}
	return fmt.Sprintf("%+.1f%%", *c)
}

func kindValue(k analytics.Kind, v float64) string {
	switch k {
	case analytics.KindMinutes:
		return hours(v)
	case analytics.KindSeconds:
		return clock(v)
	case analytics.KindPercent:
		return percent(v)
	case analytics.KindCurrency, analytics.KindPer1K:
		return money(v)
	}
	return count(math.Round(v*100) / 100)
}

// metricKind guesses the display kind of an Analytics API metric name.
func metricKind(m string) analytics.Kind {
	lm := strings.ToLower(m)
	switch {
	case strings.Contains(lm, "minuteswatched"):
		return analytics.KindMinutes
	case strings.Contains(lm, "averageviewduration") || lm == "averagetimeinplaylist" || lm == "avg_view_duration_seconds":
		return analytics.KindSeconds
	case strings.Contains(lm, "percentage") || strings.HasSuffix(lm, "rate") || lm == "ctr" || strings.HasSuffix(lm, "_pct"):
		return analytics.KindPercent
	case strings.Contains(lm, "revenue") || strings.Contains(lm, "cpm"):
		return analytics.KindCurrency
	}
	return analytics.KindCount
}

var metricLabels = map[string]string{
	"views": "Views", "engagedViews": "Engaged views", "estimatedMinutesWatched": "Watch time", "averageViewDuration": "Avg duration",
	"averageViewPercentage": "Avg % viewed", "subscribersGained": "Subs gained", "subscribersLost": "Subs lost", "likes": "Likes",
	"dislikes": "Dislikes", "comments": "Comments", "shares": "Shares", "impressions": "Impressions", "ctr": "CTR",
	"view_share_pct": "Share of views", "viewerPercentage": "% of viewers", "estimatedRevenue": "Est. revenue",
	"female": "Female", "male": "Male", "user_specified": "User-specified", "total": "Total",
}

func metricLabel(m string) string {
	if l, ok := metricLabels[m]; ok {
		return l
	}
	return m
}

func metricValue(m string, v float64) string {
	if m == "female" || m == "male" || m == "user_specified" || m == "total" {
		return percent(v)
	}
	return kindValue(metricKind(m), v)
}

// optCTR renders an optional CTR/impressions pair.
func optPct(v *float64) string {
	if v == nil {
		return "—"
	}
	return percent(*v)
}

func optCount(v *float64) string {
	if v == nil {
		return "—"
	}
	return count(math.Round(*v))
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func periodLine(p analytics.Period) string {
	return fmt.Sprintf("%s (%s to %s, Pacific time)", p.Label, p.Start, p.End)
}

func errorsSection(errs map[string]string) string {
	if len(errs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n**Partial results; these sections failed:**\n")
	for k, v := range errs {
		b.WriteString("- " + k + ": " + v + "\n")
	}
	return b.String()
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
