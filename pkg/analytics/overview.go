package analytics

import (
	"context"
	"errors"
	"strings"

	"github.com/gomcpgo/youtube_analytics/pkg/reach"
	"github.com/gomcpgo/youtube_analytics/pkg/youtube"
)

// Delta is one metric for a period and, when compared, the previous period.
type Delta struct {
	Metric    string   `json:"metric"`
	Label     string   `json:"label"`
	Kind      Kind     `json:"kind"`
	Current   float64  `json:"current"`
	Previous  *float64 `json:"previous,omitempty"`
	ChangePct *float64 `json:"change_pct,omitempty"`
}

// FormatStat is the period's performance of one content format.
type FormatStat struct {
	Type               string   `json:"content_type"`
	Format             string   `json:"format"`
	Views              float64  `json:"views"`
	EngagedViews       float64  `json:"engaged_views"`
	WatchMinutes       float64  `json:"watch_minutes"`
	AvgViewDurationSec float64  `json:"avg_view_duration_seconds"`
	AvgViewPct         float64  `json:"avg_view_percentage"`
	ViewShare          float64  `json:"view_share_pct"`
	PrevViews          *float64 `json:"previous_views,omitempty"`
	ChangePct          *float64 `json:"views_change_pct,omitempty"`
}

// Overview is a channel health summary for a period.
type Overview struct {
	Channel   *youtube.Channel  `json:"channel"`
	Period    Period            `json:"period"`
	Previous  *Period           `json:"previous_period,omitempty"`
	Metrics   []Delta           `json:"metrics"`
	Formats   []FormatStat      `json:"formats,omitempty"`
	Revenue   []Delta           `json:"revenue,omitempty"`
	TopVideos []VideoStat       `json:"top_videos,omitempty"`
	Reach     Coverage          `json:"reach"`
	Errors    map[string]string `json:"errors,omitempty"`
}

// OverviewOptions tune Overview.
type OverviewOptions struct {
	Compare  bool
	Revenue  bool
	Currency string
}

var basicMetrics = []string{"views", "engagedViews", "estimatedMinutesWatched", "averageViewDuration",
	"subscribersGained", "subscribersLost", "likes", "dislikes", "comments", "shares"}

var formatMetrics = []string{"views", "engagedViews", "estimatedMinutesWatched", "averageViewDuration", "averageViewPercentage"}

var revenueMetrics = []string{"estimatedRevenue", "estimatedAdRevenue", "estimatedRedPartnerRevenue", "grossRevenue",
	"cpm", "playbackBasedCpm", "monetizedPlaybacks", "adImpressions"}

