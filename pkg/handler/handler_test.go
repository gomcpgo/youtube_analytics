package handler

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gomcpgo/mcp/pkg/protocol"
	"github.com/gomcpgo/youtube_analytics/internal/fakegoogle"
	"github.com/gomcpgo/youtube_analytics/pkg/auth"
	"github.com/gomcpgo/youtube_analytics/pkg/config"
	"github.com/gomcpgo/youtube_analytics/pkg/youtube"
	"golang.org/x/oauth2"
)

func setup(t *testing.T) (*Handler, *fakegoogle.Server) {
	t.Helper()
	fg := fakegoogle.New()
	t.Cleanup(fg.Close)
	old := youtube.BaseURLs
	youtube.BaseURLs.Data, youtube.BaseURLs.Analytics, youtube.BaseURLs.Reporting = fg.URL+"/youtube/v3", fg.URL+"/v2", fg.URL+"/v1"
	t.Cleanup(func() { youtube.BaseURLs = old })

	dir := t.TempDir()
	store, err := auth.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	tok := &oauth2.Token{AccessToken: "tok", RefreshToken: "ref", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)}
	if err := store.Put(auth.Account{ChannelID: fakegoogle.ChannelID, Title: "Fake Channel", Handle: "@fakechannel", Token: tok}); err != nil {
		t.Fatal(err)
	}
	h, err := New(&config.Config{ClientID: "id", ClientSecret: "secret", DataDir: dir, Timeout: 10 * time.Second}, func(s string) { t.Log(s) })
	if err != nil {
		t.Fatal(err)
	}
	return h, fg
}

func call(t *testing.T, h *Handler, name string, a map[string]interface{}) (string, *protocol.CallToolResponse) {
	t.Helper()
	resp, err := h.CallTool(context.Background(), &protocol.CallToolRequest{Name: name, Arguments: a})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	text := resp.Content[0].Text
	if resp.IsError {
		t.Fatalf("%s returned an error: %s", name, text)
	}
	t.Logf("%s %v:\n%s", name, a, text)
	return text, resp
}

func TestToolsEndToEnd(t *testing.T) {
	h, _ := setup(t)
	pt := youtube.Pacific
	today := time.Now().In(pt)
	cases := []struct {
		name   string
		args   map[string]interface{}
		want   []string
		absent []string
	}{
		{"list_channels", nil, []string{"Fake Channel", "4,321", "not scheduled yet"}, nil},
		// Two cached days x (1000 @ 5% + 500 @ 2%): 3,000 impressions, 120 clicks = 4.0%.
		// The superseded backfill file (999,999 impressions) must be ignored.
		{"channel_overview", map[string]interface{}{"include_revenue": true}, []string{"| Impressions | 3,000 |", "| Impressions CTR | 4.0% |", "## Formats", "## Top videos", "How to fake a video", "## Revenue"}, []string{"999"}},
		{"list_videos", map[string]interface{}{"limit": 2}, []string{fakegoogle.Vid1, fakegoogle.Vid2, "10:00"}, []string{fakegoogle.Vid3}},
		{"list_videos", map[string]interface{}{"query": "SHORT"}, []string{"Fake short"}, []string{"How to fake"}},
		{"video_performance", map[string]interface{}{"sort": "ctr"}, []string{"sorted by ctr", "5.0%", "2.0%"}, nil},
		{"video_performance", map[string]interface{}{"video_ids": []interface{}{fakegoogle.Vid1, "https://youtu.be/" + fakegoogle.Vid2}}, []string{"How to fake a video", "Fake short"}, nil},
		{"video_performance", map[string]interface{}{"content_type": "shorts"}, []string{"Fake short"}, []string{"How to fake"}},
		{"video_report", map[string]interface{}{"video_id": "https://www.youtube.com/watch?v=" + fakegoogle.Vid1}, []string{"Launch curve", "## Traffic sources", "how to fake", "Retention summary", "Channel benchmark", "Impressions CTR", "Subscribers vs non-subscribers"}, []string{"Partial results"}},
		// The fake curve drops 15 points at 40% of a 10:00 video (4:00-4:06).
		{"video_retention", map[string]interface{}{"video_id": fakegoogle.Vid1, "audience_type": "organic"}, []string{"Steepest drop-offs after the intro: 4:00–4:06", "Rewatch spikes", "Most common exit points: 4:06 (", "Below typical", "audienceType=ORGANIC"}, nil},
		{"traffic_sources", nil, []string{"YouTube search", "Browse features", "Impressions and CTR by source", "Shorts feed"}, nil},
		{"traffic_sources", map[string]interface{}{"detail_for": "RELATED_VIDEO", "video_id": fakegoogle.Vid1}, []string{"Fake short (" + fakegoogle.Vid2 + ")"}, nil},
		{"audience", nil, []string{"Age and gender", "18–24", "Top countries", "Devices", "Subscribers vs non-subscribers", "## Formats"}, []string{"Unavailable"}},
		{"timeline", map[string]interface{}{"period": "7d"}, []string{"Day series", "Impressions", today.AddDate(0, 0, -4).Format("2006-01-02")}, nil},
		{"timeline", map[string]interface{}{"period": "365d", "granularity": "month"}, []string{"Month series", "2026-08"}, nil},
		{"impressions_ctr", nil, []string{"How to fake a video", "| 2,000 |", "Total: 3,000 impressions, CTR 4.0%"}, nil},
		{"impressions_ctr", map[string]interface{}{"group_by": "traffic_source"}, []string{"Browse features", "YouTube search", "Shorts feed"}, nil},
		{"impressions_ctr", map[string]interface{}{"video_id": fakegoogle.Vid2}, []string{"Total: 1,000 impressions, CTR 2.0%"}, nil},
		{"video_comments", map[string]interface{}{"unanswered_only": true}, []string{"Great video"}, []string{"Loved it", "Pinned"}},
		{"list_channels", nil, []string{"2 days (" + today.AddDate(0, 0, -5).Format("2006-01-02")}, nil},
		{"analytics_query", map[string]interface{}{"start_date": "2026-01-01", "end_date": "2026-01-31", "metrics": []interface{}{"views"}, "dimensions": []interface{}{"country"}}, []string{"| US |"}, nil},
	}
	for _, c := range cases {
		text, _ := call(t, h, c.name, c.args)
		for _, w := range c.want {
			if !strings.Contains(text, w) {
				t.Errorf("%s %v: missing %q", c.name, c.args, w)
			}
		}
		for _, w := range c.absent {
			if strings.Contains(text, w) {
				t.Errorf("%s %v: unexpected %q", c.name, c.args, w)
			}
		}
	}
}

