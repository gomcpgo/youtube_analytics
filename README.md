# YouTube Analytics MCP Server

An MCP server for analyzing YouTube channels **you own**, using Google's official APIs: audience
retention and drop-off points, thumbnail impressions and click-through rate, traffic sources and
search terms, audience demographics, per-video performance, trends and comments. Connect several
channels and ask your assistant things like:

- "How did my channel do in the last 28 days compared with the month before?"
- "Which videos have the lowest CTR despite high impressions?"
- "Where do viewers drop off in my last video, and what happens at those timestamps?"
- "Which search terms bring viewers to this video?"
- "Do Shorts or long videos bring me more subscribers per view?"

Written in Go on the [gomcpgo/mcp](https://github.com/gomcpgo/mcp) framework. MIT licensed.

## Tools

| Tool | What it answers |
|---|---|
| `connect_channel` | Sign in with Google in the browser to add a channel. Run once per channel. |
| `list_channels` | Connected channels, live subscriber/view counts, authorization health, impressions data status. |
| `channel_overview` | Period vs previous period: views, engaged views, watch time, avg view duration, avg % viewed, impressions, CTR, subscribers (gained/lost/net/per 1K views), likes, dislikes, comments, shares, engagement rate, Shorts vs videos vs live, top 5 videos, optional revenue. |
| `list_videos` | Uploads with IDs, dates, lengths, privacy and lifetime public counts; title search. |
| `video_performance` | Per-video table ranked by views, watch time, subscribers, CTR, impressions, avg % viewed, engagement…; filter by format or compare specific videos. |
| `video_report` | One-call diagnosis of a video: key metrics, launch curve, traffic sources, search terms, subscribers vs not, retention summary, impressions/CTR by source, and the channel's 90-day benchmark for the same format. |
| `video_retention` | The retention curve with the hook (0:30), 25/50/75%/end checkpoints, steepest drop-offs with timestamps, rewatch spikes, top exit points, and spans above/below typical for similar-length videos. Filter organic vs ads or subscribers vs not. |
| `traffic_sources` | Views by source with change vs the previous period, impressions and CTR per source, and drill-downs: search terms (`YT_SEARCH`), suggesting videos (`RELATED_VIDEO`), external sites (`EXT_URL`), playlists… |
| `audience` | Age and gender, countries, devices, OS, subscribers vs non-subscribers, formats, playback locations, sharing services. |
| `timeline` | Daily or monthly series with totals, peaks, lows and trend; daily series include impressions and CTR. |
| `impressions_ctr` | Thumbnail impressions and CTR by video, day, traffic source or device. |
| `video_comments` | Recent or top comments on a video or the whole channel; `unanswered_only` finds comments you have not replied to. Needs `YOUTUBE_API_KEY` (see Setup). |
| `analytics_query` | Raw YouTube Analytics API query for anything else (cards, end screens, playlists, live concurrents…). |

All period tools take `period` (`7d`, `28d`, `90d`, `365d`, `lifetime`) or `start_date`/`end_date`.
Rolling windows end 3 days ago because YouTube Analytics data arrives 48–72 hours late; ending on
complete days keeps comparisons honest.

## Where the data comes from, and what the API cannot give you

| Data | Source |
|---|---|
| Channel and video metadata, public counts | YouTube Data API v3 (OAuth) |
| Comments | YouTube Data API v3 (API key; public comments only) |
| Views, watch time, retention, traffic, audience, revenue | YouTube Analytics API v2 (targeted queries) |
| **Impressions and impressions CTR** | YouTube Reporting API v1 (daily bulk "reach" reports) |

Impressions and CTR are **not** available from the Analytics API. They come only from the Reporting
API's reach reports (`channel_reach_basic_a1`, `channel_reach_combined_a1`, available since January
2026). The server schedules them when you connect a channel. YouTube then:

- delivers the first files about **48 hours later**, including the **30 days before** scheduling;
- adds one file per day afterwards;
- deletes files after 30–60 days.

The server downloads new files at most hourly into a local cache, so your impressions history keeps
growing from the day you connect. Earlier impressions history is only visible in YouTube Studio.

Not available through any YouTube API as of September 2026:

- data from the last 2–3 days (the realtime panel in Studio);
- Shorts "viewed vs swiped away";
- results of thumbnail/title A/B tests ("Test & compare");
- hourly data and "when your viewers are on YouTube";
- retention for videos with very few views (YouTube returns no curve), and exit counts
  (`startedWatching`/`stoppedWatching`) for small videos. Curves of videos under 1,000 views are
  noisy, so the server smooths them before looking for drop-offs and says so.

Observed API quirks the server works around: `creatorContentType` values come back camelCase
(`videoOnDemand`, `shorts`) and filters only accept that form; `video` and `creatorContentType`
cannot be combined as dimensions; asking for exit counts together with the retention curve empties the
curve; a brand-new reporting job answers 503 until its first report exists.

## Setup (about 5 minutes)

