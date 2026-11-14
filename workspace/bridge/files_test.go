package main

import (
	"errors"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceFileBoundaries(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	outside := filepath.Join(t.TempDir(), "outside")
	if err = os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = root.Symlink(filepath.Dir(outside), "link"); err != nil {
		t.Fatal(err)
	}
	handler := workspaceFiles(root)
	for _, p := range []string{"../outside", "link/outside", "/etc/passwd"} {
		for _, method := range []string{"GET", "PUT"} {
			rec := httptest.NewRecorder()
			handler(rec, httptest.NewRequest(method, "/files/content?path="+url.QueryEscape(p), strings.NewReader("changed")))
			if rec.Code < 400 {
				t.Fatalf("%s %s: %d", method, p, rec.Code)
			}
		}
	}
	b, err := os.ReadFile(outside)
	if err != nil || string(b) != "private" {
		t.Fatal("changed external file", err)
	}
	if err = receiveFile(root, "incomplete", io.MultiReader(strings.NewReader("part"), brokenReader{})); err == nil {
		t.Fatal("incomplete file accepted")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("partial upload left files: %v, %v", files, err)
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("transfer interrupted") }
