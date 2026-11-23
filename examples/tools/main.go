// Command tools serves a Go function to Claude as a tool, through an in-process MCP server,
// and prints the answer as it streams.
//
//	go run ./examples/tools "What time is it in Tokyo?"
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	claudecode "github.com/Hugo-M33/claude-code-go"
)

func main() {
	prompt := "What time is it in Tokyo?"
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}
	clock := claudecode.NewMCPServer("clock", "1.0.0", claudecode.Tool{
		Name:        "now",
		Description: "The current time in an IANA time zone.",
		InputSchema: json.RawMessage(`{"type":"object","required":["zone"],"properties":{"zone":{"type":"string","description":"An IANA zone such as Asia/Tokyo"}}}`),
		Handler: func(ctx context.Context, call claudecode.ToolCall) (claudecode.ToolResult, error) {
			var in struct{ Zone string }
			if err := json.Unmarshal(call.Arguments, &in); err != nil {
				return claudecode.ToolResult{}, err
			}
			loc, err := time.LoadLocation(in.Zone)
			if err != nil {
				return claudecode.ErrorResult("unknown zone " + in.Zone), nil
			}
			return claudecode.TextResult(time.Now().In(loc).Format(time.RFC1123)), nil
		},
	})
	opts := claudecode.Options{
		SystemPrompt:           claudecode.String("Answer briefly, using your tools."),
		Tools:                  claudecode.Strings(), // no built-in tools: only ours
		SettingSources:         claudecode.Strings(), // no user or project settings
		SDKServers:             []*claudecode.MCPServer{clock},
		StrictMCPConfig:        true,
		AllowedTools:           []string{claudecode.ToolName("clock", "now")},
		IncludePartialMessages: true,
		NoSessionPersistence:   true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for m, err := range claudecode.Query(ctx, prompt, opts) {
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		switch v := m.(type) {
		case *claudecode.StreamEvent:
			if v.Delta.Type == "text_delta" {
				fmt.Print(v.Delta.Text)
			}
		case *claudecode.ResultMessage:
			fmt.Printf("\n(%s, %d turns)\n", v.Subtype, v.NumTurns)
		}
	}
}
