package analytics

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gomcpgo/youtube_analytics/pkg/youtube"
)

// DataLagDays is how far rolling windows end before today. YouTube Analytics
// data arrives 48-72 hours late, so ending earlier keeps period comparisons
// fair (a partial last day would look like a decline).
const DataLagDays = 3

const dateLayout = "2006-01-02"

// Period is an inclusive date range in Pacific time (YYYY-MM-DD).
type Period struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Label string `json:"label"`
}

// Days is the number of days in the period.
func (p Period) Days() int {
	s, _ := time.Parse(dateLayout, p.Start)
	e, _ := time.Parse(dateLayout, p.End)
	return int(e.Sub(s).Hours()/24) + 1
}

// PeriodSpec is the caller's request: a preset or explicit dates.
type PeriodSpec struct {
	Preset string // 7d, 28d, 90d, 365d, lifetime
	Start  string
	End    string
}

// ResolvePeriod turns a spec into a period and, except for lifetime, the
// equally long period right before it. lifetimeStart is the channel's or
// video's creation time.
func ResolvePeriod(spec PeriodSpec, now time.Time, lifetimeStart time.Time) (Period, *Period, error) {
	today := dateOnly(now.In(youtube.Pacific))
	if spec.Start != "" || spec.End != "" {
		if spec.Start == "" {
			return Period{}, nil, fmt.Errorf("start_date is required when end_date is set")
		}
		s, err := time.Parse(dateLayout, spec.Start)
		if err != nil {
			return Period{}, nil, fmt.Errorf("invalid start_date %q (use YYYY-MM-DD)", spec.Start)
		}
		e := today
		if spec.End != "" {
			if e, err = time.Parse(dateLayout, spec.End); err != nil {
				return Period{}, nil, fmt.Errorf("invalid end_date %q (use YYYY-MM-DD)", spec.End)
			}
		}
		if e.Before(s) {
			return Period{}, nil, fmt.Errorf("end_date %s is before start_date %s", e.Format(dateLayout), spec.Start)
		}
		cur := mk(s, e, "")
		prev := previous(s, e)
		return cur, &prev, nil
	}

	preset := strings.ToLower(strings.TrimSpace(spec.Preset))
	if preset == "" {
		preset = "28d"
	}
	if preset == "lifetime" || preset == "all" || preset == "max" {
		s := dateOnly(lifetimeStart.In(youtube.Pacific))
		if lifetimeStart.IsZero() || s.Before(time.Date(2005, 4, 23, 0, 0, 0, 0, time.UTC)) {
			s = time.Date(2005, 4, 23, 0, 0, 0, 0, time.UTC)
		}
		if s.After(today) {
			s = today
		}
		return mk(s, today, "lifetime"), nil, nil
	}
	n, ok := presetDays(preset)
	if !ok {
		return Period{}, nil, fmt.Errorf("unknown period %q (use 7d, 28d, 90d, 365d or lifetime, or start_date/end_date)", spec.Preset)
	}
	e := today.AddDate(0, 0, -DataLagDays)
	s := e.AddDate(0, 0, -(n - 1))
	prev := previous(s, e)
	return mk(s, e, fmt.Sprintf("last %d days", n)), &prev, nil
}

// SincePublish is the period from a video's publish date through today.
func SincePublish(published, now time.Time) Period {
	today := dateOnly(now.In(youtube.Pacific))
	s := dateOnly(published.In(youtube.Pacific))
	if s.After(today) {
		s = today
	}
	return mk(s, today, "since publish")
}

func presetDays(p string) (int, bool) {
	p = strings.TrimPrefix(p, "last_")
	p = strings.TrimSuffix(p, "_days")
	p = strings.TrimSuffix(p, "days")
	p = strings.TrimSuffix(p, "d")
	n, err := strconv.Atoi(p)
	return n, err == nil && n > 0 && n <= 3650
}

func previous(s, e time.Time) Period {
	days := int(e.Sub(s).Hours()/24) + 1
	pe := s.AddDate(0, 0, -1)
	ps := pe.AddDate(0, 0, -(days - 1))
	return mk(ps, pe, "previous period")
}

func mk(s, e time.Time, label string) Period {
	p := Period{Start: s.Format(dateLayout), End: e.Format(dateLayout)}
	if label == "" {
		label = fmt.Sprintf("%d days", p.Days())
	}
	p.Label = label
	return p
}

func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
