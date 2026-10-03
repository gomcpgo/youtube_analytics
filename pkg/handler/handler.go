// Package handler is the MCP layer: tool definitions, argument extraction,
// routing to the analytics package, and response formatting.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gomcpgo/mcp/pkg/protocol"
	"github.com/gomcpgo/youtube_analytics/pkg/analytics"
	"github.com/gomcpgo/youtube_analytics/pkg/auth"
	"github.com/gomcpgo/youtube_analytics/pkg/config"
	"github.com/gomcpgo/youtube_analytics/pkg/reach"
	"github.com/gomcpgo/youtube_analytics/pkg/youtube"
	"golang.org/x/oauth2"
)

// Handler implements the gomcpgo ToolHandler interface.
type Handler struct {
	cfg      *config.Config
	accounts *auth.Manager
	logf     func(string)

	mu     sync.Mutex
	caches map[string]*reach.Cache
}

// New opens the token store and builds the handler.
func New(cfg *config.Config, logf func(string)) (*Handler, error) {
	if logf == nil {
		logf = func(string) {}
	}
	store, err := auth.OpenStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	var oc *oauth2.Config
	if cfg.HasClient() {
		oc = auth.OAuthConfig(cfg.ClientID, cfg.ClientSecret)
	}
	h := &Handler{cfg: cfg, logf: logf, caches: map[string]*reach.Cache{}}
	h.accounts = auth.NewManager(oc, store, cfg.Timeout, logf, func(a auth.Account, c *youtube.Client) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := h.scheduleReach(ctx, a, c); err != nil {
			logf("scheduling reach reports for " + a.Title + ": " + err.Error())
		}
	})
	return h, nil
}

// ListTools returns the static tool list.
func (h *Handler) ListTools(ctx context.Context) (*protocol.ListToolsResponse, error) {
	return &protocol.ListToolsResponse{Tools: tools}, nil
}

// CallTool dispatches by name. Errors come back as isError responses so the
// model sees the actionable message.
func (h *Handler) CallTool(ctx context.Context, req *protocol.CallToolRequest) (*protocol.CallToolResponse, error) {
	a := args(req.Arguments)
	var resp *protocol.CallToolResponse
	var err error
	switch req.Name {
	case "connect_channel":
		resp, err = h.connectChannel(ctx, a)
	case "list_channels":
		resp, err = h.listChannels(ctx, a)
	case "channel_overview":
		resp, err = h.channelOverview(ctx, a)
	case "list_videos":
		resp, err = h.listVideos(ctx, a)
	case "video_performance":
		resp, err = h.videoPerformance(ctx, a)
	case "video_report":
		resp, err = h.videoReport(ctx, a)
	case "video_retention":
		resp, err = h.videoRetention(ctx, a)
	case "traffic_sources":
		resp, err = h.trafficSources(ctx, a)
	case "audience":
		resp, err = h.audience(ctx, a)
	case "timeline":
		resp, err = h.timeline(ctx, a)
	case "impressions_ctr":
		resp, err = h.impressions(ctx, a)
	case "video_comments":
		resp, err = h.comments(ctx, a)
	case "analytics_query":
		resp, err = h.rawQuery(ctx, a)
	default:
		return errorResponse(fmt.Sprintf("unknown tool: %s", req.Name)), nil
	}
	if err != nil {
		msg := err.Error()
		if errors.Is(err, auth.ErrNoClient) && h.cfg.SetupHint() != "" {
			msg += ". " + h.cfg.SetupHint()
		}
		return errorResponse(msg), nil
	}
	return resp, nil
}

// session resolves the channel argument to an analytics service.
func (h *Handler) session(a args) (*analytics.Service, auth.Account, error) {
	acct, err := h.accounts.Resolve(a.str("channel"))
	if err != nil {
		return nil, acct, err
	}
	yt, err := h.accounts.Client(acct)
	if err != nil {
		return nil, acct, err
	}
	rc, err := h.reachCache(acct.ChannelID)
	if err != nil {
		h.logf("reach cache: " + err.Error())
		rc = nil
	}
	return analytics.New(yt, rc), acct, nil
}

func (h *Handler) reachCache(channelID string) (*reach.Cache, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.caches[channelID]; ok {
		return c, nil
	}
	c, err := reach.Open(filepath.Join(h.cfg.DataDir, "channels", channelID, "reach"))
	if err != nil {
		return nil, err
	}
	h.caches[channelID] = c
	return c, nil
}

func (h *Handler) scheduleReach(ctx context.Context, a auth.Account, c *youtube.Client) ([]string, error) {
	rc, err := h.reachCache(a.ChannelID)
	if err != nil {
		return nil, err
	}
	return rc.EnsureJobs(ctx, c)
}

// --- argument helpers ---------------------------------------------------

type args map[string]interface{}

func (a args) str(key string) string {
	if v, ok := a[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func (a args) num(key string, def float64) float64 {
	switch v := a[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f
		}
	case string:
		var f float64
		if _, err := fmt.Sscanf(v, "%g", &f); err == nil {
			return f
		}
	}
	return def
}

func (a args) intv(key string, def, lo, hi int) int {
	n := int(a.num(key, float64(def)))
	if n < lo {
		n = lo
	}
	if n > hi {
		n = hi
	}
	return n
}

func (a args) boolv(key string, def bool) bool {
	if v, ok := a[key].(bool); ok {
		return v
	}
	if s := strings.ToLower(a.str(key)); s != "" {
		return s == "true" || s == "1" || s == "yes"
	}
	return def
}

func (a args) strs(key string) []string {
	var out []string
	switch v := a[key].(type) {
	case []interface{}:
		for _, x := range v {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case string:
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func (a args) period() analytics.PeriodSpec {
	return analytics.PeriodSpec{Preset: a.str("period"), Start: a.str("start_date"), End: a.str("end_date")}
}

// videoID reads video_id (ID or URL); required reports whether it must be set.
func (a args) videoID(required bool) (string, error) {
	s := a.str("video_id")
	if s == "" {
		if required {
			return "", fmt.Errorf("video_id is required (a video ID or URL; use list_videos to find it)")
		}
		return "", nil
	}
	return youtube.ParseVideoID(s)
}

// --- response helpers ---------------------------------------------------

// textResponse returns markdown only. Structured content is deliberately
// not sent: clients such as Claude Code show it to the model instead of the
// text, which would drop the notes and guidance written into the text.
func textResponse(text string) *protocol.CallToolResponse {
	return &protocol.CallToolResponse{Content: []protocol.ToolContent{{Type: "text", Text: text}}}
}

func errorResponse(msg string) *protocol.CallToolResponse {
	return &protocol.CallToolResponse{Content: []protocol.ToolContent{{Type: "text", Text: "Error: " + msg}}, IsError: true}
}