func TestFallbacks(t *testing.T) {
	h, fg := setup(t)
	// Reject multi-video filters and engagedViews everywhere.
	fg.Reject = func(q url.Values) bool {
		f, m := q.Get("filters"), q.Get("metrics")
		return strings.Contains(f, "video==") && strings.Contains(f, ",") || strings.Contains(m, "engagedViews")
	}
	text, _ := call(t, h, "video_performance", map[string]interface{}{"video_ids": []interface{}{fakegoogle.Vid1, fakegoogle.Vid2}})
	if !strings.Contains(text, "How to fake a video") || !strings.Contains(text, "Fake short") {
		t.Errorf("per-video fallback did not return both videos")
	}
	text, _ = call(t, h, "channel_overview", nil)
	if strings.Contains(text, "Engaged views") || !strings.Contains(text, "| Views |") {
		t.Errorf("overview should drop engagedViews and still report views")
	}
}

func TestErrors(t *testing.T) {
	h, _ := setup(t)
	for _, c := range []struct {
		name string
		args map[string]interface{}
		want string
	}{
		{"video_retention", nil, "video_id is required"},
		{"video_report", map[string]interface{}{"video_id": "not a video"}, "not a YouTube video ID"},
		{"channel_overview", map[string]interface{}{"channel": "Other"}, "not connected"},
		{"channel_overview", map[string]interface{}{"period": "fortnight"}, "unknown period"},
		{"video_performance", map[string]interface{}{"sort": "rpm"}, "unknown sort"},
		{"traffic_sources", map[string]interface{}{"detail_for": "END_SCREEN"}, "detail is not available"},
	} {
		resp, _ := h.CallTool(context.Background(), &protocol.CallToolRequest{Name: c.name, Arguments: c.args})
		if !resp.IsError || !strings.Contains(resp.Content[0].Text, c.want) {
			t.Errorf("%s %v: want error containing %q, got %q", c.name, c.args, c.want, resp.Content[0].Text)
		}
	}
}

func TestNoClientConfigured(t *testing.T) {
	h, err := New(&config.Config{DataDir: t.TempDir(), Timeout: time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := h.CallTool(context.Background(), &protocol.CallToolRequest{Name: "connect_channel", Arguments: map[string]interface{}{}})
	if !resp.IsError || !strings.Contains(resp.Content[0].Text, "YOUTUBE_OAUTH_CLIENT_ID") {
		t.Errorf("got %q", resp.Content[0].Text)
	}
	text, _ := call(t, h, "list_channels", nil)
	if !strings.Contains(text, "not configured") || !strings.Contains(text, "connect_channel") {
		t.Errorf("list_channels should explain setup, got %q", text)
	}
}
