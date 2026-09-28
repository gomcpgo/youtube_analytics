package youtube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

// Job is a Reporting API job that makes YouTube generate one daily report type.
type Job struct {
	ID            string    `json:"id"`
	ReportTypeID  string    `json:"reportTypeId"`
	Name          string    `json:"name"`
	CreateTime    time.Time `json:"createTime"`
	SystemManaged bool      `json:"systemManaged"`
}

// Report is one generated daily report file.
type Report struct {
	ID          string    `json:"id"`
	JobID       string    `json:"jobId"`
	StartTime   time.Time `json:"startTime"`
	EndTime     time.Time `json:"endTime"`
	CreateTime  time.Time `json:"createTime"`
	DownloadURL string    `json:"downloadUrl"`
}

// Jobs lists the channel's reporting jobs.
func (c *Client) Jobs(ctx context.Context) ([]Job, error) {
	var out []Job
	page := ""
	for {
		v := url.Values{}
		if page != "" {
			v.Set("pageToken", page)
		}
		var resp struct {
			Jobs          []Job  `json:"jobs"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := c.getJSON(ctx, withQuery(c.ReportingURL+"/jobs", v), &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Jobs...)
		if resp.NextPageToken == "" {
			return out, nil
		}
		page = resp.NextPageToken
	}
}

// CreateJob schedules a daily report type for the channel.
func (c *Client) CreateJob(ctx context.Context, reportTypeID, name string) (*Job, error) {
	b, err := c.do(ctx, http.MethodPost, c.ReportingURL+"/jobs", map[string]string{"reportTypeId": reportTypeID, "name": name})
	if err != nil {
		return nil, err
	}
	var j Job
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, err
	}
	return &j, nil
}

// Reports lists all currently downloadable reports of a job.
func (c *Client) Reports(ctx context.Context, jobID string) ([]Report, error) {
	var out []Report
	page := ""
	for {
		v := url.Values{}
		if page != "" {
			v.Set("pageToken", page)
		}
		var resp struct {
			Reports       []Report `json:"reports"`
			NextPageToken string   `json:"nextPageToken"`
		}
		if err := c.getJSON(ctx, withQuery(c.ReportingURL+"/jobs/"+url.PathEscape(jobID)+"/reports", v), &resp); err != nil {
			return nil, err
		}
		out = append(out, resp.Reports...)
		if resp.NextPageToken == "" {
			return out, nil
		}
		page = resp.NextPageToken
	}
}

// Download fetches a report's CSV body.
func (c *Client) Download(ctx context.Context, downloadURL string) ([]byte, error) {
	return c.do(ctx, http.MethodGet, downloadURL, nil)
}
