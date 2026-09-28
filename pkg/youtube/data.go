package youtube

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Channel is the authorized channel's public profile and lifetime totals.
type Channel struct {
	ID                string    `json:"id"`
	Title             string    `json:"title"`
	Handle            string    `json:"handle,omitempty"`
	Description       string    `json:"description,omitempty"`
	Country           string    `json:"country,omitempty"`
	PublishedAt       time.Time `json:"published_at"`
	Subscribers       int64     `json:"subscribers"`
	HiddenSubscribers bool      `json:"hidden_subscribers,omitempty"`
	Views             int64     `json:"views"`
	Videos            int64     `json:"videos"`
	UploadsPlaylist   string    `json:"uploads_playlist"`
}

// MyChannel returns the channel the token was granted for.
func (c *Client) MyChannel(ctx context.Context) (*Channel, error) {
	var resp struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title       string    `json:"title"`
				Description string    `json:"description"`
				CustomURL   string    `json:"customUrl"`
				PublishedAt time.Time `json:"publishedAt"`
				Country     string    `json:"country"`
			} `json:"snippet"`
			Statistics struct {
				ViewCount             string `json:"viewCount"`
				SubscriberCount       string `json:"subscriberCount"`
				HiddenSubscriberCount bool   `json:"hiddenSubscriberCount"`
				VideoCount            string `json:"videoCount"`
			} `json:"statistics"`
			ContentDetails struct {
				RelatedPlaylists struct {
					Uploads string `json:"uploads"`
				} `json:"relatedPlaylists"`
			} `json:"contentDetails"`
		} `json:"items"`
	}
	u := withQuery(c.DataURL+"/channels", url.Values{"part": {"snippet,statistics,contentDetails"}, "mine": {"true"}})
	if err := c.getJSON(ctx, u, &resp); err != nil {
		return nil, err
	}
	if len(resp.Items) == 0 {
		return nil, errors.New("this Google account has no YouTube channel")
	}
	it := resp.Items[0]
	return &Channel{
		ID:                it.ID,
		Title:             it.Snippet.Title,
		Handle:            it.Snippet.CustomURL,
		Description:       it.Snippet.Description,
		Country:           it.Snippet.Country,
		PublishedAt:       it.Snippet.PublishedAt,
		Subscribers:       atoi(it.Statistics.SubscriberCount),
		HiddenSubscribers: it.Statistics.HiddenSubscriberCount,
		Views:             atoi(it.Statistics.ViewCount),
		Videos:            atoi(it.Statistics.VideoCount),
		UploadsPlaylist:   it.ContentDetails.RelatedPlaylists.Uploads,
	}, nil
}

// Uploads returns up to limit video IDs from the uploads playlist, newest first.
func (c *Client) Uploads(ctx context.Context, playlistID string, limit int) ([]string, error) {
	var ids []string
	page := ""
	for len(ids) < limit {
		v := url.Values{"part": {"contentDetails"}, "playlistId": {playlistID}, "maxResults": {"50"}}
		if page != "" {
			v.Set("pageToken", page)
		}
		var resp struct {
			NextPageToken string `json:"nextPageToken"`
			Items         []struct {
				ContentDetails struct {
					VideoID string `json:"videoId"`
				} `json:"contentDetails"`
			} `json:"items"`
		}
		if err := c.getJSON(ctx, withQuery(c.DataURL+"/playlistItems", v), &resp); err != nil {
			return nil, err
		}
		for _, it := range resp.Items {
			if len(ids) < limit {
				ids = append(ids, it.ContentDetails.VideoID)
			}
		}
		if resp.NextPageToken == "" {
			break
		}
		page = resp.NextPageToken
	}
	return ids, nil
}

// Video is a video's metadata and lifetime public counters.
type Video struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Description string        `json:"description,omitempty"`
	PublishedAt time.Time     `json:"published_at"`
	Duration    time.Duration `json:"-"`
	DurationSec int           `json:"duration_seconds"`
	Tags        []string      `json:"tags,omitempty"`
	CategoryID  string        `json:"category_id,omitempty"`
	Privacy     string        `json:"privacy"`
	WasLive     bool          `json:"was_live,omitempty"`
	Captions    bool          `json:"captions,omitempty"`
	MadeForKids bool          `json:"made_for_kids,omitempty"`
	Language    string        `json:"language,omitempty"`
	Thumbnail   string        `json:"thumbnail,omitempty"`
	Views       int64         `json:"views"`
	Likes       int64         `json:"likes"`
	Comments    int64         `json:"comments"`
}

