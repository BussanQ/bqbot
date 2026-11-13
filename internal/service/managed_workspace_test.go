package service

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"bqbot/internal/computer"
	"bqbot/internal/store"
	"github.com/egoist/mygo"
)

// Opt in on a local Docker host. All resources belong to this temporary store.
func TestManagedWorkspaceDockerLifecycle(t *testing.T) {
	if os.Getenv("BQBOT_TEST_MANAGED_DOCKER") != "1" {
		t.Skip("requires local Docker")
	}
	// go test runs with the package directory as cwd.
	res, _ := filepath.Abs("../../resources")
	prev, _ := mygo.App.Path(mygo.PathResources)
	mygo.App.SetPath(mygo.PathResources, res)
	t.Cleanup(func() { mygo.App.SetPath(mygo.PathResources, prev) })
	_, api, h := newTestService(t, nil)
	b := mustSave(t, api, "local-desktop")
	dir, _ := h.Store.ManagedWorkspaceDir(b.ID)
	sum := sha256.Sum256([]byte(dir))
	name := fmt.Sprintf("bqbot-ws-%x", sum[:12])
	t.Cleanup(func() {
		exec.Command("docker", "rm", "-f", name).Run()
		exec.Command("docker", "volume", "rm", name+"-profile", name+"-files").Run()
	})
	cfg := store.ComputerConfig{BotID: b.ID, Provider: "workspace", Managed: true, Enabled: true}
	if _, err := api.SaveComputer(cfg); err != nil {
		t.Fatal(err)
	}
	cid, err := exec.Command("docker", "inspect", "--format", "{{.Id}}", name).Output()
	if err != nil {
		t.Fatal(err)
	}
	shot, err := api.ComputerAction(b.ID, ComputerRequest{Action: computer.Action{Kind: "screenshot"}})
	if err != nil || shot.Image == nil {
		t.Fatalf("screenshot: %v", err)
	}
	// A full-size video can still show only Chromium. Check that the window
	// manager actually reserved rows for both the menu and the bottom dock.
	area, err := exec.Command("docker", "exec", name, "xprop", "-root", "_NET_WORKAREA").Output()
	if err != nil {
		t.Fatal(err)
	}
	var x, y, width, height int
	if _, err = fmt.Sscanf(string(area), "_NET_WORKAREA(CARDINAL) = %d, %d, %d, %d", &x, &y, &width, &height); err != nil ||
		x != 0 || y <= 0 || width != shot.Width || height <= 0 || y+height >= shot.Height {
		t.Fatalf("expected menu and dock within %dx%d framebuffer, got %s", shot.Width, shot.Height, area)
	}
	cfg.Enabled = false
	if _, err = api.SaveComputer(cfg); err != nil {
		t.Fatal(err)
	}
	running, err := exec.Command("docker", "inspect", "--format", "{{.State.Running}}", name).Output()
	if err != nil || string(running) != "false\n" {
		t.Fatalf("not stopped: %s %v", running, err)
	}
	cfg.Enabled = true
	if _, err = api.SaveComputer(cfg); err != nil {
		t.Fatal(err)
	}
	if err = api.StartWorkspace(b.ID); err != nil {
		t.Fatal(err)
	}
	after, err := exec.Command("docker", "inspect", "--format", "{{.Id}}", name).Output()
	if err != nil || string(after) != string(cid) {
		t.Fatal("did not reuse container")
	}
	tabs, err := api.ComputerAction(b.ID, ComputerRequest{Action: computer.Action{Kind: "browser_tabs"}})
	if err != nil || tabs.Text == "" {
		t.Fatalf("browser tabs: %v", err)
	}
}
