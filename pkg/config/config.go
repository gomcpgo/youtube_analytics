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

	// DataDir holds tokens.json and the cached reach (impressions/CTR) reports.
	DataDir string

	Timeout time.Duration
	Debug   bool
}

// Load reads env vars. Nothing is required at startup; tools that need the
// OAuth client return an actionable error when it is missing.
func Load() *Config {
	c := &Config{
		ClientID:     strings.TrimSpace(os.Getenv("YOUTUBE_OAUTH_CLIENT_ID")),
		ClientSecret: strings.TrimSpace(os.Getenv("YOUTUBE_OAUTH_CLIENT_SECRET")),
		DataDir:      strings.TrimSpace(os.Getenv("YOUTUBE_ANALYTICS_DATA_DIR")),
		Timeout:      60 * time.Second,
	}
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
