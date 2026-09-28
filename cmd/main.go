// youtube_analytics is an MCP server for analyzing YouTube channels you own:
// retention and drop-off points, impressions and CTR, traffic sources,
// audience, per-video performance and comments, via Google's official APIs.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/gomcpgo/mcp/pkg/handler"
	"github.com/gomcpgo/mcp/pkg/protocol"
	"github.com/gomcpgo/mcp/pkg/server"
	"github.com/gomcpgo/youtube_analytics/pkg/config"
	ytahandler "github.com/gomcpgo/youtube_analytics/pkg/handler"
)

const version = "0.1.0"

func main() {
	var (
		tool     string
		argsJSON string
		listOnly bool
		doAuth   bool
	)
	flag.StringVar(&tool, "tool", "", "Terminal mode: call this tool and print the result")
	flag.StringVar(&argsJSON, "args", "{}", "Terminal mode: JSON arguments for -tool")
	flag.BoolVar(&listOnly, "list", false, "Terminal mode: list tools and exit")
	flag.BoolVar(&doAuth, "auth", false, "Terminal mode: connect a channel through the browser")
	flag.Parse()

	log.SetOutput(os.Stderr)
	cfg := config.Load()
	logf := func(string) {}
	if cfg.Debug {
		logf = func(s string) { log.Println(s) }
	}
	h, err := ytahandler.New(cfg, logf)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	switch {
	case listOnly:
		resp, _ := h.ListTools(ctx)
		for _, t := range resp.Tools {
			fmt.Printf("%-20s %s\n", t.Name, firstSentence(t.Description))
		}
		return
	case doAuth:
		if err := h.Connect(ctx, func(f string, a ...interface{}) { fmt.Printf(f, a...) }); err != nil {
			log.Fatal(err)
		}
		return
	case tool != "":
		var a map[string]interface{}
		if err := json.Unmarshal([]byte(argsJSON), &a); err != nil {
			log.Fatalf("bad -args JSON: %v", err)
		}
		resp, err := h.CallTool(ctx, &protocol.CallToolRequest{Name: tool, Arguments: a})
		if err != nil {
			log.Fatal(err)
		}
		for _, c := range resp.Content {
			fmt.Println(c.Text)
		}
		if resp.IsError {
			os.Exit(1)
		}
		return
	}

	registry := handler.NewHandlerRegistry()
	registry.RegisterToolHandler(h)
	srv := server.New(server.Options{
		Name:       "youtube_analytics",
		Title:      "YouTube Analytics",
		Version:    version,
		WebsiteURL: "https://github.com/gomcpgo/youtube_analytics",
		Registry:   registry,
	})
	if err := srv.Run(); err != nil {
		log.Fatal(err)
	}
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}
