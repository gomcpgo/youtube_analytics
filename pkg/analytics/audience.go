package analytics

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/gomcpgo/youtube_analytics/pkg/youtube"
)

// BreakdownRow is one value of a dimension with its metrics.
type BreakdownRow struct {
	Key    string             `json:"key"`
	Label  string             `json:"label"`
	Values map[string]float64 `json:"values"`
}

// Breakdown is one audience dimension.
type Breakdown struct {
	Name    string         `json:"name"`
	Title   string         `json:"title"`
	Columns []string       `json:"columns"`
	Rows    []BreakdownRow `json:"rows"`
	Note    string         `json:"note,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// Audience is a set of breakdowns for one period.
type Audience struct {
	Period     Period      `json:"period"`
	VideoID    string      `json:"video_id,omitempty"`
	Breakdowns []Breakdown `json:"breakdowns"`
}

type breakdownSpec struct {
	title    string
	dim      string
	metrics  []string
	optional []string
	sort     string
	max      int
}

var breakdownSpecs = map[string]breakdownSpec{
	"geography": {"Top countries", "country", []string{"views", "estimatedMinutesWatched", "averageViewDuration", "averageViewPercentage", "subscribersGained"},
		[]string{"averageViewPercentage", "subscribersGained"}, "-views", 25},
	"devices":           {"Devices", "deviceType", []string{"views", "estimatedMinutesWatched"}, nil, "-views", 0},
	"operating_system":  {"Operating systems", "operatingSystem", []string{"views", "estimatedMinutesWatched"}, nil, "-views", 0},
	"subscription":      {"Subscribers vs non-subscribers", "subscribedStatus", []string{"views", "estimatedMinutesWatched", "averageViewDuration", "averageViewPercentage"}, []string{"averageViewPercentage"}, "", 0},
	"content_type":      {"Formats", "creatorContentType", formatMetrics, []string{"engagedViews"}, "", 0},
	"playback_location": {"Where videos were watched", "insightPlaybackLocationType", []string{"views", "estimatedMinutesWatched"}, nil, "-views", 0},
	"sharing":           {"Where viewers shared", "sharingService", []string{"shares"}, nil, "-shares", 25},
}

// BreakdownNames lists the accepted breakdowns; "all" runs the common ones.
var BreakdownNames = []string{"demographics", "geography", "devices", "subscription", "content_type", "playback_location", "operating_system", "sharing"}

var defaultBreakdowns = []string{"demographics", "geography", "devices", "subscription", "content_type"}

// Audience runs the requested breakdowns concurrently. A failing breakdown
// is reported in its Error field rather than failing the whole call.
func (s *Service) Audience(ctx context.Context, spec PeriodSpec, videoID string, which []string) (*Audience, error) {
	cur, _, err := s.periodFor(ctx, spec, videoID)
	if err != nil {
		return nil, err
	}
	if len(which) == 0 || len(which) == 1 && which[0] == "all" {
		which = defaultBreakdowns
	}
	for _, w := range which {
		if !contains(BreakdownNames, w) {
			return nil, fmt.Errorf("unknown breakdown %q (use %s or all)", w, strings.Join(BreakdownNames, ", "))
		}
	}
	out := &Audience{Period: cur, VideoID: videoID, Breakdowns: make([]Breakdown, len(which))}
	var total float64
	fns := []func(){func() {
		if t, err := s.yt.Query(ctx, youtube.Query{Start: cur.Start, End: cur.End, Metrics: []string{"views"}, Filters: filterVideo(videoID)}); err == nil && len(t.Rows) > 0 {
			total = t.Num(t.Rows[0], "views")
		}
	}}
	for i, name := range which {
		i, name := i, name
		fns = append(fns, func() {
			if name == "demographics" {
				out.Breakdowns[i] = s.demographics(ctx, cur, videoID)
			} else {
				out.Breakdowns[i] = s.breakdown(ctx, cur, videoID, name, breakdownSpecs[name])
			}
		})
	}
	parallel(fns...)
	for i := range out.Breakdowns {
		shareOfTotal(&out.Breakdowns[i], total)
	}
	return out, nil
}

// shareOfTotal rebases view shares on the period's total views. YouTube
// omits groups below its privacy threshold (countries especially), so the
// listed rows can cover far less than 100% of views.
func shareOfTotal(b *Breakdown, total float64) {
	if len(b.Rows) == 0 || total <= 0 {
		return
	}
	if _, ok := b.Rows[0].Values["view_share_pct"]; !ok {
		return
	}
	var listed float64
	for _, r := range b.Rows {
		listed += r.Values["views"]
	}
	base := math.Max(total, listed)
	for _, r := range b.Rows {
		r.Values["view_share_pct"] = pct(r.Values["views"], base)
	}
	if listed < 0.95*total {
		b.Note = fmt.Sprintf("Only %.0f%% of the period's %.0f views are attributed here; YouTube hides groups below its privacy threshold.", pct(listed, total), total)
	}
}

func (s *Service) breakdown(ctx context.Context, p Period, videoID, name string, bs breakdownSpec) Breakdown {
	b := Breakdown{Name: name, Title: bs.title}
	t, err := s.query(ctx, youtube.Query{Start: p.Start, End: p.End, Metrics: bs.metrics, Dimensions: []string{bs.dim},
		Filters: filterVideo(videoID), Sort: bs.sort, MaxResults: bs.max}, bs.optional...)
	if err != nil {
		b.Error = err.Error()
		return b
	}
	for _, c := range t.Columns {
		if c.Name != bs.dim {
			b.Columns = append(b.Columns, c.Name)
		}
	}
	var total float64
	share := t.Index("views") >= 0
	for _, r := range t.Rows {
		total += t.Num(r, "views")
	}
	if share {
		b.Columns = append(b.Columns, "view_share_pct")
	}
	for _, r := range t.Rows {
		key := t.Str(r, bs.dim)
		row := BreakdownRow{Key: key, Label: Label(bs.dim, key), Values: map[string]float64{}}
		for _, c := range b.Columns {
			if c != "view_share_pct" {
				row.Values[c] = t.Num(r, c)
			}
		}
		if share {
			row.Values["view_share_pct"] = pct(t.Num(r, "views"), total)
		}
		b.Rows = append(b.Rows, row)
	}
	if bs.sort == "" && share {
		sort.Slice(b.Rows, func(i, j int) bool { return b.Rows[i].Values["views"] > b.Rows[j].Values["views"] })
	}
	return b
}

// demographics pivots viewerPercentage by age group (rows) and gender (columns).
func (s *Service) demographics(ctx context.Context, p Period, videoID string) Breakdown {
	b := Breakdown{Name: "demographics", Title: "Age and gender (% of signed-in viewers)", Columns: []string{"female", "male", "user_specified", "total"}}
	t, err := s.yt.Query(ctx, youtube.Query{Start: p.Start, End: p.End, Metrics: []string{"viewerPercentage"},
		Dimensions: []string{"ageGroup", "gender"}, Filters: filterVideo(videoID), Sort: "ageGroup,gender"})
	if err != nil {
		b.Error = err.Error()
		return b
	}
	byAge := map[string]map[string]float64{}
	for _, r := range t.Rows {
		age, g := t.Str(r, "ageGroup"), t.Str(r, "gender")
		if byAge[age] == nil {
			byAge[age] = map[string]float64{}
		}
		v := t.Num(r, "viewerPercentage")
		byAge[age][g] += v
		byAge[age]["total"] += v
	}
	for _, age := range sortedKeys(byAge) {
		b.Rows = append(b.Rows, BreakdownRow{Key: age, Label: Label("ageGroup", age), Values: byAge[age]})
	}
	return b
}
