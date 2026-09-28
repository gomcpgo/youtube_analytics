// Package fakegoogle is an in-process stand-in for the YouTube Data,
// Analytics and Reporting APIs, used by tests. It enforces the documented
// query constraints this server relies on (sort/maxResults limits, month
// dates, single-video retention) so wiring mistakes fail loudly.
package fakegoogle

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Video IDs served by the fake.
const (
	Vid1      = "AAAAAAAAAA1" // 10 min video
	Vid2      = "BBBBBBBBBB2" // 45 s short
	Vid3      = "CCCCCCCCCC3" // 1 h live replay
	ChannelID = "UCfakechannel"
)

// Server is the fake API.
type Server struct {
	*httptest.Server

	mu sync.Mutex
	// Jobs are the existing reporting jobs, keyed by report type.
	Jobs map[string]string
	// Queries records every analytics query.
	Queries []url.Values
	// Reject makes an analytics query fail with 400 when it returns true.
	Reject func(url.Values) bool
	// Days are the dates (YYYY-MM-DD) reach reports exist for.
	Days []string
}

// New starts a fake with reach reports for the 5th and 4th days before today.
func New() *Server {
	pt, _ := time.LoadLocation("America/Los_Angeles")
	today := time.Now().In(pt)
	s := &Server{Jobs: map[string]string{}}
	for _, back := range []int{5, 4} {
		s.Days = append(s.Days, today.AddDate(0, 0, -back).Format("2006-01-02"))
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/youtube/v3/channels", s.channels)
	mux.HandleFunc("/youtube/v3/playlistItems", s.playlistItems)
	mux.HandleFunc("/youtube/v3/videos", s.videos)
	mux.HandleFunc("/youtube/v3/commentThreads", s.comments)
	mux.HandleFunc("/v2/reports", s.analytics)
	mux.HandleFunc("/v1/jobs", s.jobs)
	mux.HandleFunc("/v1/jobs/", s.reports)
	mux.HandleFunc("/media/", s.media)
	s.Server = httptest.NewServer(mux)
	return s
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	writeJSON(w, map[string]interface{}{"error": map[string]interface{}{"code": code, "message": msg, "errors": []map[string]string{{"reason": "badRequest"}}}})
}

func (s *Server) channels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{"items": []interface{}{map[string]interface{}{
		"id":             ChannelID,
		"snippet":        map[string]interface{}{"title": "Fake Channel", "customUrl": "@fakechannel", "publishedAt": "2020-01-15T10:00:00Z"},
		"statistics":     map[string]interface{}{"viewCount": "123456", "subscriberCount": "4321", "videoCount": "3"},
		"contentDetails": map[string]interface{}{"relatedPlaylists": map[string]string{"uploads": "UUfake"}},
	}}})
}

func (s *Server) playlistItems(w http.ResponseWriter, r *http.Request) {
	var items []interface{}
	for _, id := range []string{Vid1, Vid2, Vid3} {
		items = append(items, map[string]interface{}{"contentDetails": map[string]string{"videoId": id}})
	}
	writeJSON(w, map[string]interface{}{"items": items})
}

var videoMeta = map[string][3]string{ // title, duration, publishedAt
	Vid1: {"How to fake a video", "PT10M", "2026-06-01T15:00:00Z"},
	Vid2: {"Fake short", "PT45S", "2026-07-01T15:00:00Z"},
	Vid3: {"Fake live stream", "PT1H2M3S", "2026-08-01T15:00:00Z"},
}

func (s *Server) videos(w http.ResponseWriter, r *http.Request) {
	var items []interface{}
	for _, id := range strings.Split(r.URL.Query().Get("id"), ",") {
		m, ok := videoMeta[id]
		if !ok {
			continue
		}
		items = append(items, map[string]interface{}{
			"id":             id,
			"snippet":        map[string]interface{}{"title": m[0], "publishedAt": m[2], "tags": []string{"fake"}, "thumbnails": map[string]interface{}{"high": map[string]string{"url": "https://i.ytimg.com/" + id}}},
			"contentDetails": map[string]string{"duration": m[1], "caption": "true"},
			"statistics":     map[string]string{"viewCount": "1000", "likeCount": "50", "commentCount": "7"},
			"status":         map[string]interface{}{"privacyStatus": "public"},
		})
	}
	writeJSON(w, map[string]interface{}{"items": items})
}

func (s *Server) comments(w http.ResponseWriter, r *http.Request) {
	thread := func(id, text, author, authorID string, ownerReply bool) map[string]interface{} {
		sn := map[string]interface{}{"authorDisplayName": author, "authorChannelId": map[string]string{"value": authorID}, "textDisplay": text, "likeCount": 3, "publishedAt": "2026-09-01T10:00:00Z"}
		t := map[string]interface{}{"id": id, "snippet": map[string]interface{}{"videoId": Vid1, "totalReplyCount": 0, "topLevelComment": map[string]interface{}{"snippet": sn}}}
		if ownerReply {
			t["snippet"].(map[string]interface{})["totalReplyCount"] = 1
			t["replies"] = map[string]interface{}{"comments": []interface{}{map[string]interface{}{"snippet": map[string]interface{}{"authorChannelId": map[string]string{"value": ChannelID}, "textDisplay": "thanks"}}}}
		}
		return t
	}
	writeJSON(w, map[string]interface{}{"items": []interface{}{
		thread("c1", "Great video, how did you do X?", "Viewer One", "UCviewer1", false),
		thread("c2", "Loved it", "Viewer Two", "UCviewer2", true),
		thread("c3", "Pinned: links below", "Fake Channel", ChannelID, false),
	}})
}

