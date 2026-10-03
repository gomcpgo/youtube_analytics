package handler

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/gomcpgo/mcp/pkg/protocol"
	"github.com/gomcpgo/youtube_analytics/pkg/analytics"
	"github.com/gomcpgo/youtube_analytics/pkg/auth"
	"github.com/gomcpgo/youtube_analytics/pkg/reach"
	"github.com/gomcpgo/youtube_analytics/pkg/youtube"
)

// Connect runs the consent flow from the terminal (-auth), printing the URL
// before waiting up to 10 minutes.
func (h *Handler) Connect(ctx context.Context, printf func(string, ...interface{})) error {
	res, err := h.accounts.Connect(ctx, 0, true)
	if err != nil {
		return err
	}
	printf("Opening the Google consent page (browser opened: %v). If it did not open, visit:\n\n%s\n\nWaiting up to 10 minutes...\n", res.BrowserOpened, res.URL)
	res, err = h.accounts.Connect(ctx, 10*time.Minute, false)
	if err != nil {
		return err
	}
	if !res.Completed {
		return fmt.Errorf("timed out waiting for authorization")
	}
	printf("Connected %s (%s, %s).\n%s\n", res.Account.Title, res.Account.Handle, res.Account.ChannelID, h.afterConnect(ctx, res.Account))
	return nil
}

// afterConnect makes sure the reach jobs exist and describes their state.
func (h *Handler) afterConnect(ctx context.Context, a auth.Account) string {
	c, err := h.accounts.Client(a)
	if err == nil {
		_, err = h.scheduleReach(ctx, a, c)
	}
	if err != nil {
		return "Could not schedule the impressions/CTR reach reports yet (" + err.Error() + "); they are retried on the next impressions query."
	}
	rc, err := h.reachCache(a.ChannelID)
	if err != nil {
		return ""
	}
	st := rc.Status()
	if st.Days > 0 {
		return fmt.Sprintf("Impressions/CTR data is cached for %d days (%s to %s).", st.Days, st.First, st.Last)
	}
	if time.Now().Before(st.FirstDataBy) {
		return "YouTube's daily reach reports (impressions and CTR) are scheduled. The first files arrive by about " +
			st.FirstDataBy.In(youtube.Pacific).Format("Jan 2 15:04 MST") + " and include the previous 30 days."
	}
	return "YouTube's daily reach reports (impressions and CTR) are scheduled; files are downloaded on the next impressions query."
}

func (h *Handler) connectChannel(ctx context.Context, a args) (*protocol.CallToolResponse, error) {
	wait := time.Duration(a.intv("wait_seconds", 90, 0, 300)) * time.Second
	res, err := h.accounts.Connect(ctx, wait, a.boolv("open_browser", true))
	if err != nil {
		return nil, err
	}
	if !res.Completed {
		opened := "The consent page was opened in the default browser."
		if !res.BrowserOpened {
			opened = "Open this link in a browser:"
		}
		return textResponse(fmt.Sprintf(`Waiting for Google sign-in (not finished yet). %s

%s

Tell the user:
- The link stays valid for 10 minutes; calling connect_channel again reopens the same sign-in.
- "Google hasn't verified this app" is expected for their own OAuth project: click Advanced, then "Go to (app name) (unsafe)".
- "Access blocked: (app) has not completed the Google verification process" means the OAuth app is still in Testing: in Google Cloud Console open Google Auth Platform > Audience and click "Publish app" (or add their Google account under Test users).
- For a Google account with several channels, pick the channel on the consent screen.
After they approve, call list_channels to confirm.`, opened, res.URL)), nil
	}
	acct := res.Account
	reachMsg := h.afterConnect(ctx, acct)
	return textResponse(fmt.Sprintf("Connected %s (%s, channel ID %s). %s", acct.Title, acct.Handle, acct.ChannelID, reachMsg)), nil
}

type channelStatus struct {
	ChannelID string           `json:"channel_id"`
	Title     string           `json:"title"`
	Handle    string           `json:"handle,omitempty"`
	Channel   *youtube.Channel `json:"channel,omitempty"`
	Error     string           `json:"error,omitempty"`
	Reach     *reach.Status    `json:"reach,omitempty"`
}

