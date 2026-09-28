package handler

import (
	"encoding/json"

	"github.com/gomcpgo/mcp/pkg/protocol"
)

func schema(s string) json.RawMessage { return json.RawMessage(s) }

func boolPtr(b bool) *bool { return &b }

var readOnly = &protocol.ToolAnnotations{ReadOnlyHint: boolPtr(true), DestructiveHint: boolPtr(false), IdempotentHint: boolPtr(true), OpenWorldHint: boolPtr(true)}

const channelParam = `"channel": {"type": "string", "description": "Which connected channel: channel ID, @handle or title. Optional when only one channel is connected."}`

const periodParams = `"period": {"type": "string", "enum": ["7d", "28d", "90d", "365d", "lifetime"], "default": "28d", "description": "Rolling window ending 3 days ago (YouTube Analytics data lags 2-3 days), or lifetime. Ignored when start_date is set."},
			"start_date": {"type": "string", "description": "Custom start, YYYY-MM-DD (Pacific time). Overrides period."},
			"end_date": {"type": "string", "description": "Custom end, YYYY-MM-DD. Defaults to today."}`

const videoParam = `"video_id": {"type": "string", "description": "Video ID or YouTube URL of one of the channel's videos (use list_videos to find it)"}`

// tools is the static tool list.
var tools = []protocol.Tool{
	{
		Name: "connect_channel",
		Description: "Connect a YouTube channel you own by signing in with Google in the browser. Opens the consent page and waits up to wait_seconds; " +
			"if the user has not finished by then, returns the link to open (valid 10 minutes) and they can call list_channels afterwards. " +
			"Run once per channel: for a Google account with several channels (brand accounts), pick the channel on the consent screen, then connect again for the next one. " +
			"Also schedules YouTube's daily reach reports for the channel, the only API source of impressions and click-through rate (first data about 48 hours later, with 30 days of history).",
		InputSchema: schema(`{"type": "object", "properties": {
			"wait_seconds": {"type": "integer", "minimum": 0, "maximum": 300, "default": 120, "description": "How long to wait for the user to finish in the browser"},
			"open_browser": {"type": "boolean", "default": true, "description": "Open the consent page in the default browser"}}}`),
		Annotations: &protocol.ToolAnnotations{ReadOnlyHint: boolPtr(false), DestructiveHint: boolPtr(false), IdempotentHint: boolPtr(false), OpenWorldHint: boolPtr(true)},
	},
	{
		Name:        "list_channels",
		Description: "List connected channels with live subscriber, view and video counts, authorization health, and the state of the impressions/CTR (reach) data. Call first to see what is connected.",
		InputSchema: schema(`{"type": "object", "properties": {}}`),
		Annotations: readOnly,
	},
	{
		Name: "channel_overview",
		Description: "Channel health for a period compared with the previous period of equal length: views, engaged views, watch time, average view duration, average percentage viewed, " +
			"impressions and impressions CTR, subscribers gained/lost/net and per 1K views, likes, dislikes, comments, shares, engagement rate, a Shorts vs videos vs live split, " +
			"the top 5 videos, and optionally revenue (YouTube Partner channels). Start here for any 'how is my channel doing' question.",
		InputSchema: schema(`{"type": "object", "properties": {
			` + channelParam + `,
			` + periodParams + `,
			"compare": {"type": "boolean", "default": true, "description": "Compare with the previous period"},
			"include_revenue": {"type": "boolean", "default": false, "description": "Add estimated revenue, CPM and ad metrics (monetized channels only)"},
			"currency": {"type": "string", "default": "USD", "description": "ISO 4217 currency for revenue"}}}`),
		Annotations: readOnly,
	},
	{
		Name:        "list_videos",
		Description: "List the channel's uploads, newest first, with video IDs, publish dates, durations, privacy and lifetime public counts (views, likes, comments). Use it to find video IDs, or pass query to search titles.",
		InputSchema: schema(`{"type": "object", "properties": {
			` + channelParam + `,
			"limit": {"type": "integer", "minimum": 1, "maximum": 500, "default": 50},
			"query": {"type": "string", "description": "Case-insensitive title filter (scans the latest 500 uploads)"}}}`),
		Annotations: readOnly,
	},
	{
		Name: "video_performance",
		Description: "Per-video table for a period: views, watch time, average view duration, average % viewed, impressions, CTR, subscribers gained, likes, comments, shares, engagement rate and subscribers per 1K views. " +
			"Rank the top videos by any of these, filter by format (shorts, videos, live), or pass video_ids to compare specific videos side by side. Use it to find winners and underperformers and what they have in common.",
		InputSchema: schema(`{"type": "object", "properties": {
			` + channelParam + `,
			` + periodParams + `,
			"sort": {"type": "string", "enum": ["views", "watch_time", "subscribers", "ctr", "impressions", "avg_view_percentage", "avg_view_duration", "engagement", "likes", "comments", "shares"], "default": "views"},
			"limit": {"type": "integer", "minimum": 1, "maximum": 200, "default": 25},
			"content_type": {"type": "string", "enum": ["all", "shorts", "videos", "live"], "default": "all"},
			"video_ids": {"type": "array", "items": {"type": "string"}, "maxItems": 50, "description": "Only these videos (IDs or URLs)"}}}`),
		Annotations: readOnly,
	},
	{
		Name: "video_report",
		Description: "Deep dive into one video (default: since publish): key metrics, launch curve (first day, first 7 days, peak, last 7 days), traffic sources, top YouTube search terms, " +
			"subscribers vs non-subscribers, retention summary (hook, steepest drop-offs, rewatched moments), impressions and CTR overall and by traffic source, " +
			"and the channel's 90-day averages for the same format as a benchmark. Use it to diagnose why a video did or did not perform.",
		InputSchema: schema(`{"type": "object", "properties": {
			` + channelParam + `,
			` + videoParam + `,
			"period": {"type": "string", "enum": ["7d", "28d", "90d", "365d", "lifetime"], "default": "lifetime", "description": "lifetime means since publish"},
			"start_date": {"type": "string"}, "end_date": {"type": "string"}},
			"required": ["video_id"]}`),
		Annotations: readOnly,
	},
	{
		Name: "video_retention",
		Description: "Audience retention curve of one video (since publish) with drop-off analysis: retention at 30s, 25/50/75% and the end, the intro loss, the steepest drop-off spans with timestamps, " +
			"rewatch spikes, the most common exit points, and where retention is above or below typical for videos of similar length. Filter by audience (organic vs ads) or subscribers vs non-subscribers to compare curves. " +
			"Pair the timestamps with the video's transcript to see what caused each drop.",
		InputSchema: schema(`{"type": "object", "properties": {
			` + channelParam + `,
			` + videoParam + `,
			"audience_type": {"type": "string", "enum": ["all", "organic", "ad_instream", "ad_indisplay"], "default": "all"},
			"subscribed_status": {"type": "string", "enum": ["all", "subscribed", "unsubscribed"], "default": "all"},
			"points": {"type": "integer", "enum": [10, 20, 50, 100], "default": 20, "description": "Curve points shown in the text (the full 100-point curve is always in structured content)"}},
			"required": ["video_id"]}`),
		Annotations: readOnly,
	},
	{
		Name: "traffic_sources",
		Description: "Where views come from (browse/home, suggested videos, search, Shorts feed, external, playlists, notifications…) with share, watch time and change vs the previous period, " +
			"plus impressions and CTR per source. For the channel or one video. Set detail_for to drill into a source: YT_SEARCH gives the search terms people used, RELATED_VIDEO the videos that suggest yours, " +
			"EXT_URL the external sites, PLAYLIST the playlists (top 25).",
		InputSchema: schema(`{"type": "object", "properties": {
			` + channelParam + `,
			` + videoParam + `,
			` + periodParams + `,
			"compare": {"type": "boolean", "default": true},
			"detail_for": {"type": "string", "enum": ["YT_SEARCH", "RELATED_VIDEO", "EXT_URL", "YT_CHANNEL", "SUBSCRIBER", "PLAYLIST", "YT_OTHER_PAGE", "SHORTS", "ADVERTISING", "HASHTAGS", "SOUND_PAGE", "YT_PLAYLIST_PAGE", "NO_LINK_OTHER"], "description": "Break one source down instead"},
			"limit": {"type": "integer", "minimum": 1, "maximum": 25, "default": 25}}}`),
		Annotations: readOnly,
	},
	{
		Name: "audience",
		Description: "Who watches and how: age and gender, top countries (with watch time and subscribers gained), devices, subscribers vs non-subscribers (views, average view duration and % viewed), " +
			"formats (Shorts vs videos vs live), playback locations, operating systems and sharing services. For the channel or one video.",
		InputSchema: schema(`{"type": "object", "properties": {
			` + channelParam + `,
			` + videoParam + `,
			` + periodParams + `,
			"breakdowns": {"type": "array", "items": {"type": "string", "enum": ["all", "demographics", "geography", "devices", "subscription", "content_type", "playback_location", "operating_system", "sharing"]}, "default": ["all"], "description": "all = demographics, geography, devices, subscription, content_type"}}}`),
		Annotations: readOnly,
	},
	{
		Name: "timeline",
		Description: "Daily or monthly series for the channel or one video, with totals, peaks, lows and a trend (second half vs first half). Daily series include impressions and CTR when available. " +
			"Use it to spot spikes, decay after upload, or the effect of a change (new thumbnail, title, upload schedule).",
		InputSchema: schema(`{"type": "object", "properties": {
			` + channelParam + `,
			` + videoParam + `,
			` + periodParams + `,
			"granularity": {"type": "string", "enum": ["day", "month"], "description": "Default: day, or month for periods over 400 days"},
			"metrics": {"type": "array", "items": {"type": "string"}, "description": "Analytics API metric names; default views, estimatedMinutesWatched, subscribersGained, subscribersLost. Others: engagedViews, averageViewDuration, averageViewPercentage, likes, comments, shares, estimatedRevenue"}}}`),
		Annotations: readOnly,
	},
	{
		Name: "impressions_ctr",
		Description: "Thumbnail impressions and impressions click-through rate, grouped by video, day, traffic source or device, from YouTube's daily reach reports (cached locally so history grows beyond YouTube's 60-day retention). " +
			"Also shows how far back the data goes. Use it to judge thumbnails and titles: low CTR with high impressions means packaging is the bottleneck.",
		InputSchema: schema(`{"type": "object", "properties": {
			` + channelParam + `,
			` + videoParam + `,
			` + periodParams + `,
			"group_by": {"type": "string", "enum": ["video", "day", "traffic_source", "device"], "description": "Default: video, or day when video_id is set"},
			"limit": {"type": "integer", "minimum": 1, "maximum": 200, "default": 50},
			"refresh": {"type": "boolean", "default": false, "description": "Download new reach report files now instead of using the hourly sync"}}}`),
		Annotations: readOnly,
	},
	{
		Name:        "video_comments",
		Description: "Recent or top comments on one video or across the whole channel, with likes and reply counts. Set unanswered_only to find comments the channel has not replied to. Use it for audience questions, content ideas and sentiment.",
		InputSchema: schema(`{"type": "object", "properties": {
			` + channelParam + `,
			` + videoParam + `,
			"order": {"type": "string", "enum": ["time", "relevance"], "default": "time"},
			"limit": {"type": "integer", "minimum": 1, "maximum": 100, "default": 30},
			"unanswered_only": {"type": "boolean", "default": false}}}`),
		Annotations: readOnly,
	},
	{
		Name: "analytics_query",
		Description: "Run a raw YouTube Analytics API v2 query for anything the other tools do not cover (e.g. cards, end screens, playlists, live concurrent viewers, cities). " +
			"See https://developers.google.com/youtube/analytics/channel_reports for valid metric/dimension/filter combinations; unsupported combinations return a 400 error.",
		InputSchema: schema(`{"type": "object", "properties": {
			` + channelParam + `,
			"start_date": {"type": "string", "description": "YYYY-MM-DD"},
			"end_date": {"type": "string", "description": "YYYY-MM-DD"},
			"metrics": {"type": "array", "items": {"type": "string"}, "minItems": 1, "description": "e.g. [\"views\",\"estimatedMinutesWatched\"]"},
			"dimensions": {"type": "array", "items": {"type": "string"}, "description": "e.g. [\"day\"] or [\"video\"]"},
			"filters": {"type": "array", "items": {"type": "string"}, "description": "e.g. [\"video==VIDEO_ID\", \"country==US\"]"},
			"sort": {"type": "string", "description": "e.g. -views"},
			"max_results": {"type": "integer", "minimum": 1, "maximum": 200},
			"currency": {"type": "string"}},
			"required": ["start_date", "end_date", "metrics"]}`),
		Annotations: readOnly,
	},
}