const videoTTL = 30 * time.Minute

// Videos fetches metadata for the given IDs (50 per request), with a
// 30-minute in-memory cache. Unknown or private-to-others IDs are omitted.
func (c *Client) Videos(ctx context.Context, ids []string) (map[string]*Video, error) {
	out := map[string]*Video{}
	var missing []string
	c.mu.Lock()
	for _, id := range ids {
		if cv, ok := c.videos[id]; ok && time.Since(cv.at) < videoTTL {
			out[id] = cv.v
		} else if id != "" {
			missing = append(missing, id)
		}
	}
	c.mu.Unlock()
	missing = dedupe(missing)

	for len(missing) > 0 {
		n := min(50, len(missing))
		batch := missing[:n]
		missing = missing[n:]
		var resp struct {
			Items []struct {
				ID      string `json:"id"`
				Snippet struct {
					Title                string    `json:"title"`
					Description          string    `json:"description"`
					PublishedAt          time.Time `json:"publishedAt"`
					Tags                 []string  `json:"tags"`
					CategoryID           string    `json:"categoryId"`
					DefaultAudioLanguage string    `json:"defaultAudioLanguage"`
					Thumbnails           map[string]struct {
						URL string `json:"url"`
					} `json:"thumbnails"`
				} `json:"snippet"`
				ContentDetails struct {
					Duration string `json:"duration"`
					Caption  string `json:"caption"`
				} `json:"contentDetails"`
				Statistics struct {
					ViewCount    string `json:"viewCount"`
					LikeCount    string `json:"likeCount"`
					CommentCount string `json:"commentCount"`
				} `json:"statistics"`
				Status struct {
					PrivacyStatus string `json:"privacyStatus"`
					MadeForKids   bool   `json:"madeForKids"`
				} `json:"status"`
				LiveStreamingDetails *struct{} `json:"liveStreamingDetails"`
			} `json:"items"`
		}
		v := url.Values{"part": {"snippet,contentDetails,statistics,status,liveStreamingDetails"}, "id": {strings.Join(batch, ",")}, "maxResults": {"50"}}
		if err := c.getJSON(ctx, withQuery(c.DataURL+"/videos", v), &resp); err != nil {
			return nil, err
		}
		now := time.Now()
		c.mu.Lock()
		for _, it := range resp.Items {
			d := ParseISODuration(it.ContentDetails.Duration)
			vid := &Video{
				ID:          it.ID,
				Title:       it.Snippet.Title,
				Description: it.Snippet.Description,
				PublishedAt: it.Snippet.PublishedAt,
				Duration:    d,
				DurationSec: int(d / time.Second),
				Tags:        it.Snippet.Tags,
				CategoryID:  it.Snippet.CategoryID,
				Privacy:     it.Status.PrivacyStatus,
				WasLive:     it.LiveStreamingDetails != nil,
				Captions:    it.ContentDetails.Caption == "true",
				MadeForKids: it.Status.MadeForKids,
				Language:    it.Snippet.DefaultAudioLanguage,
				Thumbnail:   bestThumb(it.Snippet.Thumbnails),
				Views:       atoi(it.Statistics.ViewCount),
				Likes:       atoi(it.Statistics.LikeCount),
				Comments:    atoi(it.Statistics.CommentCount),
			}
			out[it.ID] = vid
			c.videos[it.ID] = cachedVideo{v: vid, at: now}
		}
		c.mu.Unlock()
	}
	return out, nil
}

// Video fetches one video or fails with a clear message.
func (c *Client) Video(ctx context.Context, id string) (*Video, error) {
	m, err := c.Videos(ctx, []string{id})
	if err != nil {
		return nil, err
	}
	v, ok := m[id]
	if !ok {
		return nil, fmt.Errorf("video %s not found (check the ID; it must be a video on this channel or public)", id)
	}
	return v, nil
}

