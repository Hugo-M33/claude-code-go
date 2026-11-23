package claudecode

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// FindCLI returns the path of the `claude` program: on PATH, then in the places its installers
// use.
func FindCLI() (string, error) {
	if p, err := exec.LookPath("claude"); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".local", "bin", "claude"),
		filepath.Join(home, ".claude", "local", "claude"),
		filepath.Join(home, ".npm-global", "bin", "claude"),
		"/usr/local/bin/claude",
		filepath.Join(home, "node_modules", ".bin", "claude"),
		filepath.Join(home, ".yarn", "bin", "claude"),
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c, nil
		}
	}
	return "", &CLINotFoundError{Searched: append([]string{"PATH"}, candidates...)}
}

// args is the command line for the options: always stream-json both ways, as the official SDKs
// run the CLI, so prompts and control requests go over standard input.
func (o *Options) args() ([]string, error) {
	a := []string{"--output-format", "stream-json", "--verbose", "--input-format", "stream-json"}
	add := func(flag, v string) {
		if v != "" {
			a = append(a, flag, v)
		}
	}
	if o.SystemPrompt != nil {
		a = append(a, "--system-prompt", *o.SystemPrompt)
	}
	add("--append-system-prompt", o.AppendSystemPrompt)
	if o.Tools != nil {
		a = append(a, "--tools", strings.Join(*o.Tools, ","))
	}
	if len(o.AllowedTools) > 0 {
		a = append(a, "--allowedTools", strings.Join(o.AllowedTools, ","))
	}
	if len(o.DisallowedTools) > 0 {
		a = append(a, "--disallowedTools", strings.Join(o.DisallowedTools, ","))
	}
	if o.MaxTurns > 0 {
		a = append(a, "--max-turns", strconv.Itoa(o.MaxTurns))
	}
	if o.MaxBudgetUSD > 0 {
		a = append(a, "--max-budget-usd", strconv.FormatFloat(o.MaxBudgetUSD, 'f', -1, 64))
	}
	add("--model", o.Model)
	add("--fallback-model", o.FallbackModel)
	add("--effort", o.Effort)
	add("--thinking", o.Thinking)
	add("--thinking-display", o.ThinkingDisplay)
	add("--permission-mode", o.PermissionMode)
	if o.CanUseTool != nil {
		// The CLI asks this process, over the control protocol, instead of a terminal.
		a = append(a, "--permission-prompt-tool", "stdio")
	}
	if o.Continue {
		a = append(a, "--continue")
	}
	add("--resume", o.Resume)
	add("--session-id", o.SessionID)
	if o.ForkSession {
		a = append(a, "--fork-session")
	}
	if o.NoSessionPersistence {
		a = append(a, "--no-session-persistence")
	}
	if o.SettingSources != nil {
		a = append(a, "--setting-sources", strings.Join(*o.SettingSources, ","))
	}
	servers := map[string]any{}
	for name, cfg := range o.MCPServers {
		servers[name] = cfg
	}
	for _, s := range o.SDKServers {
		if _, dup := servers[s.Name]; dup {
			return nil, fmt.Errorf("claudecode: two MCP servers are named %q", s.Name)
		}
		servers[s.Name] = map[string]string{"type": "sdk", "name": s.Name}
	}
	if len(servers) > 0 {
		b, err := json.Marshal(map[string]any{"mcpServers": servers})
		if err != nil {
			return nil, err
		}
		a = append(a, "--mcp-config", string(b))
	}
	if o.StrictMCPConfig {
		a = append(a, "--strict-mcp-config")
	}
	if o.IncludePartialMessages {
		a = append(a, "--include-partial-messages")
	}
	return append(a, o.ExtraArgs...), nil
}

// env is the process's environment.
func (o *Options) env() []string {
	var env []string
	for _, e := range os.Environ() {
		if o.NoAPIKey && (strings.HasPrefix(e, "ANTHROPIC_API_KEY=") || strings.HasPrefix(e, "ANTHROPIC_AUTH_TOKEN=")) {
			continue
		}
		if strings.HasPrefix(e, "CLAUDE_CODE_ENTRYPOINT=") {
			continue
		}
		env = append(env, e)
	}
	entry := o.Entrypoint
	if entry == "" {
		entry = "sdk-go-unofficial"
	}
	env = append(env, "CLAUDE_CODE_ENTRYPOINT="+entry)
	return append(env, o.Env...)
}
