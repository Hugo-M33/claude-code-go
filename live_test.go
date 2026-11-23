package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestLive drives the real `claude` (signed in, or with an API key) through one prompt that
// calls an in-process tool. It runs only with CLAUDE_CODE_GO_LIVE=1: it uses the account.
func TestLive(t *testing.T) {
	if os.Getenv("CLAUDE_CODE_GO_LIVE") != "1" {
		t.Skip("set CLAUDE_CODE_GO_LIVE=1 to run against the real claude")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	called := ""
	server := NewMCPServer("demo", "0.0.1", Tool{
		Name:        "add",
		Description: "Adds one element to the diagram.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["label"],"properties":{"label":{"type":"string"}}}`),
		Handler: func(ctx context.Context, call ToolCall) (ToolResult, error) {
			called = string(call.Arguments)
			return TextResult("Added e1 'Orders API'."), nil
		},
	})
	opts := Options{
		NoAPIKey:             os.Getenv("CLAUDE_CODE_GO_LIVE_API_KEY") != "1",
		SystemPrompt:         String("You add elements to a diagram with the tools you have. Be brief."),
		Tools:                Strings(),
		SettingSources:       Strings(),
		SDKServers:           []*MCPServer{server},
		StrictMCPConfig:      true,
		AllowedTools:         []string{ToolName("demo", "add")},
		NoSessionPersistence: true,
		MaxTurns:             4,
		Effort:               "low",
		Cwd:                  t.TempDir(),
	}
	var result *ResultMessage
	for m, err := range Query(ctx, "Add an element labelled Orders API, then reply with one word.", opts) {
		if err != nil {
			t.Fatal(err)
		}
		if s, ok := m.(*SystemMessage); ok && s.Subtype == "init" {
			t.Logf("model %s, tools %v, credentials %s", s.Model, s.Tools, s.APIKeySource)
		}
		if r, ok := m.(*ResultMessage); ok {
			result = r
		}
	}
	if result == nil || result.IsError || called == "" {
		t.Fatalf("result %+v, tool called with %q", result, called)
	}
	t.Logf("tool called with %s; %d turns; reply %q", called, result.NumTurns, result.Result)
}
