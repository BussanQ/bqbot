package computer

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"bqbot/internal/store"
)

// This opt-in check reads an actual Workspace Bridge. It also catches an
// otherwise healthy X server with a locked Chromium profile or inactive AT-SPI.
func TestLiveWorkspaceProtocols(t *testing.T) {
	endpoint := os.Getenv("BQBOT_TEST_WORKSPACE_URL")
	if endpoint == "" {
		t.Skip("set BQBOT_TEST_WORKSPACE_URL to an isolated workspace")
	}
	cfg := store.ComputerConfig{BotID: "probe", Provider: "workspace", Enabled: true, URL: endpoint, TokenEnv: "BQBOT_WORKSPACE_TOKEN"}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	call := func(kind string) Result {
		t.Helper()
		r, err := Execute(ctx, cfg, Action{Kind: kind})
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		return r
	}
	frame := call("screenshot")
	if frame.Image == nil || frame.Width == 0 || frame.Height == 0 {
		t.Fatal("empty RFB frame")
	}
	var tabs []struct{ ID string }
	if json.Unmarshal([]byte(call("browser_tabs").Text), &tabs) != nil || len(tabs) == 0 || tabs[0].ID == "" {
		t.Fatal("no Chromium CDP targets")
	}
	var nodes []struct{ Role string }
	if err := json.Unmarshal([]byte(call("gui_tree").Text), &nodes); err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n.Role == "application" {
			return
		}
	}
	t.Fatal("AT-SPI tree contains no applications")
}
