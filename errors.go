package claudecode

import (
	"errors"
	"fmt"
	"strings"
)

// ErrClosed is returned once the client is closed or its process has ended.
var ErrClosed = errors.New("claudecode: the claude process has ended")

// CLINotFoundError says `claude` could not be found.
type CLINotFoundError struct{ Searched []string }

func (e *CLINotFoundError) Error() string {
	return "claudecode: the claude program was not found (install Claude Code, or set Options.CLIPath); looked in " + strings.Join(e.Searched, ", ")
}

// ProcessError says the `claude` process ended with an error.
type ProcessError struct {
	ExitCode int
	// Stderr is the end of what the process wrote on standard error.
	Stderr string
}

func (e *ProcessError) Error() string {
	s := fmt.Sprintf("claudecode: claude exited with status %d", e.ExitCode)
	if e.Stderr != "" {
		s += ": " + strings.TrimSpace(e.Stderr)
	}
	return s
}

// ControlError is a control request the CLI refused.
type ControlError struct {
	Subtype string
	Message string
}

func (e *ControlError) Error() string {
	return fmt.Sprintf("claudecode: %s refused: %s", e.Subtype, e.Message)
}
