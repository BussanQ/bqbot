package webgo

import (
	"bqbot/internal/store"
	"encoding/json"
	"testing"
)

func TestDesktopMCPSettings(t *testing.T) {
	d, st := testDesktop(t)
	config := store.MCPConfig{Name: "Local tools", Enabled: true, Transport: "stdio", Command: "/usr/bin/example", Args: []string{"--stdio"}, Env: map[string]string{"TOKEN": "TOOL_TOKEN"}}
	data, _ := json.Marshal(config)
	raw, err := d.Tools(ToolsAction{Kind: "save-mcp-server", Config: data})
	if err != nil {
		t.Fatal(err)
	}
	var saved store.MCPConfig
	if err = json.Unmarshal(raw, &saved); err != nil || saved.ID == "" {
		t.Fatal(string(raw), err)
	}
	items, err := st.MCPServers()
	if err != nil || len(items) != 1 || items[0].Env["TOKEN"] != "TOOL_TOKEN" {
		t.Fatal(items, err)
	}
	if _, err = d.Tools(ToolsAction{Kind: "delete-mcp-server", ID: saved.ID}); err != nil {
		t.Fatal(err)
	}
	items, err = st.MCPServers()
	if err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
	if _, err = d.Tools(ToolsAction{Kind: "request-arbitrary-url"}); err == nil {
		t.Fatal("arbitrary operation accepted")
	}
}
