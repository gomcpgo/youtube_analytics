package reach

import (
	"context"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gomcpgo/youtube_analytics/internal/fakegoogle"
	"github.com/gomcpgo/youtube_analytics/pkg/youtube"
)

func client(fg *fakegoogle.Server) *youtube.Client {
	c := youtube.New(http.DefaultClient, nil)
	c.DataURL, c.AnalyticsURL, c.ReportingURL = fg.URL+"/youtube/v3", fg.URL+"/v2", fg.URL+"/v1"
	return c
}

func TestSyncCreatesJobsAndKeepsNewestReport(t *testing.T) {
	fg := fakegoogle.New()
	defer fg.Close()
	dir := t.TempDir()
	c, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Sync(context.Background(), client(fg), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.JobsCreated) != 2 || res.Downloaded != 4 {
		t.Fatalf("sync = %+v, want 2 jobs created and 4 files", res)
	}
	// A second sync within maxAge is skipped; a forced one downloads nothing new.
	if r2, _ := c.Sync(context.Background(), client(fg), time.Hour); !r2.Skipped {
		t.Errorf("second sync should be skipped")
	}
	if r3, err := c.Sync(context.Background(), client(fg), 0); err != nil || r3.Downloaded != 0 || len(r3.JobsCreated) != 0 {
		t.Errorf("forced resync = %+v, %v; want nothing new", r3, err)
	}

	rows, days, err := c.Rows(Basic, fg.Days[0], fg.Days[1])
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 {
		t.Fatalf("days = %v", days)
	}
	tot := Total(rows, "")
	if tot.Impressions != 3000 || math.Abs(tot.CTR()-4.0) > 1e-9 {
		t.Errorf("total = %+v CTR %.3f, want 3000 impressions at 4%%", tot, tot.CTR())
	}
	if v := Total(rows, fakegoogle.Vid2); math.Abs(v.CTR()-2.0) > 1e-9 {
		t.Errorf("vid2 CTR = %v", v.CTR())
	}
	if rows[0].Date != fg.Days[0] {
		t.Errorf("row date = %q, want normalized %q", rows[0].Date, fg.Days[0])
	}

	comb, _, _ := c.Rows(Combined, fg.Days[0], fg.Days[1])
	by := GroupBy(comb, func(r Row) string { return TrafficSourceName(r.TrafficSource) })
	if by["Browse features"] == nil || by["Shorts feed"] == nil || math.Abs(by["Shorts feed"].CTR()-2.0) > 1e-9 {
		t.Errorf("by source = %v", by)
	}

	// State survives a reopen.
	c2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st := c2.Status(); st.Days != 2 || st.First != fg.Days[0] || len(st.Jobs) != 2 {
		t.Errorf("reopened status = %+v", st)
	}
}

func TestRatioScaleCTR(t *testing.T) {
	dir := t.TempDir()
	c, _ := Open(dir)
	csv := "date,channel_id,video_id,video_thumbnail_impressions,video_thumbnail_impressions_ctr\n20260901,UC,v1,1000,0.05\n20260901,UC,v2,1000,0.03\n"
	if err := c.writeDay(Basic, "2026-09-01", []byte(csv)); err != nil {
		t.Fatal(err)
	}
	c.idx.Days[Basic] = map[string]DayFile{"2026-09-01": {ReportID: "x"}}
	rows, _, _ := c.Rows(Basic, "2026-09-01", "2026-09-01")
	if got := Total(rows, "").CTR(); math.Abs(got-4.0) > 1e-9 {
		t.Errorf("ratio-scale CTR = %v, want 4", got)
	}
	if c.idx.PercentCTR {
		t.Errorf("ratio file must not flip the scale to percent")
	}
}

func TestParseCSVRejectsUnknownFormat(t *testing.T) {
	if _, err := parseCSV(strings.NewReader("a,b\n1,2\n")); err == nil {
		t.Error("expected an error for a CSV without reach columns")
	}
	if rows, err := parseCSV(strings.NewReader("")); err != nil || rows != nil {
		t.Errorf("empty file: %v %v", rows, err)
	}
}

func TestFilesArePrivate(t *testing.T) {
	fg := fakegoogle.New()
	defer fg.Close()
	dir := t.TempDir()
	c, _ := Open(dir)
	if _, err := c.Sync(context.Background(), client(fg), 0); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "index.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("index.json mode = %v, %v", fi.Mode(), err)
	}
}

// stubAPI returns fixed jobs and a fixed error from Reports.
type stubAPI struct {
	jobs    []youtube.Job
	reports error
}

func (s *stubAPI) Jobs(context.Context) ([]youtube.Job, error) { return s.jobs, nil }
func (s *stubAPI) CreateJob(_ context.Context, rt, _ string) (*youtube.Job, error) {
	return &youtube.Job{ID: "new-" + rt, ReportTypeID: rt, CreateTime: time.Now()}, nil
}
func (s *stubAPI) Reports(context.Context, string) ([]youtube.Report, error) { return nil, s.reports }
func (s *stubAPI) Download(context.Context, string) ([]byte, error)          { return nil, nil }

func TestSyncToleratesUnavailableNewJobs(t *testing.T) {
	unavailable := &youtube.APIError{Status: 503, Message: "The service is currently unavailable."}
	young := []youtube.Job{{ID: "a", ReportTypeID: Basic, CreateTime: time.Now()}, {ID: "b", ReportTypeID: Combined, CreateTime: time.Now()}}
	c, _ := Open(t.TempDir())
	if _, err := c.Sync(context.Background(), &stubAPI{jobs: young, reports: unavailable}, 0); err != nil {
		t.Errorf("a 503 from a job created minutes ago means no reports yet, not an error: %v", err)
	}
	if c.Status().LastSync.IsZero() {
		t.Error("a clean sync should record LastSync")
	}

	old := []youtube.Job{{ID: "a", ReportTypeID: Basic, CreateTime: time.Now().AddDate(0, 0, -10)}, {ID: "b", ReportTypeID: Combined, CreateTime: time.Now().AddDate(0, 0, -10)}}
	c2, _ := Open(t.TempDir())
	if _, err := c2.Sync(context.Background(), &stubAPI{jobs: old, reports: unavailable}, 0); err == nil {
		t.Error("a 503 from an old job should be reported")
	}
	if !c2.Status().LastSync.IsZero() {
		t.Error("a failed sync must not record LastSync, so the next call retries")
	}
}
