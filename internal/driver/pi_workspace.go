package driver

import (
	"context"
	"path/filepath"
	"sync"
)

// Pi discovers .pi/mcp.json in the working directory. A channel can run several
// bots in that directory, so keep each process's grant configuration unchanged
// until it exits. Waiting does not hold the harness lock and is cancellable.
var piWorkspaces = struct {
	sync.Mutex
	entries map[string]*piWorkspace
}{entries: map[string]*piWorkspace{}}

type piWorkspace struct {
	permit chan struct{}
	refs   int
}

func lockPiWorkspace(ctx context.Context, dir string) (func(), error) {
	path, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	piWorkspaces.Lock()
	entry := piWorkspaces.entries[path]
	if entry == nil {
		entry = &piWorkspace{permit: make(chan struct{}, 1)}
		entry.permit <- struct{}{}
		piWorkspaces.entries[path] = entry
	}
	entry.refs++
	piWorkspaces.Unlock()
	drop := func() {
		piWorkspaces.Lock()
		defer piWorkspaces.Unlock()
		entry.refs--
		if entry.refs == 0 {
			delete(piWorkspaces.entries, path)
		}
	}
	select {
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	case <-entry.permit:
		return func() { entry.permit <- struct{}{}; drop() }, nil
	}
}