// analytics fakes /v2/reports.
func (s *Server) analytics(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	s.Queries = append(s.Queries, q)
	reject := s.Reject
	s.mu.Unlock()
	if reject != nil && reject(q) {
		apiError(w, 400, "The query is not supported.")
		return
	}
	dims := split(q.Get("dimensions"), ",")
	metrics := split(q.Get("metrics"), ",")
	if msg := validate(q, dims); msg != "" {
		apiError(w, 400, msg)
		return
	}
	keys := dimensionValues(dims, q)
	var headers []map[string]string
	for _, d := range dims {
		headers = append(headers, map[string]string{"name": d, "columnType": "DIMENSION", "dataType": "STRING"})
	}
	for _, m := range metrics {
		headers = append(headers, map[string]string{"name": m, "columnType": "METRIC", "dataType": "FLOAT"})
	}
	var rows [][]interface{}
	for i, k := range keys {
		var row []interface{}
		for _, v := range k {
			row = append(row, v)
		}
		for _, m := range metrics {
			row = append(row, metricValue(m, i, len(keys)))
		}
		rows = append(rows, row)
	}
	writeJSON(w, map[string]interface{}{"kind": "youtubeAnalytics#resultTable", "columnHeaders": headers, "rows": rows})
}

func validate(q url.Values, dims []string) string {
	has := func(d string) bool {
		for _, x := range dims {
			if x == d {
				return true
			}
		}
		return false
	}
	max := 0
	fmt.Sscanf(q.Get("maxResults"), "%d", &max)
	switch {
	case q.Get("ids") != "channel==MINE":
		return "ids must be channel==MINE"
	case q.Get("startDate") == "" || q.Get("endDate") == "" || q.Get("metrics") == "":
		return "startDate, endDate and metrics are required"
	case q.Get("startDate") > q.Get("endDate"):
		return "startDate is after endDate"
	case has("video") && (q.Get("sort") == "" || max < 1 || max > 200):
		return "video dimension requires sort and maxResults <= 200"
	case has("insightTrafficSourceDetail") && (q.Get("sort") == "" || max < 1 || max > 25 || !strings.Contains(q.Get("filters"), "insightTrafficSourceType==")):
		return "traffic source detail requires sort, maxResults <= 25 and an insightTrafficSourceType filter"
	case has("month") && (!strings.HasSuffix(q.Get("startDate"), "-01") || !strings.HasSuffix(q.Get("endDate"), "-01")):
		return "month dimension requires first-of-month dates"
	case has("elapsedVideoTimeRatio") && (!strings.Contains(q.Get("filters"), "video==") || strings.Contains(q.Get("filters"), ",")):
		return "retention requires a single video filter"
	}
	return ""
}

func dimensionValues(dims []string, q url.Values) [][]interface{} {
	one := func(vals ...interface{}) [][]interface{} {
		var out [][]interface{}
		for _, v := range vals {
			out = append(out, []interface{}{v})
		}
		return out
	}
	switch strings.Join(dims, ",") {
	case "":
		return [][]interface{}{{}}
	case "creatorContentType":
		return one("VIDEO_ON_DEMAND", "SHORTS")
	case "video,creatorContentType":
		return [][]interface{}{{Vid1, "VIDEO_ON_DEMAND"}, {Vid2, "SHORTS"}, {Vid3, "LIVE_STREAM"}}
	case "video":
		return one(Vid1, Vid2, Vid3)
	case "day":
		start, _ := time.Parse("2006-01-02", q.Get("startDate"))
		end, _ := time.Parse("2006-01-02", q.Get("endDate"))
		var out [][]interface{}
		for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
			out = append(out, []interface{}{d.Format("2006-01-02")})
		}
		return out
	case "month":
		return one("2026-07", "2026-08", "2026-09")
	case "insightTrafficSourceType":
		return one("SUBSCRIBER", "YT_SEARCH", "RELATED_VIDEO")
	case "insightTrafficSourceDetail":
		return one("how to fake", Vid2)
	case "elapsedVideoTimeRatio":
		var out [][]interface{}
		for i := 1; i <= 100; i++ {
			out = append(out, []interface{}{float64(i) / 100})
		}
		return out
	case "ageGroup,gender":
		return [][]interface{}{{"age18-24", "male"}, {"age18-24", "female"}, {"age25-34", "male"}}
	case "country":
		return one("US", "IN")
	case "subscribedStatus":
		return one("UNSUBSCRIBED", "SUBSCRIBED")
	case "deviceType":
		return one("MOBILE", "DESKTOP", "TV")
	}
	var k []interface{}
	for range dims {
		k = append(k, "X")
	}
	return [][]interface{}{k}
}

