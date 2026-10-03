// Package config reads the process configuration from environment variables.
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the process configuration.
type Config struct {
	// OAuth client of type "Desktop app" from the user's own Google Cloud project.
	ClientID     string
	ClientSecret string

	// APIKey is an optional YouTube Data API key, used only to read comments.
	APIKey string

	// DataDir holds tokens.json and the cached reach (impressions/CTR) reports.
	DataDir string

	Timeout time.Duration
	Debug   bool

	// Unexpanded names variables that arrived as a literal "${NAME}": the MCP
	// client config references them but they are not set in the client's
	// environment. They are treated as unset.
	Unexpanded []string
}

// Load reads env vars. Nothing is required at startup; tools that need the
// OAuth client return an actionable error when it is missing.
func Load() *Config {
	c := &Config{
		Timeout: 60 * time.Second,
	}
	c.ClientID = c.env("YOUTUBE_OAUTH_CLIENT_ID")
	c.ClientSecret = c.env("YOUTUBE_OAUTH_CLIENT_SECRET")
	c.APIKey = c.env("YOUTUBE_API_KEY")
	c.DataDir = c.env("YOUTUBE_ANALYTICS_DATA_DIR")
	if c.DataDir == "" {
		c.DataDir = defaultDataDir()
	}
	if v := os.Getenv("YOUTUBE_ANALYTICS_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			c.Timeout = d
		} else if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			c.Timeout = time.Duration(secs) * time.Second
		}
	}
	d := os.Getenv("YOUTUBE_ANALYTICS_DEBUG")
	c.Debug = d == "1" || strings.EqualFold(d, "true")
	return c
}

// env reads a variable, treating an unexpanded "${...}" placeholder as unset.
func (c *Config) env(name string) string {
	v := strings.TrimSpace(os.Getenv(name))
	if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
		c.Unexpanded = append(c.Unexpanded, name)
		return ""
	}
	return v
}

// SetupHint explains unexpanded placeholders, or returns "".
func (c *Config) SetupHint() string {
	if len(c.Unexpanded) == 0 {
		return ""
	}
	return "The MCP client passed these as literal ${...} placeholders because they are not set in the environment " +
		"it was started from: " + strings.Join(c.Unexpanded, ", ") + ". Export them in that shell (or its startup file) " +
		"and restart the MCP client from a new terminal."
}

// HasClient reports whether the OAuth client is configured.
func (c *Config) HasClient() bool { return c.ClientID != "" && c.ClientSecret != "" }

func defaultDataDir() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "gomcpgo", "youtube_analytics")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".gomcpgo", "youtube_analytics")
	}
	return "youtube_analytics_data"
}