// Overview summarizes channel performance for a period against the previous one.
func (s *Service) Overview(ctx context.Context, spec PeriodSpec, opt OverviewOptions) (*Overview, error) {
	ch, err := s.yt.MyChannel(ctx)
	if err != nil {
		return nil, err
	}
	cur, prev, err := ResolvePeriod(spec, s.now(), ch.PublishedAt)
	if err != nil {
		return nil, err
	}
	if !opt.Compare {
		prev = nil
	}
	o := &Overview{Channel: ch, Period: cur, Previous: prev}

	var e errs
	var basicCur, basicPrev, fmtCur, fmtPrev, revCur, revPrev *youtube.Table
	var reachCur, reachPrev *reach.Agg
	var curRows []reach.Row
	basic := func(p Period, out **youtube.Table) func() {
		return func() {
			t, err := s.query(ctx, youtube.Query{Start: p.Start, End: p.End, Metrics: basicMetrics}, "engagedViews", "dislikes")
			*out = t
			e.set("metrics "+p.Label, err)
		}
	}
	formats := func(p Period, out **youtube.Table) func() {
		return func() {
			t, err := s.query(ctx, youtube.Query{Start: p.Start, End: p.End, Metrics: formatMetrics, Dimensions: []string{"creatorContentType"}}, "engagedViews")
			*out = t
			e.set("formats "+p.Label, err)
		}
	}
	revenue := func(p Period, out **youtube.Table) func() {
		return func() {
			t, err := s.query(ctx, youtube.Query{Start: p.Start, End: p.End, Metrics: revenueMetrics, Currency: opt.Currency},
				"grossRevenue", "cpm", "playbackBasedCpm", "monetizedPlaybacks", "adImpressions")
			*out = t
			e.set("revenue "+p.Label, err)
		}
	}
	fns := []func(){
		basic(cur, &basicCur),
		formats(cur, &fmtCur),
		func() {
			vs, err := s.videoStats(ctx, cur, PerformanceOptions{Sort: "views", Limit: 5})
			o.TopVideos = vs
			e.set("top videos", err)
		},
		func() {
			rows, cov := s.reachRows(ctx, reach.Basic, cur)
			o.Reach = cov
			if !cov.Available {
				return
			}
			a := reach.Total(rows, "")
			reachCur, curRows = &a, rows
			if prev == nil || cov.DaysCovered < cov.DaysRequested {
				return
			}
			prows, pcov := s.reachRows(ctx, reach.Basic, *prev)
			if pcov.DaysCovered == pcov.DaysRequested {
				pa := reach.Total(prows, "")
				reachPrev = &pa
			} else {
				o.Reach.Note += " No CTR comparison: the previous period is not fully covered."
			}
		},
	}
	if prev != nil {
		fns = append(fns, basic(*prev, &basicPrev), formats(*prev, &fmtPrev))
	}
	if opt.Revenue {
		fns = append(fns, revenue(cur, &revCur))
		if prev != nil {
			fns = append(fns, revenue(*prev, &revPrev))
		}
	}
	parallel(fns...)
	o.Errors = e.m
	if basicCur == nil {
		msg := "the analytics query failed"
		for k, v := range e.m {
			if strings.HasPrefix(k, "metrics") {
				msg = v
			}
		}
		return nil, errors.New(msg)
	}

	if len(curRows) > 0 {
		byVideo := reach.GroupBy(curRows, func(r reach.Row) string { return r.VideoID })
		for i := range o.TopVideos {
			if a, ok := byVideo[o.TopVideos[i].ID]; ok {
				o.TopVideos[i].Impressions, o.TopVideos[i].CTR = fptr(a.Impressions), fptr(a.CTR())
			}
		}
	}

	one := func(t *youtube.Table, m string) float64 {
		if t == nil || len(t.Rows) == 0 {
			return 0
		}
		return t.Num(t.Rows[0], m)
	}
	add := func(metric, label string, kind Kind, f func(t *youtube.Table) float64, prevT *youtube.Table, curT *youtube.Table) {
		d := Delta{Metric: metric, Label: label, Kind: kind, Current: f(curT)}
		if prevT != nil {
			p := f(prevT)
			d.Previous = &p
			d.ChangePct = change(d.Current, p)
		}
		o.Metrics = append(o.Metrics, d)
	}
	m := func(name string) func(*youtube.Table) float64 {
		return func(t *youtube.Table) float64 { return one(t, name) }
	}
	add("views", "Views", KindCount, m("views"), basicPrev, basicCur)
	if basicCur.Index("engagedViews") >= 0 {
		add("engagedViews", "Engaged views", KindCount, m("engagedViews"), basicPrev, basicCur)
	}
	add("estimatedMinutesWatched", "Watch time", KindMinutes, m("estimatedMinutesWatched"), basicPrev, basicCur)
	add("averageViewDuration", "Avg view duration", KindSeconds, m("averageViewDuration"), basicPrev, basicCur)
	if fmtCur != nil {
		d := Delta{Metric: "averageViewPercentage", Label: "Avg % viewed", Kind: KindPercent, Current: weightedPct(fmtCur)}
		if fmtPrev != nil {
			p := weightedPct(fmtPrev)
			d.Previous, d.ChangePct = &p, change(d.Current, p)
		}
		o.Metrics = append(o.Metrics, d)
	}
	if reachCur != nil {
		imp := Delta{Metric: "impressions", Label: "Impressions", Kind: KindCount, Current: reachCur.Impressions}
		ctr := Delta{Metric: "impressionsCtr", Label: "Impressions CTR", Kind: KindPercent, Current: reachCur.CTR()}
		if reachPrev != nil {
			imp.Previous, imp.ChangePct = fptr(reachPrev.Impressions), change(reachCur.Impressions, reachPrev.Impressions)
			ctr.Previous, ctr.ChangePct = fptr(reachPrev.CTR()), change(reachCur.CTR(), reachPrev.CTR())
		}
		o.Metrics = append(o.Metrics, imp, ctr)
	}
	add("subscribersGained", "Subscribers gained", KindCount, m("subscribersGained"), basicPrev, basicCur)
	add("subscribersLost", "Subscribers lost", KindCount, m("subscribersLost"), basicPrev, basicCur)
	add("subscribersNet", "Net subscribers", KindCount, func(t *youtube.Table) float64 {
		return one(t, "subscribersGained") - one(t, "subscribersLost")
	}, basicPrev, basicCur)
	add("subscribersPer1k", "Subscribers per 1K views", KindPer1K, func(t *youtube.Table) float64 {
		return per1k(one(t, "subscribersGained"), one(t, "views"))
	}, basicPrev, basicCur)
	add("likes", "Likes", KindCount, m("likes"), basicPrev, basicCur)
	if basicCur.Index("dislikes") >= 0 {
		add("dislikes", "Dislikes", KindCount, m("dislikes"), basicPrev, basicCur)
	}
	add("comments", "Comments", KindCount, m("comments"), basicPrev, basicCur)
	add("shares", "Shares", KindCount, m("shares"), basicPrev, basicCur)
	add("engagementRate", "Engagement rate (likes+comments+shares per view)", KindPercent, func(t *youtube.Table) float64 {
		return pct(one(t, "likes")+one(t, "comments")+one(t, "shares"), one(t, "views"))
	}, basicPrev, basicCur)

	if fmtCur != nil {
		prevViews := map[string]float64{}
		if fmtPrev != nil {
			for _, r := range fmtPrev.Rows {
				prevViews[fmtPrev.Str(r, "creatorContentType")] = fmtPrev.Num(r, "views")
			}
		}
		total := 0.0
		for _, r := range fmtCur.Rows {
			total += fmtCur.Num(r, "views")
		}
		for _, r := range fmtCur.Rows {
			typ := fmtCur.Str(r, "creatorContentType")
			f := FormatStat{
				Type: typ, Format: Label("creatorContentType", typ),
				Views: fmtCur.Num(r, "views"), EngagedViews: fmtCur.Num(r, "engagedViews"),
				WatchMinutes: fmtCur.Num(r, "estimatedMinutesWatched"), AvgViewDurationSec: fmtCur.Num(r, "averageViewDuration"),
				AvgViewPct: fmtCur.Num(r, "averageViewPercentage"),
			}
			f.ViewShare = pct(f.Views, total)
			if fmtPrev != nil {
				p := prevViews[typ]
				f.PrevViews, f.ChangePct = &p, change(f.Views, p)
			}
			o.Formats = append(o.Formats, f)
		}
	}

	if revCur != nil {
		for _, c := range revCur.Columns {
			d := Delta{Metric: c.Name, Label: revenueLabel(c.Name), Kind: KindCurrency, Current: one(revCur, c.Name)}
			if c.Name == "monetizedPlaybacks" || c.Name == "adImpressions" {
				d.Kind = KindCount
			}
			if revPrev != nil && revPrev.Index(c.Name) >= 0 {
				p := one(revPrev, c.Name)
				d.Previous, d.ChangePct = &p, change(d.Current, p)
			}
			o.Revenue = append(o.Revenue, d)
		}
	}
	return o, nil
}

// weightedPct is the views-weighted average view percentage across rows.
func weightedPct(t *youtube.Table) float64 {
	var sum, views float64
	for _, r := range t.Rows {
		v := t.Num(r, "views")
		sum += t.Num(r, "averageViewPercentage") * v
		views += v
	}
	if views == 0 {
		return 0
	}
	return sum / views
}

func per1k(n, views float64) float64 {
	if views == 0 {
		return 0
	}
	return n / views * 1000
}

func revenueLabel(m string) string {
	switch m {
	case "estimatedRevenue":
		return "Estimated revenue (net)"
	case "estimatedAdRevenue":
		return "Estimated ad revenue"
	case "estimatedRedPartnerRevenue":
		return "YouTube Premium revenue"
	case "grossRevenue":
		return "Gross ad revenue"
	case "cpm":
		return "CPM"
	case "playbackBasedCpm":
		return "Playback-based CPM"
	case "monetizedPlaybacks":
		return "Monetized playbacks"
	case "adImpressions":
		return "Ad impressions"
	}
	return m
}