// metricValue returns plausible values; retention metrics trace a curve
// with a sharp drop at 40% and a rewatch bump at 60%.
func metricValue(m string, i, n int) float64 {
	ratio := float64(i+1) / 100
	switch m {
	case "audienceWatchRatio":
		w := 1.0 - 0.3*math.Min(ratio/0.1, 1) - 0.2*ratio
		if ratio > 0.40 {
			w -= 0.15
		}
		if ratio > 0.60 && ratio <= 0.63 {
			w += 0.05
		}
		return math.Round(w*1000) / 1000
	case "relativeRetentionPerformance":
		if ratio > 0.40 && ratio < 0.50 {
			return 0.2
		}
		return 0.55
	case "stoppedWatching":
		if math.Abs(ratio-0.41) < 0.001 {
			return 300
		}
		return 10
	case "startedWatching":
		return 5
	case "averageViewPercentage":
		return 40 + float64(i)
	case "averageViewDuration":
		return 120 + 10*float64(i)
	case "viewerPercentage":
		return []float64{30, 20, 50}[i%3]
	case "estimatedMinutesWatched":
		return float64(3000 - 100*i)
	}
	return math.Max(0, float64(1000-10*i))
}

func split(s, sep string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, sep)
}

// --- Reporting API ---

func (s *Server) jobs(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method == http.MethodPost {
		var body struct {
			ReportTypeID string `json:"reportTypeId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		id := "job-" + body.ReportTypeID
		s.Jobs[body.ReportTypeID] = id
		writeJSON(w, map[string]interface{}{"id": id, "reportTypeId": body.ReportTypeID, "createTime": time.Now().UTC().Add(-72 * time.Hour).Format(time.RFC3339)})
		return
	}
	var jobs []interface{}
	for rt, id := range s.Jobs {
		jobs = append(jobs, map[string]interface{}{"id": id, "reportTypeId": rt, "createTime": time.Now().UTC().Add(-72 * time.Hour).Format(time.RFC3339)})
	}
	writeJSON(w, map[string]interface{}{"jobs": jobs})
}

// reports lists, per job, one report per day plus an older superseded
// version of the last day (a backfill).
func (s *Server) reports(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/jobs/"), "/reports")
	pt, _ := time.LoadLocation("America/Los_Angeles")
	var reports []interface{}
	for i, d := range s.Days {
		day, _ := time.ParseInLocation("2006-01-02", d, pt)
		rep := func(id string, created time.Time) map[string]interface{} {
			return map[string]interface{}{"id": id, "jobId": jobID, "startTime": day.UTC().Format(time.RFC3339), "endTime": day.AddDate(0, 0, 1).UTC().Format(time.RFC3339),
				"createTime": created.UTC().Format(time.RFC3339), "downloadUrl": s.URL + "/media/" + jobID + "/" + d + "/" + id}
		}
		reports = append(reports, rep(fmt.Sprintf("r%d-new", i), day.AddDate(0, 0, 3)))
		if i == len(s.Days)-1 {
			reports = append(reports, rep(fmt.Sprintf("r%d-old", i), day.AddDate(0, 0, 2)))
		}
	}
	writeJSON(w, map[string]interface{}{"reports": reports})
}

// media serves report CSVs. CTR is a percentage; the superseded ("-old")
// version carries impressions that must not be used.
func (s *Server) media(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/media/"), "/")
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}
	job, day, id := parts[0], strings.ReplaceAll(parts[1], "-", ""), parts[2]
	imp := 1000
	if strings.HasSuffix(id, "-old") {
		imp = 999999
	}
	w.Header().Set("Content-Type", "text/csv")
	if strings.Contains(job, "combined") {
		fmt.Fprintf(w, "date,channel_id,video_id,traffic_source_type,traffic_source_detail,operating_system,device_type,video_thumbnail_impressions,video_thumbnail_impressions_ctr\n")
		fmt.Fprintf(w, "%s,%s,%s,3,,2,104,%d,5.0\n", day, ChannelID, Vid1, imp*8/10)
		fmt.Fprintf(w, "%s,%s,%s,5,,2,101,%d,5.0\n", day, ChannelID, Vid1, imp*2/10)
		fmt.Fprintf(w, "%s,%s,%s,24,,2,104,%d,2.0\n", day, ChannelID, Vid2, imp/2)
		return
	}
	fmt.Fprintf(w, "date,channel_id,video_id,video_thumbnail_impressions,video_thumbnail_impressions_ctr\n")
	fmt.Fprintf(w, "%s,%s,%s,%d,5.0\n", day, ChannelID, Vid1, imp)
	fmt.Fprintf(w, "%s,%s,%s,%d,2.0\n", day, ChannelID, Vid2, imp/2)
}
