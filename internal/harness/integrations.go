package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"bqbot/internal/composio"
	"bqbot/internal/driver"
	"bqbot/internal/mcpgateway"
	"bqbot/internal/store"
)

type externalTool struct {
	tool   mcpgateway.Tool
	client *mcpgateway.Client
	invoke func(context.Context, json.RawMessage) (json.RawMessage, error)
}
type externalGroup struct {
	clients []*mcpgateway.Client
	tools   map[string]externalTool
}

func (g *externalGroup) close() {
	if g != nil {
		for _, c := range g.clients {
			c.Close()
		}
	}
}

// prepareExternal performs network/process work without holding the harness mutex.
func (h *Harness) prepareExternal(t *turn, in *driver.TurnInput) (func(), error) {
	if t.consult {
		return nil, nil
	}
	configs, err := h.Store.MCPServers()
	if err != nil {
		return nil, err
	}
	group := &externalGroup{tools: map[string]externalTool{}}
	cleanup := func() { group.close() }
	add := func(id string, config mcpgateway.Config) error {
		if in.MCP == nil {
			return errors.New("this engine cannot use configured MCP tools; enable the bqbot tool endpoint and choose an MCP-capable engine")
		}
		client, err := mcpgateway.Open(t.ctx, config)
		if err != nil {
			return fmt.Errorf("MCP %s: %w", id, err)
		}
		group.clients = append(group.clients, client)
		ctx, cancel := context.WithTimeout(t.ctx, 20*time.Second)
		defer cancel()
		tools, err := client.Tools(ctx)
		if err != nil {
			return fmt.Errorf("MCP %s tool discovery: %w", id, err)
		}
		for i, tool := range tools {
			// Provider-safe names do not depend on an external server's naming rules.
			name := fmt.Sprintf("external_%s_%d", id, i)
			if len(name) > 64 {
				return errors.New("MCP server ID is too long for tool names")
			}
			group.tools[name] = externalTool{tool: tool, client: client}
		}
		return nil
	}
	for _, v := range configs {
		if !v.Enabled || len(v.BotIDs) > 0 && !slices.Contains(v.BotIDs, t.bot.ID) {
			continue
		}
		if err = v.Validate(); err != nil {
			cleanup()
			return nil, err
		}
		config := mcpgateway.Config{Transport: v.Transport, Command: v.Command, Args: v.Args, URL: v.URL, Env: os.Environ(), Headers: map[string]string{}}
		for k, name := range v.Env {
			value, ok := os.LookupEnv(name)
			if !ok {
				cleanup()
				return nil, fmt.Errorf("MCP %s environment variable %s is missing", v.Name, name)
			}
			config.Env = append(config.Env, k+"="+value)
		}
		for k, name := range v.Headers {
			value, ok := os.LookupEnv(name)
			if !ok {
				cleanup()
				return nil, fmt.Errorf("MCP %s header environment variable %s is missing", v.Name, name)
			}
			if strings.ContainsAny(value, "\r\n") {
				cleanup()
				return nil, errors.New("invalid MCP header value")
			}
			config.Headers[k] = value
		}
		if err = add(v.ID, config); err != nil {
			cleanup()
			return nil, err
		}
	}
	settings, err := h.Store.ComposioSettings()
	if err != nil {
		cleanup()
		return nil, err
	}
	if settings.Enabled && (len(settings.BotIDs) == 0 || slices.Contains(settings.BotIDs, t.bot.ID)) {
		headers, e := composio.New().Headers()
		if e != nil {
			cleanup()
			return nil, e
		}
		if err = add("composio", mcpgateway.Config{Transport: "http", URL: settings.MCPURL, Headers: headers}); err != nil {
			cleanup()
			return nil, err
		}
	}
	if err = h.prepareComputer(t, group); err != nil {
		cleanup()
		return nil, err
	}
	if len(group.tools) == 0 {
		return cleanup, nil
	}
	if in.MCP == nil {
		cleanup()
		return nil, errors.New("this engine cannot use configured MCP tools; start the bqbot tool endpoint and choose an MCP-capable engine")
	}
	h.mu.Lock()
	t.external = group
	h.mu.Unlock()
	for name := range group.tools {
		in.MCP.Tools = append(in.MCP.Tools, name)
	}
	slices.Sort(in.MCP.Tools)
	return cleanup, nil
}
func (hd *handoff) serveExternalList(w http.ResponseWriter, r *http.Request) {
	from := hd.caller(r, false)
	if from == nil {
		reply(w, http.StatusUnauthorized, "", errors.New("unauthorized"))
		return
	}
	hd.h.mu.Lock()
	group := from.external
	hd.h.mu.Unlock()
	tools := []mcpgateway.Tool{}
	if group != nil {
		for name, v := range group.tools {
			tool := v.tool
			tool.Name = name
			tools = append(tools, tool)
		}
	}
	slices.SortFunc(tools, func(a, b mcpgateway.Tool) int { return strings.Compare(a.Name, b.Name) })
	data, _ := json.Marshal(tools)
	reply(w, http.StatusOK, string(data), nil)
}
func (hd *handoff) serveExternalCall(w http.ResponseWriter, r *http.Request) {
	from := hd.caller(r, false)
	if from == nil {
		reply(w, http.StatusUnauthorized, "", errors.New("unauthorized"))
		return
	}
	var req struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		reply(w, 400, "", errors.New("invalid MCP call"))
		return
	}
	hd.h.mu.Lock()
	group := from.external
	hd.h.mu.Unlock()
	if group == nil {
		reply(w, 200, "", errors.New("external MCP tool is unavailable"))
		return
	}
	tool, ok := group.tools[req.Name]
	if !ok {
		reply(w, 200, "", errors.New("unknown external MCP tool"))
		return
	}
	if from.consult {
		reply(w, 200, "", errors.New("external tools are disabled during consult"))
		return
	}
	var args map[string]any
	if json.Unmarshal(req.Arguments, &args) != nil || args == nil {
		reply(w, 400, "", errors.New("tool arguments must be an object"))
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(from.ctx, cancel)
	defer stop()
	decision, err := hd.h.approval(ctx, from, driver.ApprovalRequest{ID: store.NewID(), Kind: "tool", Tool: "MCP: " + tool.tool.Name, Input: req.Arguments})
	if err != nil || decision.Outcome != "allow" {
		reply(w, 200, "", errors.New("MCP tool permission denied"))
		return
	}
	hd.h.note(from, "MCP: "+tool.tool.Name)
	var result json.RawMessage
	if tool.invoke != nil {
		result, err = tool.invoke(ctx, req.Arguments)
	} else {
		result, err = tool.client.Call(ctx, tool.tool.Name, req.Arguments)
	}
	reply(w, 200, string(result), err)
}