// Comment is a top-level comment thread.
type Comment struct {
	ID           string    `json:"id"`
	VideoID      string    `json:"video_id"`
	Author       string    `json:"author"`
	Text         string    `json:"text"`
	Likes        int64     `json:"likes"`
	Replies      int64     `json:"replies"`
	OwnerReplied bool      `json:"owner_replied"`
	ByOwner      bool      `json:"by_owner,omitempty"`
	PublishedAt  time.Time `json:"published_at"`
}

// Comments lists top-level comments on a video, or across the whole channel
// when videoID is empty. order is "time" or "relevance".
func (c *Client) Comments(ctx context.Context, channelID, videoID, order string, limit int) ([]Comment, error) {
	var out []Comment
	page := ""
	for len(out) < limit {
		v := url.Values{"part": {"snippet,replies"}, "maxResults": {"100"}, "order": {order}, "textFormat": {"plainText"}}
		if videoID != "" {
			v.Set("videoId", videoID)
		} else {
			v.Set("allThreadsRelatedToChannelId", channelID)
		}
		if page != "" {
			v.Set("pageToken", page)
		}
		var resp struct {
			NextPageToken string `json:"nextPageToken"`
			Items         []struct {
				ID      string `json:"id"`
				Snippet struct {
					VideoID         string `json:"videoId"`
					TotalReplyCount int64  `json:"totalReplyCount"`
					TopLevelComment struct {
						Snippet commentSnippet `json:"snippet"`
					} `json:"topLevelComment"`
				} `json:"snippet"`
				Replies struct {
					Comments []struct {
						Snippet commentSnippet `json:"snippet"`
					} `json:"comments"`
				} `json:"replies"`
			} `json:"items"`
		}
		if err := c.getJSON(ctx, withQuery(c.DataURL+"/commentThreads", v), &resp); err != nil {
			return nil, err
		}
		for _, it := range resp.Items {
			s := it.Snippet.TopLevelComment.Snippet
			cm := Comment{
				ID:          it.ID,
				VideoID:     it.Snippet.VideoID,
				Author:      s.AuthorDisplayName,
				Text:        s.TextDisplay,
				Likes:       s.LikeCount,
				Replies:     it.Snippet.TotalReplyCount,
				PublishedAt: s.PublishedAt,
				ByOwner:     s.AuthorChannelID.Value == channelID,
			}
			for _, r := range it.Replies.Comments {
				if r.Snippet.AuthorChannelID.Value == channelID {
					cm.OwnerReplied = true
				}
			}
			if len(out) < limit {
				out = append(out, cm)
			}
		}
		if resp.NextPageToken == "" {
			break
		}
		page = resp.NextPageToken
	}
	return out, nil
}

type commentSnippet struct {
	AuthorDisplayName string `json:"authorDisplayName"`
	AuthorChannelID   struct {
		Value string `json:"value"`
	} `json:"authorChannelId"`
	TextDisplay string    `json:"textDisplay"`
	LikeCount   int64     `json:"likeCount"`
	PublishedAt time.Time `json:"publishedAt"`
}

var isoDur = regexp.MustCompile(`^P(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)S)?)?$`)

// ParseISODuration parses YouTube's ISO 8601 durations such as PT1H2M3S.
func ParseISODuration(s string) time.Duration {
	m := isoDur.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	n := func(i int) time.Duration {
		v, _ := strconv.Atoi(m[i])
		return time.Duration(v)
	}
	return n(1)*24*time.Hour + n(2)*time.Hour + n(3)*time.Minute + n(4)*time.Second
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
var idInURL = regexp.MustCompile(`(?:v=|youtu\.be/|/shorts/|/live/|/embed/|/v/|/video/)([A-Za-z0-9_-]{11})`)

// ParseVideoID accepts a bare 11-character ID or any common YouTube URL form.
func ParseVideoID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if idPattern.MatchString(s) {
		return s, nil
	}
	if m := idInURL.FindStringSubmatch(s); m != nil {
		return m[1], nil
	}
	return "", fmt.Errorf("%q is not a YouTube video ID or URL", s)
}

func bestThumb(t map[string]struct {
	URL string `json:"url"`
}) string {
	for _, k := range []string{"maxres", "standard", "high", "medium", "default"} {
		if v, ok := t[k]; ok {
			return v.URL
		}
	}
	return ""
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
