#!/bin/bash
# Build, test and drive the youtube_analytics MCP server from the terminal.
cd "$(dirname "$0")"

# Load the OAuth client from .env if present
if [ -f .env ]; then
    set -a; . ./.env; set +a
fi

usage() {
    cat <<USAGE
Usage: ./run.sh <command> [args]

  build                        Build bin/youtube_analytics
  test                         Run unit and fake-API tests
  run                          Run the MCP server on stdio
  tools                        List tools
  auth                         Connect a channel (opens the Google consent page)
  call <tool> '<json args>'    Call any tool, e.g. ./run.sh call audience '{"period":"90d"}'

Shortcuts (add a channel with CHANNEL=@handle when several are connected):
  channels                     list_channels
  overview [28d]               channel_overview
  videos [50] [title filter]   list_videos
  perf [views] [28d]           video_performance (sort: views watch_time subscribers ctr impressions avg_view_percentage engagement)
  report <video>               video_report
  retention <video>            video_retention
  traffic [video] [SOURCE]     traffic_sources (SOURCE e.g. YT_SEARCH, RELATED_VIDEO, EXT_URL)
  audience [video]             audience
  timeline [28d] [day|month]   timeline
  ctr [video|day|traffic_source|device]
                               impressions_ctr
  comments [video]             video_comments (unanswered only)
USAGE
    exit 1
}

# json '{"k":"v"}' adds "channel" when CHANNEL is set.
json() {
    python3 -c 'import sys,json,os; a=json.loads(sys.argv[1]); c=os.environ.get("CHANNEL");
if c: a["channel"]=c
print(json.dumps(a))' "$1"
}
call() { go run ./cmd -tool "$1" -args "$(json "$2")"; }
vid() { [ -n "$1" ] && printf '"video_id":"%s"' "$1"; }

case "$1" in
    build)     mkdir -p bin && go build -o bin/youtube_analytics ./cmd && echo "built bin/youtube_analytics" ;;
    test)      go test ./... ;;
    run)       go run ./cmd ;;
    tools)     go run ./cmd -list ;;
    auth)      go run ./cmd -auth ;;
    call)      [ -z "$2" ] && usage; call "$2" "${3:-{\}}" ;;
    channels)  call list_channels '{}' ;;
    overview)  call channel_overview "{\"period\":\"${2:-28d}\"}" ;;
    videos)    call list_videos "{\"limit\":${2:-50},\"query\":\"$3\"}" ;;
    perf)      call video_performance "{\"sort\":\"${2:-views}\",\"period\":\"${3:-28d}\"}" ;;
    report)    [ -z "$2" ] && usage; call video_report "{$(vid "$2")}" ;;
    retention) [ -z "$2" ] && usage; call video_retention "{$(vid "$2")}" ;;
    traffic)   a="$(vid "$2")"; [ -n "$3" ] && a="${a:+$a,}\"detail_for\":\"$3\""; call traffic_sources "{$a}" ;;
    audience)  call audience "{$(vid "$2")}" ;;
    timeline)  call timeline "{\"period\":\"${2:-28d}\",\"granularity\":\"${3:-day}\"}" ;;
    ctr)       call impressions_ctr "{\"group_by\":\"${2:-video}\"}" ;;
    comments)  call video_comments "{$(vid "$2")${2:+,}\"unanswered_only\":true}" ;;
    *)         usage ;;
esac
