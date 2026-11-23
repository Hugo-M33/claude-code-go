// Package claudecodetest is a scripted fake of the `claude` program, for testing code that uses
// claudecode without an account. The test binary plays the CLI: call Main from TestMain, and
// Options returns claudecode.Options that run it with a Script. The fake answers the handshake,
// lists the tools of the in-process MCP servers, and for each prompt plays one Turn: text and
// progress notes as stream events, tool calls through the MCP servers (their results go into
// the transcript), then a result. It records the prompts, calls, results and command line in a
// Log the test reads back.
package claudecodetest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	claudecode "github.com/Hugo-M33/claude-code-go"
)

const envScript = "CLAUDECODETEST_SCRIPT"

// Script is what the fake does: one Turn per prompt, in order (past the last, an empty turn).
type Script struct {
	Turns []Turn `json:"turns"`
	// SessionID is reported in every message ("fake-session" by default).
	SessionID string `json:"sessionId,omitempty"`
	// AuthLoggedIn is what `claude auth status` reports (true by default when nil).
	AuthLoggedIn *bool `json:"authLoggedIn,omitempty"`
	// ExitAfterInit makes the process exit with this status right after the handshake.
	ExitAfterInit int `json:"exitAfterInit,omitempty"`
}

// Turn is the answer to one prompt.
type Turn struct {
	Steps []Step `json:"steps"`
	// Subtype of the result: "success" by default; "error_max_turns" and others end it so.
	Subtype string `json:"subtype,omitempty"`
	// Result is the result's text (the turn's last text by default).
	Result string `json:"result,omitempty"`
	// Usage is reported in the result.
	Usage map[string]int64 `json:"usage,omitempty"`
}