func (h *Handler) listChannels(ctx context.Context, a args) (*protocol.CallToolResponse, error) {
	accts := h.accounts.Accounts()
	var b strings.Builder
	if !h.cfg.HasClient() {
		b.WriteString("The Google OAuth client is not configured (YOUTUBE_OAUTH_CLIENT_ID / YOUTUBE_OAUTH_CLIENT_SECRET), so no channel can be queried. See the README.\n")
		if hint := h.cfg.SetupHint(); hint != "" {
			b.WriteString(hint + "\n")
		}
		b.WriteString("\n")
	}
	if len(accts) == 0 {
		b.WriteString("No channels connected yet. Call connect_channel to sign in with Google.\n")
		return textResponse(b.String()), nil
	}
	statuses := make([]channelStatus, len(accts))
	var fns []func()
	for i, acct := range accts {
		i, acct := i, acct
		fns = append(fns, func() {
			st := channelStatus{ChannelID: acct.ChannelID, Title: acct.Title, Handle: acct.Handle}
			if c, err := h.accounts.Client(acct); err != nil {
				st.Error = err.Error()
			} else if ch, err := c.MyChannel(ctx); err != nil {
				st.Error = err.Error()
			} else {
				st.Channel = ch
			}
			if rc, err := h.reachCache(acct.ChannelID); err == nil {
				s := rc.Status()
				st.Reach = &s
			}
			statuses[i] = st
		})
	}
	runAll(fns)

	rows := make([][]string, 0, len(statuses))
	for _, st := range statuses {
		health, subs, views, videos := "ok", "—", "—", "—"
		if st.Error != "" {
			health = "needs attention: " + truncate(st.Error, 160)
		}
		if ch := st.Channel; ch != nil {
			subs, views, videos = count(float64(ch.Subscribers)), count(float64(ch.Views)), count(float64(ch.Videos))
			if ch.HiddenSubscribers {
				subs += " (hidden)"
			}
		}
		rows = append(rows, []string{st.Title, st.Handle, st.ChannelID, subs, views, videos, reachLine(st.Reach), health})
	}
	b.WriteString(table([]string{"Channel", "Handle", "ID", "Subscribers", "Views", "Videos", "Impressions/CTR data", "Status"}, rows))
	return textResponse(b.String()), nil
}

func reachLine(s *reach.Status) string {
	switch {
	case s == nil || len(s.Jobs) == 0:
		return "not scheduled yet"
	case s.Days == 0 && time.Now().Before(s.FirstDataBy):
		return "scheduled; first data by " + s.FirstDataBy.In(youtube.Pacific).Format("Jan 2 15:04 MST")
	case s.Days == 0:
		return "scheduled; no files synced yet"
	}
	return fmt.Sprintf("%d days (%s to %s)", s.Days, s.First, s.Last)
}

func (h *Handler) channelOverview(ctx context.Context, a args) (*protocol.CallToolResponse, error) {
	svc, _, err := h.session(a)
	if err != nil {
		return nil, err
	}
	o, err := svc.Overview(ctx, a.period(), analytics.OverviewOptions{
		Compare: a.boolv("compare", true), Revenue: a.boolv("include_revenue", false), Currency: strings.ToUpper(a.str("currency")),
	})
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	ch := o.Channel
	fmt.Fprintf(&b, "# %s: %s\n", ch.Title, periodLine(o.Period))
	if o.Previous != nil {
		fmt.Fprintf(&b, "Compared with %s to %s.\n", o.Previous.Start, o.Previous.End)
	}
	fmt.Fprintf(&b, "Lifetime: %s subscribers, %s views, %s videos.\n\n", count(float64(ch.Subscribers)), count(float64(ch.Views)), count(float64(ch.Videos)))
	for _, n := range o.Notes {
		b.WriteString(n + "\n\n")
	}
	b.WriteString(deltaTable(o.Metrics, o.Previous != nil))

	if len(o.Formats) > 0 {
		b.WriteString("\n## Formats\n")
		var rows [][]string
		for _, f := range o.Formats {
			rows = append(rows, []string{f.Format, count(f.Views), percent(f.ViewShare), hours(f.WatchMinutes), clock(f.AvgViewDurationSec), percent(f.AvgViewPct), changeStr(f.ChangePct)})
		}
		b.WriteString(table([]string{"Format", "Views", "Share", "Watch time", "Avg duration", "Avg % viewed", "Views change"}, rows))
	}
	if len(o.TopVideos) > 0 {
		b.WriteString("\n## Top videos\n")
		var rows [][]string
		for i, v := range o.TopVideos {
			rows = append(rows, []string{fmt.Sprint(i + 1), truncate(v.Title, 60), v.ID, v.Format, count(v.Views), hours(v.WatchMinutes), percent(v.AvgViewPct), optPct(v.CTR), count(v.SubsGained)})
		}
		b.WriteString(table([]string{"#", "Title", "ID", "Format", "Views", "Watch time", "Avg % viewed", "CTR", "Subs gained"}, rows))
	}
	if len(o.Revenue) > 0 {
		b.WriteString("\n## Revenue\n")
		if allZero(o.Revenue) {
			b.WriteString("All revenue metrics are zero: the channel is not monetized (YouTube Partner Program) or earned nothing in this period.\n")
		} else {
			b.WriteString(deltaTable(o.Revenue, o.Previous != nil))
		}
	}
	b.WriteString("\n" + o.Reach.Note + "\n")
	b.WriteString(errorsSection(o.Errors))
	return textResponse(b.String()), nil
}

