package analytics

import (
	"context"
	"fmt"
	"sort"

	"github.com/gomcpgo/youtube_analytics/pkg/reach"
	"github.com/gomcpgo/youtube_analytics/pkg/youtube"
)

// ReachRow is impressions and CTR for one group.
type ReachRow struct {
	Key         string  `json:"key"`
	Label       string  `json:"label"`
	Impressions float64 `json:"impressions"`
	Clicks      float64 `json:"clicks"`
	CTR         float64 `json:"ctr_pct"`
	Share       float64 `json:"impression_share_pct"`
}

// ReachReport is thumbnail impressions and CTR grouped one way.
type ReachReport struct {
	Period      Period       `json:"period"`
	GroupBy     string       `json:"group_by"`
	VideoID     string       `json:"video_id,omitempty"`
	Impressions float64      `json:"impressions"`
	CTR         float64      `json:"ctr_pct"`
	Rows        []ReachRow   `json:"rows"`
	Coverage    Coverage     `json:"coverage"`
	Status      reach.Status `json:"cache"`
	Downloaded  int          `json:"downloaded_now"`
}

// ReachGroups are the accepted group_by values.
var ReachGroups = []string{"video", "day", "traffic_source", "device"}

// Impressions reports thumbnail impressions and click-through rate from the
// Reporting API reach reports. refresh forces a sync now.
func (s *Service) Impressions(ctx context.Context, spec PeriodSpec, groupBy, videoID string, limit int, refresh bool) (*ReachReport, error) {
	if s.reach == nil {
		return nil, fmt.Errorf("the local reach cache could not be opened")
	}
	if groupBy == "" {
		groupBy = "video"
		if videoID != "" {
			groupBy = "day"
		}
	}
	if !contains(ReachGroups, groupBy) {
		return nil, fmt.Errorf("group_by must be one of video, day, traffic_source, device")
	}
	cur, _, err := s.periodFor(ctx, spec, videoID)
	if err != nil {
		return nil, err
	}
	out := &ReachReport{Period: cur, GroupBy: groupBy, VideoID: videoID}
	if refresh {
		res, err := s.reach.Sync(ctx, s.yt, 0)
		if err != nil {
			return nil, err
		}
		out.Downloaded = res.Downloaded
	}
	reportType := reach.Basic
	if groupBy == "traffic_source" || groupBy == "device" {
		reportType = reach.Combined
	}
	rows, cov := s.reachRows(ctx, reportType, cur)
	out.Coverage, out.Status = cov, s.reach.Status()
	if videoID != "" {
		kept := rows[:0]
		for _, r := range rows {
			if r.VideoID == videoID {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	tot := reach.Total(rows, "")
	out.Impressions, out.CTR = tot.Impressions, tot.CTR()

	key := map[string]func(reach.Row) string{
		"video":          func(r reach.Row) string { return r.VideoID },
		"day":            func(r reach.Row) string { return r.Date },
		"traffic_source": func(r reach.Row) string { return reach.TrafficSourceName(r.TrafficSource) },
		"device":         func(r reach.Row) string { return reach.DeviceName(r.DeviceType) },
	}[groupBy]
	for k, a := range reach.GroupBy(rows, key) {
		out.Rows = append(out.Rows, ReachRow{Key: k, Label: k, Impressions: a.Impressions, Clicks: a.Clicks, CTR: a.CTR(), Share: pct(a.Impressions, tot.Impressions)})
	}
	if groupBy == "day" {
		sort.Slice(out.Rows, func(i, j int) bool { return out.Rows[i].Key < out.Rows[j].Key })
	} else {
		sort.Slice(out.Rows, func(i, j int) bool { return out.Rows[i].Impressions > out.Rows[j].Impressions })
	}
	if groupBy == "video" {
		if limit > 0 && len(out.Rows) > limit {
			out.Rows = out.Rows[:limit]
		}
		ids := make([]string, len(out.Rows))
		for i, r := range out.Rows {
			ids[i] = r.Key
		}
		if meta, err := s.yt.Videos(ctx, ids); err == nil {
			for i := range out.Rows {
				if m, ok := meta[out.Rows[i].Key]; ok {
					out.Rows[i].Label = m.Title
				}
			}
		}
	}
	return out, nil
}

// CommentList is a page of comments with video titles.
type CommentList struct {
	VideoID  string            `json:"video_id,omitempty"`
	Comments []youtube.Comment `json:"comments"`
	Titles   map[string]string `json:"titles,omitempty"`
}

// Comments lists recent or top comments on a video or the whole channel.
// unanswered keeps only threads the channel has not replied to.
func (s *Service) Comments(ctx context.Context, apiKey, channelID, videoID, order string, limit int, unanswered bool) (*CommentList, error) {
	fetch := limit
	if unanswered {
		fetch = min(limit*3, 300)
	}
	cs, err := s.yt.Comments(ctx, apiKey, channelID, videoID, order, fetch)
	if err != nil {
		return nil, err
	}
	out := &CommentList{VideoID: videoID}
	for _, c := range cs {
		if unanswered && (c.OwnerReplied || c.ByOwner) {
			continue
		}
		if len(out.Comments) < limit {
			out.Comments = append(out.Comments, c)
		}
	}
	if videoID == "" {
		var ids []string
		for _, c := range out.Comments {
			ids = append(ids, c.VideoID)
		}
		if meta, err := s.yt.Videos(ctx, ids); err == nil {
			out.Titles = map[string]string{}
			for id, m := range meta {
				out.Titles[id] = m.Title
			}
		}
	}
	return out, nil
}
