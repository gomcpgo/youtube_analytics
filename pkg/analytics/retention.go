package analytics

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"

	"github.com/gomcpgo/youtube_analytics/pkg/youtube"
)

// RetentionPoint is one of the ~100 points of the audience retention curve.
type RetentionPoint struct {
	Ratio    float64 `json:"elapsed_ratio"`        // 0.01 .. 1.0
	Second   float64 `json:"second"`               // Ratio x duration
	Watch    float64 `json:"watch_ratio"`          // share of views still watching (can exceed 1 on rewatches)
	Relative float64 `json:"relative_performance"` // 0..1 vs videos of similar length
	Started  float64 `json:"started_watching"`
	Stopped  float64 `json:"stopped_watching"`
}

// Span is a stretch of the video where retention moved sharply.
type Span struct {
	FromSec  float64 `json:"from_second"`
	ToSec    float64 `json:"to_second"`
	FromPct  float64 `json:"retention_before_pct"`
	ToPct    float64 `json:"retention_after_pct"`
	PointsPP float64 `json:"change_points"` // percentage points (negative for drops)
}

// Moment is a single position with a value (share of exits, relative score).
type Moment struct {
	Second float64 `json:"second"`
	Value  float64 `json:"value"`
}

// Retention is the analyzed audience retention curve of one video.
type Retention struct {
	VideoID     string            `json:"video_id"`
	Title       string            `json:"title"`
	DurationSec float64           `json:"duration_seconds"`
	Filters     map[string]string `json:"filters,omitempty"`
	Period      Period            `json:"period"`

	AvgWatchPct float64  `json:"approx_avg_percentage_viewed"`
	StartPct    float64  `json:"retention_at_start_pct"`
	At30sPct    *float64 `json:"retention_at_30s_pct,omitempty"`
	At25Pct     float64  `json:"retention_at_25pct"`
	At50Pct     float64  `json:"retention_at_50pct"`
	At75Pct     float64  `json:"retention_at_75pct"`
	EndPct      float64  `json:"retention_at_end_pct"`
	// Intro covers the first 30 seconds (or first 10% of videos under 90s).
	Intro       Span             `json:"intro"`
	Drops       []Span           `json:"steepest_drops"`
	OutroLossPP float64          `json:"outro_loss_points"` // drop across the last 5%
	Rewatches   []Span           `json:"rewatch_spikes"`
	Exits       []Moment         `json:"top_exit_points"` // share of all exits, excluding the last 5%
	RelativeAvg float64          `json:"relative_performance_avg"`
	WeakSpans   []Span           `json:"below_typical_spans"` // relative performance < 0.35
	StrongSpans []Span           `json:"above_typical_spans"` // relative performance > 0.65
	Points      []RetentionPoint `json:"points"`
}

// RetentionOptions filter the curve.
type RetentionOptions struct {
	AudienceType     string // ORGANIC, AD_INSTREAM, AD_INDISPLAY
	SubscribedStatus string // SUBSCRIBED, UNSUBSCRIBED
}

// Retention fetches and analyzes a video's audience retention since publish.
func (s *Service) Retention(ctx context.Context, videoID string, opt RetentionOptions) (*Retention, error) {
	v, err := s.yt.Video(ctx, videoID)
	if err != nil {
		return nil, err
	}
	p := SincePublish(v.PublishedAt, s.now())
	filters := filterVideo(videoID)
	applied := map[string]string{}
	for k, val := range map[string]string{"audienceType": opt.AudienceType, "subscribedStatus": opt.SubscribedStatus} {
		if val != "" {
			filters = append(filters, k+"=="+val)
			applied[k] = val
		}
	}
	sort.Strings(filters[1:])
	t, err := s.query(ctx, youtube.Query{
		Start: p.Start, End: p.End, Dimensions: []string{"elapsedVideoTimeRatio"}, Filters: filters,
		Metrics: []string{"audienceWatchRatio", "relativeRetentionPerformance", "startedWatching", "stoppedWatching"},
	}, "startedWatching", "stoppedWatching")
	if err != nil {
		return nil, err
	}
	if len(t.Rows) == 0 {
		return nil, errors.New("YouTube returned no retention data for this video: it needs a minimum number of views (and the filters must match some views)")
	}
	pts := make([]RetentionPoint, 0, len(t.Rows))
	for _, r := range t.Rows {
		pts = append(pts, RetentionPoint{
			Ratio: t.Num(r, "elapsedVideoTimeRatio"), Watch: t.Num(r, "audienceWatchRatio"),
			Relative: t.Num(r, "relativeRetentionPerformance"), Started: t.Num(r, "startedWatching"), Stopped: t.Num(r, "stoppedWatching"),
		})
	}
	res := AnalyzeRetention(pts, v.Duration.Seconds())
	res.VideoID, res.Title, res.Period = v.ID, v.Title, p
	if len(applied) > 0 {
		res.Filters = applied
	}
	return res, nil
}

