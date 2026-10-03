package analytics

import (
	"math"
	"testing"
	"time"
)

func TestResolvePeriod(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) // 05:00 Pacific, same day
	created := time.Date(2019, 3, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		spec              PeriodSpec
		start, end        string
		prevStart, prevEn string
		err               bool
	}{
		{PeriodSpec{}, "2026-08-27", "2026-09-23", "2026-07-30", "2026-08-26", false},
		{PeriodSpec{Preset: "7d"}, "2026-09-17", "2026-09-23", "2026-09-10", "2026-09-16", false},
		{PeriodSpec{Preset: "last_90_days"}, "2026-06-26", "2026-09-23", "2026-03-28", "2026-06-25", false},
		{PeriodSpec{Preset: "lifetime"}, "2019-02-28", "2026-09-26", "", "", false},
		{PeriodSpec{Start: "2026-09-01", End: "2026-09-10"}, "2026-09-01", "2026-09-10", "2026-08-22", "2026-08-31", false},
		{PeriodSpec{Start: "2026-09-01"}, "2026-09-01", "2026-09-26", "2026-08-06", "2026-08-31", false},
		{PeriodSpec{End: "2026-09-10"}, "", "", "", "", true},
		{PeriodSpec{Start: "2026-09-10", End: "2026-09-01"}, "", "", "", "", true},
		{PeriodSpec{Preset: "fortnight"}, "", "", "", "", true},
	}
	for _, c := range cases {
		cur, prev, err := ResolvePeriod(c.spec, now, created)
		if c.err {
			if err == nil {
				t.Errorf("%+v: expected error", c.spec)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%+v: %v", c.spec, err)
		}
		if cur.Start != c.start || cur.End != c.end {
			t.Errorf("%+v: got %s..%s want %s..%s", c.spec, cur.Start, cur.End, c.start, c.end)
		}
		if c.prevStart == "" {
			if prev != nil {
				t.Errorf("%+v: lifetime should have no previous period", c.spec)
			}
			continue
		}
		if prev == nil || prev.Start != c.prevStart || prev.End != c.prevEn {
			t.Errorf("%+v: previous %+v want %s..%s", c.spec, prev, c.prevStart, c.prevEn)
		}
		if prev != nil && prev.Days() != cur.Days() {
			t.Errorf("%+v: previous period has %d days, current %d", c.spec, prev.Days(), cur.Days())
		}
	}
}

// curve builds a 100-point curve: steady decay, a 12-point cliff at 50%,
// and a 4-point rewatch bump at 70%.
func curve() []RetentionPoint {
	var pts []RetentionPoint
	for i := 1; i <= 100; i++ {
		r := float64(i) / 100
		w := 0.9 - 0.3*r
		if r > 0.50 {
			w -= 0.12
		}
		if r > 0.70 && r <= 0.72 {
			w += 0.04
		}
		stopped := 5.0
		if i == 51 {
			stopped = 200
		}
		rel := 0.6
		if r > 0.50 && r <= 0.60 {
			rel = 0.2
		}
		pts = append(pts, RetentionPoint{Ratio: r, Watch: w, Relative: rel, Stopped: stopped})
	}
	return pts
}

func TestAnalyzeRetention(t *testing.T) {
	r := AnalyzeRetention(curve(), 600, false)
	if r.At30sPct == nil || math.Abs(*r.At30sPct-(90-0.3*5)) > 0.01 {
		t.Errorf("At30s = %v, want 88.5", r.At30sPct)
	}
	if len(r.Drops) == 0 {
		t.Fatal("no drops found")
	}
	d := r.Drops[0]
	if d.FromSec != 300 || d.ToSec != 306 || d.PointsPP > -12 {
		t.Errorf("top drop = %+v, want 5:00-5:06 losing >= 12 points", d)
	}
	if len(r.Rewatches) == 0 || r.Rewatches[0].FromSec != 420 {
		t.Errorf("rewatch = %+v, want starting at 7:00", r.Rewatches)
	}
	if len(r.Exits) == 0 || r.Exits[0].Second != 306 {
		t.Errorf("exits = %+v, want 5:06 first", r.Exits)
	}
	if len(r.WeakSpans) != 1 || r.WeakSpans[0].FromSec != 306 || r.WeakSpans[0].ToSec != 360 {
		t.Errorf("weak spans = %+v, want 5:06-6:00", r.WeakSpans)
	}
	if r.OutroLossPP <= 0 {
		t.Errorf("outro loss = %v, want positive", r.OutroLossPP)
	}
}

func TestAnalyzeRetentionShortVideo(t *testing.T) {
	r := AnalyzeRetention(curve(), 45, false)
	if r.At30sPct != nil {
		t.Errorf("videos under 90s should not report 0:30 retention")
	}
	if r.Intro.ToSec != 4.5 {
		t.Errorf("short intro should end at 10%% (4.5s), got %v", r.Intro.ToSec)
	}
}

func TestAdditive(t *testing.T) {
	for m, want := range map[string]bool{"views": true, "averageViewDuration": false, "ctr": false, "cpm": false, "impressions": true, "averageViewPercentage": false} {
		if Additive(m) != want {
			t.Errorf("Additive(%s) = %v", m, !want)
		}
	}
}

func TestAnalyzeRetentionSmoothsNoise(t *testing.T) {
	// A steady decline with ±2-point alternating noise and a 15-point cliff at 50%.
	var pts []RetentionPoint
	for i := 1; i <= 100; i++ {
		r := float64(i) / 100
		w := 0.8 - 0.3*r
		if i%2 == 0 {
			w += 0.04
		}
		if r > 0.50 {
			w -= 0.15
		}
		pts = append(pts, RetentionPoint{Ratio: r, Watch: w, Relative: 0.5})
	}
	raw := AnalyzeRetention(append([]RetentionPoint(nil), pts...), 600, false)
	if len(raw.Rewatches) == 0 {
		t.Fatal("the raw noisy curve should produce spurious rewatch spikes")
	}
	sm := AnalyzeRetention(pts, 600, true)
	if !sm.Smoothed || len(sm.Rewatches) != 0 {
		t.Errorf("smoothed analysis should drop noise spikes, got %+v", sm.Rewatches)
	}
	if len(sm.Drops) == 0 || sm.Drops[0].FromSec > 300 || sm.Drops[0].ToSec < 306 {
		t.Errorf("smoothed analysis should still find the cliff at 5:00, got %+v", sm.Drops)
	}
}
