package youtube

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestParseISODuration(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"PT45S": 45 * time.Second, "PT10M": 10 * time.Minute, "PT1H2M3S": time.Hour + 2*time.Minute + 3*time.Second,
		"P1DT1H": 25 * time.Hour, "P0D": 0, "garbage": 0,
	} {
		if got := ParseISODuration(in); got != want {
			t.Errorf("%s = %v, want %v", in, got, want)
		}
	}
}

func TestParseVideoID(t *testing.T) {
	for _, in := range []string{
		"dQw4w9WgXcQ", "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=10s", "https://youtu.be/dQw4w9WgXcQ",
		"https://www.youtube.com/shorts/dQw4w9WgXcQ", "https://www.youtube.com/live/dQw4w9WgXcQ?si=x", "https://studio.youtube.com/video/dQw4w9WgXcQ/analytics",
	} {
		if id, err := ParseVideoID(in); err != nil || id != "dQw4w9WgXcQ" {
			t.Errorf("%s = %q, %v", in, id, err)
		}
	}
	if _, err := ParseVideoID("hello"); err == nil {
		t.Error("expected error")
	}
}

func TestAPIErrorHints(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"quota":       {403, `{"error":{"code":403,"message":"The request cannot be completed","errors":[{"reason":"quotaExceeded"}]}}`},
		"enable":      {403, `{"error":{"code":403,"message":"API not enabled","status":"PERMISSION_DENIED","details":[{"reason":"SERVICE_DISABLED"}]}}`},
		"connect":     {401, `{"error":{"code":401,"message":"Invalid Credentials"}}`},
		"combination": {400, `{"error":{"code":400,"message":"The query is not supported."}}`},
	}
	for want, c := range cases {
		err := parseAPIError(c.status, []byte(c.body))
		if !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Errorf("%d: %q should mention %q", c.status, err, want)
		}
	}
	if !IsBadRequest(parseAPIError(400, []byte("{}"))) {
		t.Error("IsBadRequest")
	}
}

func TestWrapTokenError(t *testing.T) {
	re := &oauth2.RetrieveError{Response: &http.Response{StatusCode: 400}, ErrorCode: "invalid_grant"}
	var target *ReauthError
	if !errors.As(WrapTokenError(re), &target) || !strings.Contains(target.Error(), "In production") {
		t.Errorf("invalid_grant should become a ReauthError with the Testing-mode hint")
	}
	plain := errors.New("network down")
	if WrapTokenError(plain) != plain {
		t.Error("other errors pass through")
	}
}
