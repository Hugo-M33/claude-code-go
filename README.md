# claude-code-go

Drive [Claude Code](https://code.claude.com) from Go. This package runs the `claude` program as a
subprocess and speaks the same stream-json protocol as Anthropic's Agent SDKs for Python and
TypeScript: prompts and control requests go in on standard input, messages come out on standard
output, and tools written in Go are served to Claude through an in-process MCP server.

**Unofficial.** This is not an Anthropic product. Anthropic publishes the Agent SDK for Python
and TypeScript; this is an independent Go port of the protocol those SDKs use, written by reading
them. It may break when Claude Code changes its protocol.

No dependencies beyond the standard library. Go 1.25 or later, and Claude Code installed
(`claude` on your PATH).

## Example

```go
clock := claudecode.NewMCPServer("clock", "1.0.0", claudecode.Tool{
	Name:        "now",
	Description: "The current time in an IANA time zone.",
	InputSchema: json.RawMessage(`{"type":"object","required":["zone"],"properties":{"zone":{"type":"string"}}}`),
	Handler: func(ctx context.Context, call claudecode.ToolCall) (claudecode.ToolResult, error) {
		var in struct{ Zone string }
		json.Unmarshal(call.Arguments, &in)
		loc, err := time.LoadLocation(in.Zone)
		if err != nil {
			return claudecode.ErrorResult("unknown zone " + in.Zone), nil
		}
		return claudecode.TextResult(time.Now().In(loc).Format(time.RFC1123)), nil
	},
})

opts := claudecode.Options{
	SystemPrompt:   claudecode.String("Answer briefly, using your tools."),
	Tools:          claudecode.Strings(), // no built-in tools: only ours
	SettingSources: claudecode.Strings(), // no user or project settings, no CLAUDE.md
	SDKServers:     []*claudecode.MCPServer{clock},
	AllowedTools:   []string{claudecode.ToolName("clock", "now")},
}
for m, err := range claudecode.Query(ctx, "What time is it in Tokyo?", opts) {
	if err != nil {
		log.Fatal(err)
	}
	if r, ok := m.(*claudecode.ResultMessage); ok {
		fmt.Println(r.Result)
	}
}
```

`examples/tools` streams the answer as it is written; `examples/chat` keeps a session open across
prompts and interrupts a reply on Ctrl+C.

## What it covers

| | |
|---|---|
| `Query` | One prompt in a new process; yields its messages up to the turn's result |
| `Connect`, `Send`, `Turn`, `Receive`, `Messages` | A long-lived session: send prompts, read each turn |
| `Interrupt`, `SetModel`, `SetPermissionMode`, `Control` | Control requests |
| `MCPServer`, `Tool` | Go functions as MCP tools, served from this process (`mcp__<server>__<tool>`); a cancelled call cancels the handler's context |
| `Options.CanUseTool` | Decide tool permissions in Go instead of a terminal prompt |
| Messages | `SystemMessage`, `AssistantMessage`, `UserMessage`, `StreamEvent` (with `Options.IncludePartialMessages`), `ResultMessage`, `RateLimitEvent`; unknown types arrive as `OtherMessage` with their raw JSON |
| Options | System prompt, built-in tools, allowed and disallowed tools, model, effort, thinking display, turns and budget limits, sessions (`Resume`, `Continue`, `SessionID`, `ForkSession`), settings sources, external MCP servers, extra flags |

Not covered yet: hook callbacks (the CLI's requests for them are answered with an error), session
stores and transcript mirroring, sub-agent definitions.

## Authentication and Anthropic's terms

The package uses whatever `claude` is signed in with and never reads, stores or passes on
credentials. With `Options.NoAPIKey` it removes `ANTHROPIC_API_KEY` and `ANTHROPIC_AUTH_TOKEN`
from the process's environment, so a Claude subscription login (`claude`, then `/login`) is used.

Anthropic's [terms for Claude Code](https://code.claude.com/docs/en/legal-and-compliance) allow a
subscription for a person's own, ordinary use of Claude Code. A product or service built on this
package and offered to other people must use API keys (or a cloud provider), and must not route
other people's requests through your subscription. Check those terms for your use.

## Tests

`go test ./...` runs against a fake `claude` (the test binary plays the CLI's side of the
protocol, as recorded from Claude Code 2.1.330), so it needs no account. `CLAUDE_CODE_GO_LIVE=1
go test -run TestLive ./` runs one prompt with a tool against the real `claude`.

## Licence

MIT (see `LICENSE`).
