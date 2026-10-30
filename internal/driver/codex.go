package driver

import (
	"context"
	"encoding/json"
	"fmt"

	"bqbot/internal/mcp"
)

// Codex runs turns using the Codex app-server JSON-RPC protocol.
type Codex struct {
	Bin string // "" is "codex"
}

func (d *Codex) Name() string { return "codex" }

// SupportsSteering reports that active turns accept additional text.
func (*Codex) SupportsSteering() bool { return true }

func (d *Codex) Binary() string {
	if d.Bin != "" {
		return d.Bin
	}
	return "codex"
}

// Args configures the app-server. Approvals always go through the client;
// automatic approval modes are enforced by TurnInput.Approve, not CLI flags.
func (d *Codex) Args(in TurnInput) []string {
	args := []string{"app-server", "-c", `sandbox_mode="read-only"`, "-c", `approval_policy="untrusted"`, "-c", `mcp_servers={}`}
	// Replace inherited servers before adding this turn's scoped grant.
	if s := in.MCP; s != nil {
		key := "mcp_servers." + mcp.Server + "."
		args = append(args,
			"-c", key+"command="+tomlValue(s.Command),
			"-c", key+"args="+tomlValue(s.Args),
			"-c", key+"enabled_tools="+tomlValue(append(append([]string{}, mcp.Tools...), s.Tools...)),
			"-c", key+`default_tools_approval_mode="approve"`,
			"-c", fmt.Sprintf("%stool_timeout_sec=%d", key, int(mcp.ToolTimeout.Seconds())))
	}
	return args
}

// Prompt is retained for callers that display the effective user prompt. The
// app-server receives persona instructions separately from the user's input.
func (d *Codex) Prompt(in TurnInput) string { return in.Text }

func tomlValue(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (d *Codex) Run(ctx context.Context, in TurnInput, emit func(Event)) error {
	return d.runRPC(ctx, in, emit)
}
