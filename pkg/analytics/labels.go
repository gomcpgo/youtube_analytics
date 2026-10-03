package analytics

import (
	"strings"
	"unicode"
)

// Kind says how a metric value should be displayed.
type Kind string

const (
	KindCount    Kind = "count"
	KindMinutes  Kind = "minutes" // shown as hours
	KindSeconds  Kind = "seconds" // shown as m:ss
	KindPercent  Kind = "percent"
	KindCurrency Kind = "currency"
	KindPer1K    Kind = "per_1k_views"
)

// Label maps enum values from the Analytics API to readable names.
func Label(dimension, value string) string {
	key := Canonical(value)
	var m map[string]string
	switch dimension {
	case "creatorContentType":
		m = contentTypes
	case "insightTrafficSourceType":
		m = trafficSources
	case "deviceType":
		m = devices
	case "insightPlaybackLocationType":
		m = playbackLocations
	case "subscribedStatus":
		m = subscribed
	case "gender":
		m, key = genders, strings.ToLower(value)
	}
	if v, ok := m[key]; ok {
		return v
	}
	if strings.HasPrefix(value, "age") {
		return strings.ReplaceAll(strings.TrimPrefix(value, "age"), "-", "–")
	}
	return value
}

// Canonical returns the documented UPPER_SNAKE_CASE form of an enum value.
// The API returns some values in camelCase (creatorContentType: videoOnDemand).
func Canonical(v string) string {
	var b strings.Builder
	prevLower := false
	for _, r := range v {
		if unicode.IsUpper(r) && prevLower {
			b.WriteByte('_')
		}
		prevLower = unicode.IsLower(r) || unicode.IsDigit(r)
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

var contentTypes = map[string]string{
	"SHORTS": "Shorts", "VIDEO_ON_DEMAND": "Videos", "LIVE_STREAM": "Live", "STORY": "Stories", "UNSPECIFIED": "Unspecified",
}

// contentTypeValues are the creatorContentType filter values the API
// accepts. Filters must use this camelCase form; SHORTS is rejected.
var contentTypeValues = []string{"videoOnDemand", "shorts", "liveStream"}

// ContentTypeFilter maps a user-facing format name to a creatorContentType
// filter value.
func ContentTypeFilter(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "all":
		return "", true
	case "shorts", "short":
		return "shorts", true
	case "videos", "video", "long", "longform", "long-form":
		return "videoOnDemand", true
	case "live", "livestream", "live_stream":
		return "liveStream", true
	}
	return "", false
}

var trafficSources = map[string]string{
	"ADVERTISING": "YouTube advertising", "ANNOTATION": "Video cards and annotations", "CAMPAIGN_CARD": "Campaign cards",
	"END_SCREEN": "End screens", "EXT_URL": "External", "HASHTAGS": "Hashtag pages", "LIVE_REDIRECT": "Live redirects",
	"NO_LINK_EMBEDDED": "Embedded (no link)", "NO_LINK_OTHER": "Direct or unknown", "NOTIFICATION": "Notifications",
	"PLAYLIST": "Playlists", "PRODUCT_PAGE": "Product pages", "PROMOTED": "Promoted", "RELATED_VIDEO": "Suggested videos",
	"SHORTS": "Shorts feed", "SOUND_PAGE": "Sound pages", "SUBSCRIBER": "Browse features (home, subscriptions)",
	"YT_CHANNEL": "Channel pages", "YT_OTHER_PAGE": "Other YouTube features", "YT_PLAYLIST_PAGE": "Playlist pages",
	"YT_SEARCH": "YouTube search", "VIDEO_REMIXES": "Remixed videos", "IMMERSIVE_LIVE": "Vertical live feed",
	"SHORTS_CONTENT_LINKS": "Shorts related-video links",
}

var devices = map[string]string{
	"DESKTOP": "Computer", "MOBILE": "Mobile phone", "TABLET": "Tablet", "TV": "TV", "GAME_CONSOLE": "Game console", "UNKNOWN_PLATFORM": "Unknown",
}

var playbackLocations = map[string]string{
	"BROWSE": "Home/browse pages", "CHANNEL": "Channel page", "EMBEDDED": "Embedded on other sites", "EXTERNAL_APP": "External apps",
	"MOBILE": "YouTube mobile site", "SEARCH": "Search results page", "WATCH": "Watch page", "YT_OTHER": "Other YouTube pages", "SHORTS": "Shorts feed",
}

var subscribed = map[string]string{"SUBSCRIBED": "Subscribers", "UNSUBSCRIBED": "Non-subscribers"}

var genders = map[string]string{"female": "Female", "male": "Male", "user_specified": "User-specified"}
