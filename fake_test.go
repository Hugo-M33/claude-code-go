package claudecode

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// The tests run this test binary as a fake `claude`: with CLAUDE_CODE_GO_FAKE set it speaks the
// CLI's side of the protocol, as the real one did in a recorded session (claude 2.1.330), and
// plays the scenario the variable names.

func TestMain(m *testing.M) {
	if scenario := os.Getenv("CLAUDE_CODE_GO_FAKE"); scenario != "" {
		os.Exit(fakeCLI(scenario))
	}
	os.Exit(m.Run())
}

type fake struct {
	wmu     sync.Mutex
	mu      sync.Mutex
	waiting map[string]chan json.RawMessage
	n       int
	servers []string
	lines   chan map[string]any
}

func (f *fake) send(v any) {
	b, _ := json.Marshal(v)
	f.wmu.Lock()
	defer f.wmu.Unlock()
	os.Stdout.Write(append(b, '\n'))
}

// ask sends a control request to the host and waits for its response.
func (f *fake) ask(req map[string]any) json.RawMessage {
	f.mu.Lock()
	f.n++
	id := fmt.Sprintf("cli_%d", f.n)
	ch := make(chan json.RawMessage, 1)
	f.waiting[id] = ch
	f.mu.Unlock()
	f.send(map[string]any{"type": "control_request", "request_id": id, "request": req})
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		fmt.Fprintln(os.Stderr, "fake: no answer to", req["subtype"])
		os.Exit(9)
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
	var out struct {
		Response struct {
			MCPResponse map[string]any `json:"mcp_response"`
		} `json:"response"`
		Error string `json:"error"`
	}
	_ = json.Unmarshal(r, &out)
	if out.Error != "" {
		return map[string]any{"error": out.Error}
	}
	return out.Response.MCPResponse
}

// slow streams words until the host interrupts the turn (answering its other control requests
// meanwhile).
func (f *fake) slow() {
	for i := 0; i < 100; i++ {
		f.send(map[string]any{"type": "stream_event", "session_id": "s1", "event": map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "word "}}})
		time.Sleep(20 * time.Millisecond)
		select {
		case l, ok := <-f.lines:
			if !ok || l["type"] != "control_request" {
				continue
			}
			req, _ := l["request"].(map[string]any)
			f.send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": l["request_id"], "response": map[string]any{}}})
			if req["subtype"] == "interrupt" {
				f.send(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "num_turns": 1, "session_id": "s1", "result": "interrupted"})
				return
			}
		default:
		}
	}
	f.send(map[string]any{"type": "result", "subtype": "success", "num_turns": 1, "session_id": "s1", "result": "not interrupted"})
}

