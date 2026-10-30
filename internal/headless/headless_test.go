package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bqbot/internal/driver"
	"bqbot/internal/harness"
	"bqbot/internal/service"
	"bqbot/internal/store"
)

func TestExecuteRunJSONExportAndNoOverwrite(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := harness.New(st, &driver.Echo{})
	defer h.Close()
	s, err := service.Listen(h, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	api := service.NewClient(s.URL, s.Token)
	if err := api.SaveBot(store.Bot{ID: "echo", Name: "Echo", Engine: "echo", ApprovalMode: "ask"}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "result.json")
	var out, stderr bytes.Buffer
	code := ExecuteWithClient(context.Background(), []string{"run", "--bot", "echo", "--json", "--export", file, "--prompt", "hello"}, strings.NewReader(""), &out, &stderr, api)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var result service.RunResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || !strings.Contains(result.Text, "hello") {
		t.Fatalf("bad result %#v", result)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("export mode: %v %v", info, err)
	}
	data, _ := os.ReadFile(file)
	if !json.Valid(data) {
		t.Fatal("invalid export JSON")
	}
	if err := writeNew(file, []byte("overwrite")); err == nil {
		t.Fatal("overwrote existing export")
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(data, after) {
		t.Fatal("existing export changed")
	}
}
func TestReadInputLimits(t *testing.T) {
	if _, err := readInput("-", strings.NewReader("12345"), 4); err == nil {
		t.Fatal("oversized input accepted")
	}
}
