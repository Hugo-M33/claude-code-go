package claudecode

import (
	"encoding/json"
	"io"
)

// Options configure a Client. The zero value runs `claude` with its defaults.
type Options struct {
	// CLIPath is the `claude` program to run. Empty: found on PATH, then in the usual install
	// locations (FindCLI).
	CLIPath string
	// Cwd is the working directory of the process (where Claude Code looks for project
	// settings and files). Empty: the current directory.
	Cwd string
	// Env is added to the process's environment ("KEY=value").
	Env []string
	// NoAPIKey removes ANTHROPIC_API_KEY and ANTHROPIC_AUTH_TOKEN from the process's environment,
	// so the CLI uses the login the user made with `/login` (a Claude subscription).
	NoAPIKey bool
	// Entrypoint is the CLAUDE_CODE_ENTRYPOINT the process reports ("sdk-go-unofficial").
	Entrypoint string

	// Model and FallbackModel name the models; Effort is low, medium, high, xhigh or max.
	Model         string
	FallbackModel string
	Effort        string
	// Thinking is "adaptive" or "disabled"; ThinkingDisplay is "summarized", "omitted" or
	// "updates" (progress notes between tool calls).
	Thinking        string
	ThinkingDisplay string

	// SystemPrompt replaces Claude Code's system prompt when not nil (an empty string sends
	// none). AppendSystemPrompt adds to the prompt in use.
	SystemPrompt       *string
	AppendSystemPrompt string

	// Tools chooses Claude Code's built-in tools: nil keeps the defaults, an empty slice
	// removes them all (only MCP tools remain), names keep those.
	Tools *[]string
	// AllowedTools run without asking; DisallowedTools are removed.
	AllowedTools    []string
	DisallowedTools []string
	// PermissionMode is default, acceptEdits, plan, bypassPermissions… (see `claude --help`).
	PermissionMode string
	// CanUseTool, when set, decides each tool use the CLI would otherwise ask about.
	CanUseTool CanUseToolFunc

	// MCPServers are external MCP servers, by name, as Claude Code's --mcp-config takes them
	// (stdio, http or sse configurations).
	MCPServers map[string]json.RawMessage
	// SDKServers are MCP servers that run in this process (NewMCPServer); their tools are
	// named mcp__<server>__<tool>.
	SDKServers []*MCPServer
	// StrictMCPConfig ignores every MCP server but those given here.
	StrictMCPConfig bool

	// MaxTurns bounds the turns of one prompt; MaxBudgetUSD its estimated spend.
	MaxTurns     int
	MaxBudgetUSD float64

	// Resume continues a session by id; Continue the most recent one in Cwd. SessionID starts
	// a session with a given id. ForkSession resumes into a new session.
	Resume      string
	Continue    bool
	SessionID   string
	ForkSession bool
	// NoSessionPersistence keeps the session off disk (it cannot be resumed).
	NoSessionPersistence bool

	// SettingSources chooses which settings Claude Code loads: nil keeps its default, an empty
	// slice loads none (no user or project settings, no CLAUDE.md), or "user", "project",
	// "local".
	SettingSources *[]string

	// IncludePartialMessages delivers StreamEvent messages while a response streams.
	IncludePartialMessages bool

	// ExtraArgs are added to the command line as they are, after the others.
	ExtraArgs []string
	// Stderr receives the process's standard error (it is also kept for errors).
	Stderr io.Writer
}

// Strings returns a pointer to a slice, for Options.Tools and Options.SettingSources:
// Strings() is an empty list, which is not the same as nil.
func Strings(s ...string) *[]string {
	if s == nil {
		s = []string{}
	}
	return &s
}

// String returns a pointer to s, for Options.SystemPrompt.
func String(s string) *string { return &s }