func fakeCLI(scenario string) int {
	if p := os.Getenv("CLAUDE_CODE_GO_FAKE_ARGS"); p != "" {
		b, _ := json.Marshal(map[string]any{"args": os.Args[1:], "apiKey": os.Getenv("ANTHROPIC_API_KEY"), "entrypoint": os.Getenv("CLAUDE_CODE_ENTRYPOINT")})
		_ = os.WriteFile(p, b, 0o644)
	}
	f := &fake{waiting: map[string]chan json.RawMessage{}, lines: make(chan map[string]any, 16)}
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
	// Reader: answers to our requests go to their waiters, the rest to the scenario.
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
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
					ch <- json.RawMessage(mustJSON(map[string]any{"response": r["response"], "error": r["error"]}))
				}
				continue
			}
			f.lines <- m
		}
		close(f.lines)
	}()
	apiKeySource := "none"
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		apiKeySource = "ANTHROPIC_API_KEY"
	}
	var tools []string
	for line := range f.lines {
		switch line["type"] {
		case "control_request":
			req, _ := line["request"].(map[string]any)
			id := line["request_id"]
			switch req["subtype"] {
			case "initialize":
				for _, s := range f.servers {
					f.mcp(s, "initialize", 1, map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}})
					f.mcp(s, "notifications/initialized", 0, nil)
					list := f.mcp(s, "tools/list", 2, nil)
					res, _ := list["result"].(map[string]any)
					ts, _ := res["tools"].([]any)
					for _, t := range ts {
						tools = append(tools, ToolName(s, t.(map[string]any)["name"].(string)))
					}
				}
				f.send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": id, "response": map[string]any{"commands": []any{}}}})
				if scenario == "crash" {
					fmt.Fprintln(os.Stderr, "boom: the fake crashed")
					return 3
				}
			case "interrupt":
				f.send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": id, "response": map[string]any{}}})
				f.lines <- map[string]any{"type": "interrupted"}
			case "set_model":
				f.send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "error", "request_id": id, "error": "no such model"}})
			}
		case "interrupted":
			f.send(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "num_turns": 1, "session_id": "s1", "result": ""})
		case "user":
			f.send(map[string]any{"type": "system", "subtype": "init", "session_id": "s1", "model": "claude-opus-5-5", "tools": tools,
				"mcp_servers": []any{map[string]any{"name": "aboard", "status": "connected"}}, "apiKeySource": apiKeySource})
			f.send(map[string]any{"type": "stray"})
			os.Stdout.WriteString("this is not JSON\n")
			msg, _ := line["message"].(map[string]any)
			prompt, _ := msg["content"].(string)
			switch scenario {
			case "tools":
				f.send(map[string]any{"type": "stream_event", "session_id": "s1", "event": map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "Adding "}}})
				f.send(map[string]any{"type": "assistant", "session_id": "s1", "message": map[string]any{"role": "assistant", "content": []any{
					map[string]any{"type": "text", "text": "Adding it."},
					map[string]any{"type": "tool_use", "id": "toolu_1", "name": "mcp__aboard__add", "input": map[string]any{"label": prompt}}}}})
				r := f.mcp("aboard", "tools/call", 3, map[string]any{"name": "add", "arguments": map[string]any{"label": prompt}, "_meta": map[string]any{"claudecode/toolUseId": "toolu_1"}})
				res, _ := r["result"].(map[string]any)
				content, _ := res["content"].([]any)
				text := content[0].(map[string]any)["text"]
				f.send(map[string]any{"type": "user", "session_id": "s1", "message": map[string]any{"role": "user", "content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": []any{map[string]any{"type": "text", "text": text}}, "is_error": res["isError"]}}}})
				bad := f.mcp("aboard", "tools/call", 4, map[string]any{"name": "missing", "arguments": map[string]any{}})
				badRes, _ := bad["result"].(map[string]any)
				unknown := f.mcp("aboard", "resources/list", 5, nil)
				f.send(map[string]any{"type": "assistant", "session_id": "s1", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": fmt.Sprintf("Done. missing=%v unknown=%v", badRes["isError"], unknown["error"] != nil)}}}})
				f.send(map[string]any{"type": "result", "subtype": "success", "is_error": false, "num_turns": 2, "session_id": "s1", "total_cost_usd": 0.014, "result": fmt.Sprint(text), "usage": map[string]any{"input_tokens": 4}})
			case "permission":
				r := f.ask(map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "input": map[string]any{"command": "ls"}, "tool_use_id": "toolu_2"})
				var out struct {
					Response map[string]any `json:"response"`
				}
				_ = json.Unmarshal(r, &out)
				f.send(map[string]any{"type": "result", "subtype": "success", "num_turns": 1, "session_id": "s1", "result": fmt.Sprint(out.Response["behavior"], " ", out.Response["message"])})
			case "slow":
				f.slow()
			case "cancel":
				f.send(map[string]any{"type": "control_request", "request_id": "cli_slow", "request": map[string]any{"subtype": "mcp_message", "server_name": "aboard",
					"message": map[string]any{"jsonrpc": "2.0", "id": 9, "method": "tools/call", "params": map[string]any{"name": "slow", "arguments": map[string]any{}}}}})
				time.Sleep(100 * time.Millisecond)
				f.send(map[string]any{"type": "control_cancel_request", "request_id": "cli_slow"})
				time.Sleep(300 * time.Millisecond)
				f.send(map[string]any{"type": "result", "subtype": "success", "num_turns": 1, "session_id": "s1", "result": "cancelled"})
			default:
				f.send(map[string]any{"type": "result", "subtype": "success", "num_turns": 1, "session_id": "s1", "result": "echo: " + strings.ToUpper(prompt)})
			}
		}
	}
	return 0
}