func allZero(ds []analytics.Delta) bool {
	for _, d := range ds {
		if d.Current != 0 || d.Previous != nil && *d.Previous != 0 {
			return false
		}
	}
	return true
}

func deltaTable(ds []analytics.Delta, compare bool) string {
	var rows [][]string
	for _, d := range ds {
		r := []string{d.Label, kindValue(d.Kind, d.Current)}
		if compare {
			prev := "—"
			if d.Previous != nil {
				prev = kindValue(d.Kind, *d.Previous)
			}
			r = append(r, prev, changeStr(d.ChangePct))
		}
		rows = append(rows, r)
	}
	if compare {
		return table([]string{"Metric", "This period", "Previous", "Change"}, rows)
	}
	return table([]string{"Metric", "Value"}, rows)
}

func (h *Handler) listVideos(ctx context.Context, a args) (*protocol.CallToolResponse, error) {
	svc, _, err := h.session(a)
	if err != nil {
		return nil, err
	}
	l, err := svc.ListVideos(ctx, a.intv("limit", 50, 1, 500), a.str("query"))
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d videos", l.Channel.Title, len(l.Videos))
	if q := a.str("query"); q != "" {
		fmt.Fprintf(&b, " matching %q (scanned %d uploads)", q, l.Scanned)
	}
	b.WriteString(". Counts are lifetime public totals.\n\n")
	var rows [][]string
	for _, v := range l.Videos {
		rows = append(rows, []string{v.PublishedAt.In(youtube.Pacific).Format("2006-01-02"), truncate(v.Title, 70), v.ID, clock(float64(v.DurationSec)),
			count(float64(v.Views)), count(float64(v.Likes)), count(float64(v.Comments)), v.Privacy})
	}
	b.WriteString(table([]string{"Published", "Title", "ID", "Length", "Views", "Likes", "Comments", "Privacy"}, rows))
	return textResponse(b.String()), nil
}

func (h *Handler) comments(ctx context.Context, a args) (*protocol.CallToolResponse, error) {
	svc, acct, err := h.session(a)
	if err != nil {
		return nil, err
	}
	vid, err := a.videoID(false)
	if err != nil {
		return nil, err
	}
	order := a.str("order")
	if order != "relevance" {
		order = "time"
	}
	l, err := svc.Comments(ctx, h.cfg.APIKey, acct.ChannelID, vid, order, a.intv("limit", 30, 1, 100), a.boolv("unanswered_only", false))
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	scope := "across the channel"
	if vid != "" {
		scope = "on video " + vid
	}
	fmt.Fprintf(&b, "%d comments %s (order: %s).\n\n", len(l.Comments), scope, order)
	for _, c := range l.Comments {
		meta := fmt.Sprintf("%s · %s · %s likes · %d replies", c.PublishedAt.In(youtube.Pacific).Format("2006-01-02"), c.Author, count(float64(c.Likes)), c.Replies)
		if c.OwnerReplied {
			meta += " · you replied"
		}
		if vid == "" {
			meta += " · on \"" + truncate(l.Titles[c.VideoID], 50) + "\" (" + c.VideoID + ")"
		}
		fmt.Fprintf(&b, "- %s\n  %s\n", meta, truncate(c.Text, 500))
	}
	return textResponse(b.String()), nil
}

func (h *Handler) rawQuery(ctx context.Context, a args) (*protocol.CallToolResponse, error) {
	acct, err := h.accounts.Resolve(a.str("channel"))
	if err != nil {
		return nil, err
	}
	yt, err := h.accounts.Client(acct)
	if err != nil {
		return nil, err
	}
	q := youtube.Query{Start: a.str("start_date"), End: a.str("end_date"), Metrics: a.strs("metrics"), Dimensions: a.strs("dimensions"),
		Filters: a.strs("filters"), Sort: a.str("sort"), MaxResults: a.intv("max_results", 0, 0, 200), Currency: strings.ToUpper(a.str("currency"))}
	if q.Start == "" || q.End == "" || len(q.Metrics) == 0 {
		return nil, fmt.Errorf("start_date, end_date and metrics are required")
	}
	t, err := yt.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	headers := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		headers[i] = c.Name
	}
	var rows [][]string
	for _, r := range t.Rows {
		row := make([]string, len(r))
		for i, v := range r {
			switch x := v.(type) {
			case float64:
				row[i] = count(math.Round(x*100) / 100)
			default:
				row[i] = fmt.Sprint(x)
			}
		}
		rows = append(rows, row)
	}
	text := fmt.Sprintf("%d rows.\n\n", len(rows)) + table(headers, rows)
	return textResponse(text), nil
}

func runAll(fns []func()) {
	done := make(chan struct{}, len(fns))
	for _, f := range fns {
		go func(f func()) {
			f()
			done <- struct{}{}
		}(f)
	}
	for range fns {
		<-done
	}
}
