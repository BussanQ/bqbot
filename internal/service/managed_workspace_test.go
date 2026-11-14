package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
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
	// Transfer through both authenticated HTTP hops, including an empty file.
	if err := exec.Command("docker", "exec", name, "mkdir", "-p", "/home/agent/workspace/项目").Run(); err != nil {
		t.Fatal(err)
	}
	entries, err := api.WorkspaceFiles(b.ID, ".")
	if err != nil || len(entries) != 1 || entries[0].Kind != "dir" || entries[0].Name != "项目" {
		t.Fatalf("root list: %v %v", entries, err)
	}
	ctx := context.Background()
	for _, sample := range []struct {
		name string
		data []byte
	}{
		{"图 & #1.bin", bytes.Repeat([]byte{0, 255, 1, 2, 3}, 250000)}, {"空.txt", nil},
	} {
		path := "项目/" + sample.name
		if err = api.UploadWorkspaceFile(ctx, b.ID, path, bytes.NewReader(sample.data), int64(len(sample.data))); err != nil {
			t.Fatal(err)
		}
		var conflict *HTTPError
		if err = api.UploadWorkspaceFile(ctx, b.ID, path, bytes.NewReader([]byte("x")), 1); !errors.As(err, &conflict) || conflict.StatusCode != 409 {
			t.Fatalf("expected conflict, got %v", err)
		}
		var dst bytes.Buffer
		if err = api.DownloadWorkspaceFile(ctx, b.ID, path, &dst); err != nil || !bytes.Equal(sample.data, dst.Bytes()) {
			t.Fatalf("round trip %s: %v", path, err)
		}
	}
	entries, err = api.WorkspaceFiles(b.ID, "项目")
	if err != nil || len(entries) != 2 {
		t.Fatal(entries, err)
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
