package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Auth is how the `claude` program is signed in, as `claude auth status` reports it. It costs
// no request to the model. Only these fields are read; the account's identity is left out.
type Auth struct {
	LoggedIn bool `json:"loggedIn"`
	// AuthMethod is "claude.ai" for a subscription login, or the API key's source.
	AuthMethod string `json:"authMethod"`
	// APIProvider is "firstParty" (Anthropic), or a cloud provider.
	APIProvider string `json:"apiProvider"`
}

// AuthStatus asks the `claude` program how it is signed in, in the environment opts gives it
// (Options.NoAPIKey and Options.Env apply; Options.CLIPath picks the program).
func AuthStatus(ctx context.Context, opts Options) (Auth, error) {
	path := opts.CLIPath
	if path == "" {
		p, err := FindCLI()
		if err != nil {
			return Auth{}, err
		}
		path = p
	}
	cmd := exec.CommandContext(ctx, path, "auth", "status")
	cmd.Env = opts.env()
	cmd.Dir = opts.Cwd
	out, err := cmd.Output()
	var a Auth
	if jerr := json.Unmarshal(out, &a); jerr != nil {
		if err != nil {
			msg := ""
			if ee, ok := err.(*exec.ExitError); ok {
				msg = strings.TrimSpace(string(ee.Stderr))
			}
			return Auth{}, fmt.Errorf("claudecode: claude auth status: %w %s", err, msg)
		}
		return Auth{}, fmt.Errorf("claudecode: claude auth status: unexpected output: %w", jerr)
	}
	// A signed-out CLI may exit non-zero and still print its status.
	return a, nil
}
