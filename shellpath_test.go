package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestMergePath(t *testing.T) {
	got := mergePath("/a:/b", "", "/b::/c", "/a:/d")
	if want := "/a:/b:/c:/d"; got != want {
		t.Fatalf("mergePath = %q, want %q", got, want)
	}
}

func TestShellPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX shell")
	}
	// A fake shell whose startup files print noise around the PATH.
	sh := filepath.Join(t.TempDir(), "sh")
	script := "#!/bin/sh\necho 'welcome'\nPATH=/x/bin:/y/bin /bin/sh -c \"$2\"\necho bye\n"
	if err := os.WriteFile(sh, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := shellPath(sh, 5*time.Second), "/x/bin:/y/bin"; got != want {
		t.Fatalf("shellPath = %q, want %q", got, want)
	}
	if got := shellPath(filepath.Join(t.TempDir(), "missing"), time.Second); got != "" {
		t.Fatalf("shellPath of a missing shell = %q, want empty", got)
	}
}
