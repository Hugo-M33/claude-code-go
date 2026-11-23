package claudecode

import (
	"context"
	"encoding/json"
)

// CanUseToolFunc decides whether Claude may use a tool the CLI would otherwise ask about. It
// runs while the CLI waits; ctx is cancelled if the CLI gives up the request.
type CanUseToolFunc func(ctx context.Context, req PermissionRequest) (PermissionResult, error)

// PermissionRequest is a tool use waiting for a decision.
type PermissionRequest struct {
	ToolName  string          `json:"tool_name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	// Suggestions are the permission updates the CLI proposes, as it sent them.
	Suggestions json.RawMessage `json:"permission_suggestions"`
	// BlockedPath and DecisionReason explain why the CLI asks, when it says.
	BlockedPath    string          `json:"blocked_path"`
	DecisionReason string          `json:"decision_reason"`
	Raw            json.RawMessage `json:"-"`
}

// PermissionResult is the decision: allow (optionally with a changed input), or deny with a
// message the model reads (and optionally stop the turn).
type PermissionResult struct {
	Allow        bool
	UpdatedInput json.RawMessage
	Message      string
	Interrupt    bool
}

// Allow allows the tool use as asked.
func Allow() PermissionResult { return PermissionResult{Allow: true} }

// Deny refuses it, telling the model why.
func Deny(message string) PermissionResult { return PermissionResult{Message: message} }

func (r PermissionResult) wire(original json.RawMessage) map[string]any {
	if r.Allow {
		in := r.UpdatedInput
		if len(in) == 0 {
			in = original
		}
		return map[string]any{"behavior": "allow", "updatedInput": in}
	}
	out := map[string]any{"behavior": "deny", "message": r.Message}
	if r.Interrupt {
		out["interrupt"] = true
	}
	return out
}
