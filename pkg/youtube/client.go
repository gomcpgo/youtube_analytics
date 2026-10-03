// Package youtube is a thin HTTP client for the three Google APIs this server
// uses: Data API v3 (channel, videos, comments), Analytics API v2 (targeted
// queries) and Reporting API v1 (bulk reach reports with impressions/CTR).
package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // Pacific time on systems without a zoneinfo database

	"golang.org/x/oauth2"
)

// Pacific is the time zone YouTube Analytics uses for dates.
var Pacific = mustLoad("America/Los_Angeles")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// BaseURLs are the API roots new clients use; tests point them at fakes.
var BaseURLs = struct{ Data, Analytics, Reporting string }{
	Data:      "https://www.googleapis.com/youtube/v3",
	Analytics: "https://youtubeanalytics.googleapis.com/v2",
	Reporting: "https://youtubereporting.googleapis.com/v1",
}

// Client calls the YouTube APIs with an OAuth-authorized HTTP client.
type Client struct {
	http *http.Client
	logf func(string)

	DataURL      string
	AnalyticsURL string
	ReportingURL string

	mu     sync.Mutex
	videos map[string]cachedVideo
}

type cachedVideo struct {
	v  *Video
	at time.Time
}

// New wraps an authorized HTTP client.
func New(hc *http.Client, logf func(string)) *Client {
	if logf == nil {
		logf = func(string) {}
	}
	return &Client{
		http:         hc,
		logf:         logf,
		DataURL:      BaseURLs.Data,
		AnalyticsURL: BaseURLs.Analytics,
		ReportingURL: BaseURLs.Reporting,
		videos:       map[string]cachedVideo{},
	}
}

// APIError is a Google API error with an actionable hint.
type APIError struct {
	Status  int
	Reason  string
	Message string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("YouTube API error %d", e.Status)
	if e.Reason != "" {
		msg += " (" + e.Reason + ")"
	}
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if h := e.hint(); h != "" {
		msg += ". " + h
	}
	return msg
}

func (e *APIError) hint() string {
	r := strings.ToLower(e.Reason)
	switch {
	case e.Status == http.StatusUnauthorized:
		return "The authorization expired or was revoked; run connect_channel again for this channel"
	case strings.Contains(r, "quota") || strings.Contains(r, "ratelimit") || strings.Contains(r, "dailylimit"):
		return "API quota is exhausted; it resets at midnight Pacific time"
	case strings.Contains(r, "insufficientpermissions") || strings.Contains(r, "scope_insufficient"):
		return "The stored authorization lacks a required scope; run connect_channel again"
	case strings.Contains(r, "accessnotconfigured") || strings.Contains(r, "service_disabled"):
		return "Enable YouTube Data API v3, YouTube Analytics API and YouTube Reporting API in your Google Cloud project"
	case e.Status == http.StatusBadRequest:
		return "YouTube Analytics does not support this combination of metrics, dimensions and filters, or the dates are invalid"
	case e.Status == http.StatusForbidden:
		return "This data is not available for this channel (revenue metrics need YouTube Partner Program membership)"
	}
	return ""
}

// IsServerError reports whether err is an HTTP 5xx from the API.
func IsServerError(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status >= 500
}

// IsBadRequest reports whether err is an HTTP 400 from the API.
func IsBadRequest(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusBadRequest
}

func parseAPIError(status int, body []byte) error {
	var env struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	e := &APIError{Status: status}
	if json.Unmarshal(body, &env) == nil {
		e.Message = env.Error.Message
		for _, d := range env.Error.Details {
			if d.Reason != "" {
				e.Reason = d.Reason
			}
		}
		if len(env.Error.Errors) > 0 && env.Error.Errors[0].Reason != "" {
			e.Reason = env.Error.Errors[0].Reason
		}
		if e.Reason == "" {
			e.Reason = env.Error.Status
		}
	}
	if e.Message == "" {
		e.Message = strings.TrimSpace(string(body))
		if len(e.Message) > 300 {
			e.Message = e.Message[:300]
		}
	}
	return e
}

func (c *Client) do(ctx context.Context, method, u string, body interface{}) ([]byte, error) {
	return c.doWith(ctx, c.http, method, u, body)
}

// doWith sends a request with the given HTTP client (OAuth or plain).
func (c *Client) doWith(ctx context.Context, hc *http.Client, method, u string, body interface{}) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	start := time.Now()
	resp, err := hc.Do(req)
	if err != nil {
		var re *ReauthError
		if errors.As(err, &re) {
			return nil, re
		}
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	c.logf(fmt.Sprintf("%s %s -> %d (%s)", method, redact(u), resp.StatusCode, time.Since(start).Round(time.Millisecond)))
	if resp.StatusCode >= 300 {
		return nil, parseAPIError(resp.StatusCode, b)
	}
	return b, nil
}

func (c *Client) getJSON(ctx context.Context, u string, out interface{}) error {
	b, err := c.do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

var keyParam = regexp.MustCompile(`([?&]key=)[^&]+`)

func redact(u string) string {
	u = keyParam.ReplaceAllString(u, "${1}REDACTED")
	if i := strings.Index(u, "?"); i > 0 && len(u) > 160 {
		return u[:160] + "…"
	}
	return u
}

func withQuery(base string, v url.Values) string { return base + "?" + v.Encode() }

// ReauthError means the stored refresh token no longer works.
type ReauthError struct{ Cause error }

func (e *ReauthError) Error() string {
	return "the stored authorization for this channel no longer works (" + e.Cause.Error() + "). " +
		"Run connect_channel again. If this happens every 7 days, your OAuth consent screen is in " +
		"'Testing' mode; set its publishing status to 'In production' in Google Cloud Console"
}

func (e *ReauthError) Unwrap() error { return e.Cause }

// WrapTokenError converts an invalid_grant refresh failure into ReauthError.
func WrapTokenError(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) && (re.ErrorCode == "invalid_grant" || re.Response != nil && re.Response.StatusCode == http.StatusBadRequest) {
		return &ReauthError{Cause: err}
	}
	return err
}
