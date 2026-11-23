package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeOptions runs the fake CLI for a scenario; its arguments and environment are written to a
// file the test can read.
func fakeOptions(t *testing.T, scenario string) (Options, string) {
	t.Helper()
	argsFile := filepath.Join(t.TempDir(), "args.json")
	return Options{
		CLIPath: os.Args[0],
		Env:     []string{"CLAUDE_CODE_GO_FAKE=" + scenario, "CLAUDE_CODE_GO_FAKE_ARGS=" + argsFile},
	}, argsFile
}

func ctx(t *testing.T) context.Context {
	t.Helper()
	c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestQueryYieldsTheTurn(t *testing.T) {
	opts, argsFile := fakeOptions(t, "echo")
	opts.SystemPrompt = String("Be brief.")
	opts.Tools = Strings()
	opts.SettingSources = Strings()
	opts.NoAPIKey = true
	opts.Model = "claude-opus-5-5"
	opts.MaxTurns = 3
	t.Setenv("ANTHROPIC_API_KEY", "sk-should-not-pass")
	var types []string
	var result *ResultMessage
	for m, err := range Query(ctx(t), "hello", opts) {
		if err != nil {
			t.Fatal(err)
		}
		types = append(types, m.Type())
		if r, ok := m.(*ResultMessage); ok {
			result = r
		}
		if s, ok := m.(*SystemMessage); ok && s.APIKeySource != "none" {
			t.Errorf("the API key reached the process: %s", s.APIKeySource)
		}
	}
	if result == nil || result.Result != "echo: HELLO" || result.SessionID != "s1" {
		t.Fatalf("result %+v (messages %v)", result, types)
	}
	// Unknown types come through; lines that are not JSON do not.
	if strings.Join(types, ",") != "system,stray,result" {
		t.Fatalf("messages %v", types)
	}
	var seen struct {
		Args       []string `json:"args"`
		APIKey     string   `json:"apiKey"`
		Entrypoint string   `json:"entrypoint"`
	}
	b, _ := os.ReadFile(argsFile)
	if err := json.Unmarshal(b, &seen); err != nil {
		t.Fatal(err)
	}
	cmd := strings.Join(seen.Args, " ")
	for _, want := range []string{"--output-format stream-json --verbose --input-format stream-json", "--system-prompt Be brief.", "--tools  ", "--setting-sources ", "--max-turns 3", "--model claude-opus-5-5"} {
		if !strings.Contains(cmd+" ", want) {
			t.Errorf("command line lacks %q: %s", want, cmd)
		}
	}
	if seen.APIKey != "" || seen.Entrypoint != "sdk-go-unofficial" {
		t.Errorf("environment: %+v", seen)
	}
}

func TestInProcessMCPTools(t *testing.T) {
	opts, argsFile := fakeOptions(t, "tools")
	var gotCall ToolCall
	server := NewMCPServer("aboard", "0.1.0", Tool{
		Name:        "add",
		Description: "Adds one element.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"label":{"type":"string"}},"required":["label"]}`),
		Handler: func(ctx context.Context, call ToolCall) (ToolResult, error) {
			gotCall = call
			var in struct{ Label string }
			if err := json.Unmarshal(call.Arguments, &in); err != nil {
				return ToolResult{}, err
			}
			return TextResult("Added '" + in.Label + "'."), nil
		},
	})
	opts.SDKServers = []*MCPServer{server}
	opts.StrictMCPConfig = true
	opts.AllowedTools = []string{ToolName("aboard", "add")}
	opts.IncludePartialMessages = true
	c, err := Connect(ctx(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Send(ctx(t), "Orders API"); err != nil {
		t.Fatal(err)
	}
	var init *SystemMessage
	var deltas, toolUses []string
	var result *ResultMessage
	var final string
	for m, err := range c.Turn(ctx(t)) {
		if err != nil {
			t.Fatal(err)
		}
		switch v := m.(type) {
		case *SystemMessage:
			init = v
		case *StreamEvent:
			if v.EventType == "content_block_delta" && v.Delta.Type == "text_delta" {
				deltas = append(deltas, v.Delta.Text)
			}
		case *AssistantMessage:
			for _, b := range v.Message.Content {
				if b.Type == "tool_use" {
					toolUses = append(toolUses, b.Name+" "+string(b.Input))
				}
			}
			final = v.Message.Text()
		case *ResultMessage:
			result = v
		}
	}
	if init == nil || strings.Join(init.Tools, ",") != "mcp__aboard__add" || init.MCPServers[0].Status != "connected" {
		t.Fatalf("init %+v", init)
	}
	if strings.Join(deltas, "") != "Adding " || len(toolUses) != 1 || toolUses[0] != `mcp__aboard__add {"label":"Orders API"}` {
		t.Fatalf("deltas %v tool uses %v", deltas, toolUses)
	}
	if gotCall.Name != "add" || gotCall.ToolUseID != "toolu_1" || string(gotCall.Arguments) != `{"label":"Orders API"}` {
		t.Fatalf("call %+v", gotCall)
	}
	if result == nil || result.Result != "Added 'Orders API'." || result.TotalCostUSD != 0.014 || result.NumTurns != 2 {
		t.Fatalf("result %+v", result)
	}
	// An unknown tool is a failed call; an unknown method a JSON-RPC error.
	if final != "Done. missing=true unknown=true" {
		t.Fatalf("final text %q", final)
	}
	b, _ := os.ReadFile(argsFile)
	for _, want := range []string{`--mcp-config","{\"mcpServers\":{\"aboard\":{\"name\":\"aboard\",\"type\":\"sdk\"}}}"`, `"--strict-mcp-config"`, `"--allowedTools","mcp__aboard__add"`, `"--include-partial-messages"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("args lack %s: %s", want, b)
		}
	}
}

func TestCanUseTool(t *testing.T) {
	for _, c := range []struct {
		name string
		res  PermissionResult
		want string
	}{
		{"allow", Allow(), "allow <nil>"},
		{"deny", Deny("not here"), "deny not here"},
	} {
		t.Run(c.name, func(t *testing.T) {
			opts, argsFile := fakeOptions(t, "permission")
			var asked PermissionRequest
			opts.CanUseTool = func(ctx context.Context, req PermissionRequest) (PermissionResult, error) {
				asked = req
				return c.res, nil
			}
			var result string
			for m, err := range Query(ctx(t), "go", opts) {
				if err != nil {
					t.Fatal(err)
				}
				if r, ok := m.(*ResultMessage); ok {
					result = r.Result
				}
			}
			if result != c.want || asked.ToolName != "Bash" || string(asked.Input) != `{"command":"ls"}` || asked.ToolUseID != "toolu_2" {
				t.Fatalf("result %q, asked %+v", result, asked)
			}
			b, _ := os.ReadFile(argsFile)
			if !strings.Contains(string(b), `"--permission-prompt-tool","stdio"`) {
				t.Fatalf("args %s", b)
			}
		})
	}
}

func TestInterrupt(t *testing.T) {
	opts, _ := fakeOptions(t, "slow")
	c, err := Connect(ctx(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Send(ctx(t), "talk"); err != nil {
		t.Fatal(err)
	}
	interrupted := false
	start := time.Now()
	var result *ResultMessage
	for m, err := range c.Turn(ctx(t)) {
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := m.(*StreamEvent); ok && !interrupted {
			interrupted = true
			if err := c.Interrupt(ctx(t)); err != nil {
				t.Fatal(err)
			}
		}
		if r, ok := m.(*ResultMessage); ok {
			result = r
		}
	}
	if result == nil || result.Result != "interrupted" || !result.IsError || time.Since(start) > 2*time.Second {
		t.Fatalf("result %+v after %v", result, time.Since(start))
	}
	// A refused control request is an error saying so.
	var ce *ControlError
	if err := c.SetModel(ctx(t), "nope"); !errors.As(err, &ce) || ce.Message != "no such model" {
		t.Fatalf("set model: %v", err)
	}
}

func TestCancelledToolCall(t *testing.T) {
	opts, _ := fakeOptions(t, "cancel")
	cancelled := make(chan struct{})
	opts.SDKServers = []*MCPServer{NewMCPServer("aboard", "0", Tool{Name: "slow", Handler: func(ctx context.Context, call ToolCall) (ToolResult, error) {
		select {
		case <-ctx.Done():
			close(cancelled)
			return ToolResult{}, ctx.Err()
		case <-time.After(5 * time.Second):
			return TextResult("late"), nil
		}
	}})}
	for m, err := range Query(ctx(t), "go", opts) {
		if err != nil {
			t.Fatal(err)
		}
		_ = m
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("the tool's context was not cancelled")
	}
}

func TestProcessFailure(t *testing.T) {
	opts, _ := fakeOptions(t, "crash")
	c, err := Connect(ctx(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.Send(ctx(t), "x")
	_, err = c.Receive(ctx(t))
	var pe *ProcessError
	if !errors.As(err, &pe) || pe.ExitCode != 3 || !strings.Contains(pe.Stderr, "boom") {
		t.Fatalf("error %v", err)
	}
	if err := c.Send(ctx(t), "again"); !errors.Is(err, ErrClosed) {
		t.Fatalf("send after the end: %v", err)
	}
}

func TestCLINotFound(t *testing.T) {
	_, err := Connect(ctx(t), Options{CLIPath: filepath.Join(t.TempDir(), "no-claude-here")})
	var nf *CLINotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("error %v", err)
	}
}
