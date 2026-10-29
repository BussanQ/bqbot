package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// pathMarker brackets the PATH the shell prints, apart from whatever its
// startup files print.
const pathMarker = "__bqbot_path__"

// fixPath gives an app started from Finder or the Dock, whose PATH is only
// the system's, the PATH of the user's shell, where the engine CLIs and
// node live, as a terminal would.
func fixPath() {
	if runtime.GOOS == "windows" {
		return
	}
	home, _ := os.UserHomeDir()
	os.Setenv("PATH", mergePath(shellPath(os.Getenv("SHELL"), 5*time.Second), os.Getenv("PATH"), fallbackDirs(home)))
}

// shellPath returns the PATH of shell as an interactive login shell, where
// rc files such as .zshrc set it up, or "" when that fails in time.
func shellPath(shell string, timeout time.Duration) string {
	if shell == "" {
		shell = "/bin/zsh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-ilc", `printf '`+pathMarker+`%s`+pathMarker+`' "$PATH"`)
	cmd.WaitDelay = time.Second
	out, _ := cmd.Output()
	s := string(out)
	i := strings.Index(s, pathMarker)
	if i < 0 {
		return ""
	}
	s = s[i+len(pathMarker):]
	j := strings.Index(s, pathMarker)
	if j < 0 {
		return ""
	}
	return s[:j]
}

// fallbackDirs are where agent CLIs are usually installed, for when the
// shell gives nothing.
func fallbackDirs(home string) string {
	dirs := []string{"/opt/homebrew/bin", "/usr/local/bin"}
	if home != "" {
		dirs = append([]string{filepath.Join(home, ".local", "bin"), filepath.Join(home, "bin")}, dirs...)
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}

// mergePath joins PATH lists in order, without empty or repeated entries.
func mergePath(lists ...string) string {
	seen := map[string]bool{}
	var dirs []string
	for _, l := range lists {
		for _, d := range filepath.SplitList(l) {
			if d != "" && !seen[d] {
				seen[d] = true
				dirs = append(dirs, d)
			}
		}
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}