// AnalyzeRetention derives the hook, drop-offs, rewatches and exits from a curve.
func AnalyzeRetention(pts []RetentionPoint, durationSec float64) *Retention {
	sort.Slice(pts, func(i, j int) bool { return pts[i].Ratio < pts[j].Ratio })
	for i := range pts {
		pts[i].Second = pts[i].Ratio * durationSec
	}
	r := &Retention{DurationSec: durationSec, Points: pts}
	n := len(pts)
	if n == 0 {
		return r
	}
	at := func(ratio float64) int {
		best := 0
		for i, p := range pts {
			if math.Abs(p.Ratio-ratio) < math.Abs(pts[best].Ratio-ratio) {
				best = i
			}
		}
		return best
	}
	w := func(i int) float64 { return pts[i].Watch * 100 }
	span := func(from, to int) Span {
		start := 0.0
		if from > 0 {
			start = pts[from].Second
		}
		return Span{FromSec: start, ToSec: pts[to].Second, FromPct: w(from), ToPct: w(to), PointsPP: w(to) - w(from)}
	}

	var sum, rel float64
	for i := range pts {
		sum += w(i)
		rel += pts[i].Relative
	}
	r.AvgWatchPct = sum / float64(n)
	r.RelativeAvg = rel / float64(n)
	r.StartPct, r.EndPct = w(0), w(n-1)
	r.At25Pct, r.At50Pct, r.At75Pct = w(at(0.25)), w(at(0.5)), w(at(0.75))

	introEnd := at(0.10)
	if durationSec >= 90 {
		i30 := at(30 / durationSec)
		v := w(i30)
		r.At30sPct = &v
		introEnd = i30
	}
	r.Intro = span(0, introEnd)

	outroStart := at(0.95)
	r.OutroLossPP = w(outroStart) - w(n-1)

	// Segment deltas between consecutive points, after the intro and before the outro.
	var segs []segment
	for i := introEnd + 1; i <= outroStart; i++ {
		segs = append(segs, segment{i, w(i) - w(i-1)})
	}
	r.Drops = pickSpans(segs, func(d float64) bool { return d < 0 }, -1.0, 5, span)
	r.Rewatches = pickSpans(segs, func(d float64) bool { return d > 0 }, 0.5, 3, span)

	var stopped float64
	for i := 0; i < outroStart; i++ {
		stopped += pts[i].Stopped
	}
	if stopped > 0 {
		var ex []Moment
		for i := 0; i < outroStart; i++ {
			ex = append(ex, Moment{Second: pts[i].Second, Value: pct(pts[i].Stopped, stopped)})
		}
		sort.Slice(ex, func(i, j int) bool { return ex[i].Value > ex[j].Value })
		// Only exits well above an even spread are worth pointing at.
		floor := 2 * 100 / float64(len(ex))
		for _, m := range ex[:min(3, len(ex))] {
			if m.Value >= floor {
				r.Exits = append(r.Exits, m)
			}
		}
	}

	r.WeakSpans = runs(pts, func(p RetentionPoint) bool { return p.Relative < 0.35 }, span)
	r.StrongSpans = runs(pts, func(p RetentionPoint) bool { return p.Relative > 0.65 }, span)
	return r
}

// segment is the change d (points) from point i-1 to point i.
type segment struct {
	i int
	d float64
}

// pickSpans picks the largest segments matching want, merges adjacent picks
// into spans, keeps those whose total change beats minPP (in points) and
// ranks them by their steepest segment, so sharp cliffs outrank slow slides.
func pickSpans(segs []segment, want func(float64) bool, minPP float64, keep int, span func(int, int) Span) []Span {
	var cand []segment
	for _, x := range segs {
		if want(x.d) {
			cand = append(cand, x)
		}
	}
	sort.Slice(cand, func(i, j int) bool { return math.Abs(cand[i].d) > math.Abs(cand[j].d) })
	cand = cand[:min(len(cand), keep*2)]
	sort.Slice(cand, func(i, j int) bool { return cand[i].i < cand[j].i })

	type ranked struct {
		Span
		peak float64
	}
	var out []ranked
	for k := 0; k < len(cand); {
		j, peak := k, math.Abs(cand[k].d)
		for j+1 < len(cand) && cand[j+1].i == cand[j].i+1 {
			j++
			peak = math.Max(peak, math.Abs(cand[j].d))
		}
		sp := span(cand[k].i-1, cand[j].i)
		if minPP < 0 && sp.PointsPP <= minPP || minPP > 0 && sp.PointsPP >= minPP {
			out = append(out, ranked{sp, peak})
		}
		k = j + 1
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].peak > out[j].peak })
	spans := make([]Span, 0, keep)
	for _, r := range out[:min(len(out), keep)] {
		spans = append(spans, r.Span)
	}
	return spans
}

// runs returns contiguous stretches of at least 3 points satisfying cond.
func runs(pts []RetentionPoint, cond func(RetentionPoint) bool, span func(int, int) Span) []Span {
	var out []Span
	for i := 0; i < len(pts); {
		if !cond(pts[i]) {
			i++
			continue
		}
		j := i
		for j+1 < len(pts) && cond(pts[j+1]) {
			j++
		}
		if j-i >= 2 {
			out = append(out, span(i, j))
		}
		i = j + 1
	}
	return out
}

// AudienceTypeFilter normalizes an audience_type argument.
func AudienceTypeFilter(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "all":
		return "", true
	case "organic":
		return "ORGANIC", true
	case "ad_instream", "ads", "instream":
		return "AD_INSTREAM", true
	case "ad_indisplay", "indisplay":
		return "AD_INDISPLAY", true
	}
	return "", false
}

// SubscribedFilter normalizes a subscribed_status argument.
func SubscribedFilter(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "all":
		return "", true
	case "subscribed", "subscribers":
		return "SUBSCRIBED", true
	case "unsubscribed", "non_subscribers", "non-subscribers":
		return "UNSUBSCRIBED", true
	}
	return "", false
}