// Step is one thing the model does: write text, write a progress note, or call a tool.
// DelayMS waits before it; an interrupt during the wait ends the turn.
type Step struct {
	Text    string          `json:"text,omitempty"`
	Note    string          `json:"note,omitempty"`
	Server  string          `json:"server,omitempty"`
	Tool    string          `json:"tool,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	DelayMS int             `json:"delayMs,omitempty"`
}

// Log is what the fake saw and did: the command line and environment of the last process it
// ran as, and every prompt and call over all of them.
type Log struct {
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	Prompts []json.RawMessage `json:"prompts"`
	Calls   []LoggedCall      `json:"calls"`
}

// LoggedCall is a tool call and the result the host returned.
type LoggedCall struct {
	Server, Tool string
	Input        json.RawMessage
	Result       json.RawMessage
}

// Main runs the fake when the test binary was started as one; call it first in TestMain:
//
//	func TestMain(m *testing.M) { claudecodetest.Main(); os.Exit(m.Run()) }
func Main() {
	if path := os.Getenv(envScript); path != "" {
		os.Exit(run(path))
	}
}

// Options are options that run the fake with the script; the returned function reads its Log.
func Options(t testing.TB, s Script) (claudecode.Options, func() Log) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "script.json")
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	read := func() Log {
		var l Log
		data, err := os.ReadFile(path + ".log")
		if err == nil {
			_ = json.Unmarshal(data, &l)
		}
		return l
	}
	return claudecode.Options{CLIPath: os.Args[0], Env: []string{envScript + "=" + path}}, read
}

// --- The fake ---------------------------------------------------------------------------------

type fake struct {
	script  Script
	logPath string
	log     Log
	lmu     sync.Mutex

	wmu     sync.Mutex
	mu      sync.Mutex
	waiting map[string]chan json.RawMessage
	n       int
	servers []string
	tools   []string
	inbox   chan map[string]any
	session string
}

func run(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "claudecodetest:", err)
		return 2
	}
	f := &fake{logPath: path + ".log", waiting: map[string]chan json.RawMessage{}, inbox: make(chan map[string]any, 64)}
	if err := json.Unmarshal(data, &f.script); err != nil {
		fmt.Fprintln(os.Stderr, "claudecodetest:", err)
		return 2
	}
	f.session = f.script.SessionID
	if f.session == "" {
		f.session = "fake-session"
	}
	if len(os.Args) == 3 && os.Args[1] == "auth" && os.Args[2] == "status" {
		in := f.script.AuthLoggedIn == nil || *f.script.AuthLoggedIn
		b, _ := json.Marshal(map[string]any{"loggedIn": in, "authMethod": "claude.ai", "apiProvider": "firstParty"})
		os.Stdout.Write(b)
		if !in {
			return 1
		}
		return 0
	}
	// Prompts and calls accumulate over the processes a test starts (one per session, or per
	// turn): the n-th prompt the fake receives, in any process, plays Turns[n].
	if prev, err := os.ReadFile(f.logPath); err == nil {
		_ = json.Unmarshal(prev, &f.log)
	}
	f.log.Args = os.Args[1:]
	f.log.Env = map[string]string{"ANTHROPIC_API_KEY": os.Getenv("ANTHROPIC_API_KEY"), "CLAUDE_CODE_ENTRYPOINT": os.Getenv("CLAUDE_CODE_ENTRYPOINT")}
	f.save()
	for i, a := range os.Args {
		if a == "--mcp-config" && i+1 < len(os.Args) {
			var cfg struct {
				MCPServers map[string]struct {
					Type string `json:"type"`
				} `json:"mcpServers"`
			}
			_ = json.Unmarshal([]byte(os.Args[i+1]), &cfg)
			for name, s := range cfg.MCPServers {
				if s.Type == "sdk" {
					f.servers = append(f.servers, name)
				}
			}
		}
	}
	go f.read()
	for m := range f.inbox {
		switch m["type"] {
		case "control_request":
			f.control(m)
			if f.script.ExitAfterInit != 0 {
				fmt.Fprintln(os.Stderr, "claudecodetest: exiting as scripted")
				return f.script.ExitAfterInit
			}
		case "user":
			msg, _ := m["message"].(map[string]any)
			b, _ := json.Marshal(msg["content"])
			f.lmu.Lock()
			turn := len(f.log.Prompts)
			f.log.Prompts = append(f.log.Prompts, b)
			f.lmu.Unlock()
			f.save()
			var t Turn
			if turn < len(f.script.Turns) {
				t = f.script.Turns[turn]
			}
			f.play(t)
		}
	}
	return 0
}

func (f *fake) save() {
	f.lmu.Lock()
	defer f.lmu.Unlock()
	b, _ := json.Marshal(f.log)
	_ = os.WriteFile(f.logPath, b, 0o644)
}

func (f *fake) send(v any) {
	b, _ := json.Marshal(v)
	f.wmu.Lock()
	defer f.wmu.Unlock()
	os.Stdout.Write(append(b, '\n'))
}

// read routes stdin: answers to the fake's requests to their waiters, the rest to the inbox.
func (f *fake) read() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m["type"] == "control_response" {
			r, _ := m["response"].(map[string]any)
			id, _ := r["request_id"].(string)
			f.mu.Lock()
			ch := f.waiting[id]
			delete(f.waiting, id)
			f.mu.Unlock()
			if ch != nil {
				b, _ := json.Marshal(r)
				ch <- b
			}
			continue
		}
		f.inbox <- m
	}
	close(f.inbox)
}

func (f *fake) ask(req map[string]any) map[string]any {
	f.mu.Lock()
	f.n++
	id := fmt.Sprintf("fake_%d", f.n)
	ch := make(chan json.RawMessage, 1)
	f.waiting[id] = ch
	f.mu.Unlock()
	f.send(map[string]any{"type": "control_request", "request_id": id, "request": req})
	select {
	case b := <-ch:
		var r map[string]any
		_ = json.Unmarshal(b, &r)
		return r
	case <-time.After(60 * time.Second):
		fmt.Fprintln(os.Stderr, "claudecodetest: no answer to", req["subtype"])
		os.Exit(3)
	}
	return nil
}

func (f *fake) mcp(server, method string, id int, params any) map[string]any {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if id > 0 {
		msg["id"] = id
	}
	if params != nil {
		msg["params"] = params
	}
	r := f.ask(map[string]any{"subtype": "mcp_message", "server_name": server, "message": msg})
	resp, _ := r["response"].(map[string]any)
	out, _ := resp["mcp_response"].(map[string]any)
	return out
}

func (f *fake) control(m map[string]any) {
	req, _ := m["request"].(map[string]any)
	id := m["request_id"]
	ok := func(resp any) {
		f.send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": id, "response": resp}})
	}
	switch req["subtype"] {
	case "initialize":
		for _, s := range f.servers {
			f.mcp(s, "initialize", 1, map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}})
			f.mcp(s, "notifications/initialized", 0, nil)
			list := f.mcp(s, "tools/list", 2, nil)
			res, _ := list["result"].(map[string]any)
			ts, _ := res["tools"].([]any)
			for _, t := range ts {
				name, _ := t.(map[string]any)["name"].(string)
				f.tools = append(f.tools, claudecode.ToolName(s, name))
			}
		}
		ok(map[string]any{"commands": []any{}})
	default:
		ok(map[string]any{})
	}
}

// play runs one turn; an interrupt arriving during a step's delay ends it.
func (f *fake) play(t Turn) {
	f.send(map[string]any{"type": "system", "subtype": "init", "session_id": f.session, "model": "fake-model", "tools": f.tools,
		"mcp_servers": serverStatus(f.servers), "apiKeySource": apiKeySource()})
	text := ""
	n := 0
	for _, s := range t.Steps {
		if s.DelayMS > 0 && f.wait(time.Duration(s.DelayMS)*time.Millisecond) {
			f.result("error_during_execution", true, text, t.Usage)
			return
		}
		switch {
		case s.Text != "":
			text += s.Text
			f.event(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": s.Text}})
		case s.Note != "":
			f.event(map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "thinking_delta", "thinking": s.Note}})
		case s.Tool != "":
			n++
			useID := fmt.Sprintf("toolu_fake_%d", n)
			input := s.Input
			if len(input) == 0 {
				input = json.RawMessage(`{}`)
			}
			f.send(map[string]any{"type": "assistant", "session_id": f.session, "parent_tool_use_id": nil, "message": map[string]any{"role": "assistant",
				"content": []any{map[string]any{"type": "tool_use", "id": useID, "name": claudecode.ToolName(s.Server, s.Tool), "input": input}}}})
			r := f.mcp(s.Server, "tools/call", 100+n, map[string]any{"name": s.Tool, "arguments": input, "_meta": map[string]any{"claudecode/toolUseId": useID}})
			res, _ := json.Marshal(r["result"])
			f.lmu.Lock()
			f.log.Calls = append(f.log.Calls, LoggedCall{Server: s.Server, Tool: s.Tool, Input: input, Result: res})
			f.lmu.Unlock()
			f.save()
			var tr struct {
				Content []any `json:"content"`
				IsError bool  `json:"isError"`
			}
			_ = json.Unmarshal(res, &tr)
			f.send(map[string]any{"type": "user", "session_id": f.session, "parent_tool_use_id": nil, "message": map[string]any{"role": "user",
				"content": []any{map[string]any{"type": "tool_result", "tool_use_id": useID, "content": tr.Content, "is_error": tr.IsError}}}})
		}
	}
	if text != "" {
		f.send(map[string]any{"type": "assistant", "session_id": f.session, "parent_tool_use_id": nil, "message": map[string]any{"role": "assistant",
			"content": []any{map[string]any{"type": "text", "text": text}}}})
	}
	sub := t.Subtype
	if sub == "" {
		sub = "success"
	}
	result := t.Result
	if result == "" {
		result = text
	}
	f.result(sub, sub != "success", result, t.Usage)
}

func (f *fake) event(ev map[string]any) {
	f.send(map[string]any{"type": "stream_event", "session_id": f.session, "parent_tool_use_id": nil, "event": ev})
}

func (f *fake) result(subtype string, isError bool, text string, usage map[string]int64) {
	if usage == nil {
		usage = map[string]int64{}
	}
	f.send(map[string]any{"type": "result", "subtype": subtype, "is_error": isError, "num_turns": 1, "session_id": f.session,
		"result": text, "usage": usage, "total_cost_usd": 0})
}

// wait sleeps, answering control requests; it reports whether an interrupt arrived.
func (f *fake) wait(d time.Duration) bool {
	deadline := time.After(d)
	for {
		select {
		case <-deadline:
			return false
		case m, ok := <-f.inbox:
			if !ok {
				return true
			}
			if m["type"] == "control_request" {
				req, _ := m["request"].(map[string]any)
				f.control(m)
				if req["subtype"] == "interrupt" {
					return true
				}
			}
		}
	}
}

func serverStatus(names []string) []any {
	out := []any{}
	for _, n := range names {
		out = append(out, map[string]any{"name": n, "status": "connected"})
	}
	return out
}

func apiKeySource() string {
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return "ANTHROPIC_API_KEY"
	}
	return "none"
}
