package claudecodetest

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	claudecode "github.com/Hugo-M33/claude-code-go"
)

func TestMain(m *testing.M) {
	Main()
	os.Exit(m.Run())
}

func TestScriptedTurns(t *testing.T) {
	opts, readLog := Options(t, Script{SessionID: "s-42", Turns: []Turn{
		{Steps: []Step{
			{Text: "Adding it. "},
			{Note: "Adding the service."},
			{Server: "demo", Tool: "add", Input: json.RawMessage(`{"label":"API"}`)},
			{Text: "Done."},
		}, Usage: map[string]int64{"input_tokens": 12}},
		{Steps: []Step{{DelayMS: 2000}, {Text: "too late"}}},
	}})
	opts.IncludePartialMessages = true
	opts.SDKServers = []*claudecode.MCPServer{claudecode.NewMCPServer("demo", "0", claudecode.Tool{Name: "add",
		Handler: func(ctx context.Context, call claudecode.ToolCall) (claudecode.ToolResult, error) {
			return claudecode.TextResult("added " + string(call.Arguments)), nil
		}})}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := claudecode.Connect(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Send(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	var text, notes string
	var result *claudecode.ResultMessage
	for m, err := range c.Turn(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		if ev, ok := m.(*claudecode.StreamEvent); ok {
			text += ev.Delta.Text
			notes += ev.Delta.Thinking
		}
		if r, ok := m.(*claudecode.ResultMessage); ok {
			result = r
		}
	}
	if text != "Adding it. Done." || notes != "Adding the service." || result.SessionID != "s-42" || result.Result != "Adding it. Done." || string(result.Usage) != `{"input_tokens":12}` {
		t.Fatalf("text %q notes %q result %+v", text, notes, result)
	}
	// The second turn waits; an interrupt ends it.
	if err := c.Send(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	go func() { time.Sleep(100 * time.Millisecond); _ = c.Interrupt(ctx) }()
	for m, err := range c.Turn(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		if r, ok := m.(*claudecode.ResultMessage); ok && (r.Subtype != "error_during_execution" || !r.IsError) {
			t.Fatalf("interrupted turn %+v", r)
		}
	}
	l := readLog()
	if len(l.Prompts) != 2 || string(l.Prompts[0]) != `"first"` || len(l.Calls) != 1 || !strings.Contains(string(l.Calls[0].Result), `added {\"label\":\"API\"}`) {
		t.Fatalf("log %+v", l)
	}
	if !strings.Contains(strings.Join(l.Args, " "), "--input-format stream-json") {
		t.Fatalf("args %v", l.Args)
	}
	// A new process plays the next turn, and the log keeps growing.
	c.Close()
	opts.Resume = "s-42"
	var third string
	for m, err := range claudecode.Query(ctx, "third", opts) {
		if err != nil {
			t.Fatal(err)
		}
		if r, ok := m.(*claudecode.ResultMessage); ok {
			third = r.Result
		}
	}
	l = readLog()
	if third != "" || len(l.Prompts) != 3 || !strings.Contains(strings.Join(l.Args, " "), "--resume s-42") {
		t.Fatalf("third turn %q, log %d prompts, args %v", third, len(l.Prompts), l.Args)
	}
}

func TestAuthAndExit(t *testing.T) {
	no := false
	opts, _ := Options(t, Script{AuthLoggedIn: &no})
	ctx := context.Background()
	if a, err := claudecode.AuthStatus(ctx, opts); err != nil || a.LoggedIn {
		t.Fatalf("auth %+v %v", a, err)
	}
	crash, _ := Options(t, Script{ExitAfterInit: 4})
	c, err := claudecode.Connect(ctx, crash)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Receive(ctx)
	var pe *claudecode.ProcessError
	if pe, _ = err.(*claudecode.ProcessError); pe == nil || pe.ExitCode != 4 {
		t.Fatalf("exit: %v", err)
	}
}