The server uses your own Google Cloud OAuth client, so your data never passes through anyone else's app.

1. Open [Google Cloud Console](https://console.cloud.google.com/) and create a project (any name).
2. **APIs & Services → Library**: enable **YouTube Data API v3**, **YouTube Analytics API** and
   **YouTube Reporting API**.
3. **Google Auth Platform** (formerly "OAuth consent screen") → Get started: app name, your email,
   audience **External**.
4. Under **Audience**, click **Publish app** to set the status to **In production**. In "Testing"
   status Google expires refresh tokens after 7 days and you would have to reconnect every week. You do
   not need Google's verification for personal use; you will see a "Google hasn't verified this app"
   warning at sign-in, which is expected (click *Advanced → Go to …*).
5. **Clients → Create client → Desktop app**. Copy the client ID and client secret.
6. Put them in the environment (or `.env` for `run.sh`):

   ```bash
   YOUTUBE_OAUTH_CLIENT_ID=1234567890-abc.apps.googleusercontent.com
   YOUTUBE_OAUTH_CLIENT_SECRET=GOCSPX-...
   ```

7. Optional, for `video_comments`: **APIs & Services → Credentials → Create credentials → API key**,
   then **Edit API key → API restrictions → Restrict key → YouTube Data API v3**. Set it as
   `YOUTUBE_API_KEY`. Reading comments over OAuth would need the read-write `youtube.force-ssl` scope,
   so the server uses a key instead and sees public comments only.
8. Build and connect a channel:

   ```bash
   ./run.sh build
   ./run.sh auth          # opens the Google consent page; pick the channel
   ./run.sh overview
   ```

   Or ask your assistant to call `connect_channel`; it opens the consent page, waits up to 90
   seconds, and calling it again reopens the same sign-in. For a Google account that owns several
   channels (brand accounts), connect once per channel and pick a different channel on the consent
   screen each time.

### MCP client configuration

Claude Code:

```bash
claude mcp add youtube_analytics \
  -e YOUTUBE_OAUTH_CLIENT_ID=... -e YOUTUBE_OAUTH_CLIENT_SECRET=... \
  -- /path/to/youtube_analytics/bin/youtube_analytics
```

Claude Desktop and other clients:

```json
{
  "mcpServers": {
    "youtube_analytics": {
      "command": "/path/to/youtube_analytics/bin/youtube_analytics",
      "env": {
        "YOUTUBE_OAUTH_CLIENT_ID": "...",
        "YOUTUBE_OAUTH_CLIENT_SECRET": "...",
        "YOUTUBE_API_KEY": "..."
      }
    }
  }
}
```

### Environment variables

| Variable | Default | |
|---|---|---|
| `YOUTUBE_OAUTH_CLIENT_ID` | (required) | OAuth client ID (Desktop app) |
| `YOUTUBE_OAUTH_CLIENT_SECRET` | (required) | OAuth client secret |
| `YOUTUBE_API_KEY` | (optional) | Data API key, only for `video_comments` |
| `YOUTUBE_ANALYTICS_DATA_DIR` | OS config dir + `gomcpgo/youtube_analytics` | Tokens and the impressions cache |
| `YOUTUBE_ANALYTICS_TIMEOUT` | `60s` | HTTP timeout |
| `YOUTUBE_ANALYTICS_DEBUG` | off | `1` logs each API call to stderr |

## Data, privacy and quotas

- Scopes requested: `youtube.readonly`, `yt-analytics.readonly`, `yt-analytics-monetary.readonly`
  (the last one only returns revenue for YouTube Partner Program channels). The server never
  modifies your videos or channel. The only write it makes is scheduling the two reach reporting jobs.
- Tools return markdown text only (no `structuredContent`): some clients show structured content to
  the model instead of the text, which would hide the coverage notes and caveats.
- Tokens live in `<data dir>/tokens.json` (mode 0600); reach report CSVs in
  `<data dir>/channels/<channel id>/reach/`. Nothing is sent anywhere except Google's APIs.
- Revoke access any time at <https://myaccount.google.com/permissions>.
- Quota: the Data API allows 10,000 units a day per project; each tool call uses 1–5 units. The
  Analytics and Reporting APIs have separate, generous quotas.

## Development

```bash
./run.sh test                       # unit tests + end-to-end tests against an in-process fake Google API
./run.sh tools                      # list tools
./run.sh call video_retention '{"video_id":"https://youtu.be/VIDEO_ID"}'
./run.sh perf ctr 90d               # shortcuts: channels overview videos perf report retention traffic audience timeline ctr comments
CHANNEL=@otherchannel ./run.sh overview
```

Layout: `cmd/` (entry point and terminal mode), `pkg/handler` (MCP layer), `pkg/analytics` (analysis
logic), `pkg/youtube` (API client), `pkg/reach` (Reporting API cache), `pkg/auth` (OAuth and token
store), `internal/fakegoogle` (test fake).
