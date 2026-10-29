// Bqbot is a chat app whose contacts are AI agents running on the agent
// CLIs of this machine (claude, codex, pi), drawn with MyGo's native UI.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"bqbot/internal/driver"
	"bqbot/internal/harness"
	"bqbot/internal/mcp"
	"bqbot/internal/service"
	"bqbot/internal/store"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		// The engines start this for a bot's turn: the bqbot MCP tools.
		if err := mcp.Main(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "bqbot mcp:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: bqbot [serve]")
		os.Exit(2)
	}
	fixPath()
	dir, err := store.DefaultDir()
	if err != nil {
		log.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		log.Fatal(err)
	}
	h := harness.New(st, &driver.Claude{}, &driver.Codex{}, &driver.Pi{}, &driver.Grok{}, &driver.OpenAI{}, &driver.Echo{})
	// Without the endpoint the bots still chat, but cannot ask each other.
	if exe, err := os.Executable(); err != nil {
		log.Printf("bot handoff is off: %v", err)
	} else if err := h.Listen(exe); err != nil {
		log.Printf("bot handoff is off: %v", err)
	}
	defer h.Close()
	server, err := service.Listen(h, "127.0.0.1:8799")
	var api *service.Client
	if err != nil {
		if len(os.Args) > 1 {
			log.Fatal(err)
		}
		// A desktop window can attach to an already running local service.
		api, err = existingService(filepath.Join(dir, "service.json"))
		if err != nil {
			log.Fatal(err)
		}
	} else {
		defer server.Close()
		if err := publishDiscovery(server, filepath.Join(dir, "service.json")); err != nil {
			log.Fatal(err)
		}
		api = service.NewClient(server.URL, server.Token)
	}
	if len(os.Args) > 1 {
		log.Printf("bqbot service listening at %s (credentials in %s)", server.URL, filepath.Join(dir, "service.json"))
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		<-ctx.Done()
		return
	}

	var win *mygo.Window
	a, err := newApp(api, func(fn func()) { win.Update(fn) })
	if err != nil {
		log.Fatal(err)
	}
	mygo.App.WhenReady(func() {
		win = mygo.NewWindow(mygo.WindowOptions{
			Title:     "bqbot",
			Width:     1080,
			Height:    720,
			MinWidth:  640,
			MinHeight: 420,
			StateKey:  "main",
			// The sidebar reaches up under the traffic lights, as in apps
			// with a sidebar on macOS 26.
			TitleBarStyle:        mygo.TitleBarHidden,
			TrafficLightPosition: &mygo.Point{X: 20, Y: 20},
			Vibrancy:             mygo.VibrancySidebar,
			BackgroundColor:      "light-dark(#ffffff, #141416)",
			Content:              ui.View(a.view),
		})
		a.connect()
		win.OnClosed(func() { a.cancel() })
		a.probeEngines()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

func publishDiscovery(server *service.Server, path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return server.WriteDiscovery(path)
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 4096 {
		return fmt.Errorf("cannot replace invalid service discovery file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var old service.Discovery
	if json.Unmarshal(data, &old) != nil || old.URL != server.URL || len(old.Token) != 64 {
		return fmt.Errorf("existing discovery file does not belong to this service address")
	}
	// Owning this listener proves the previous process at the same address has
	// exited. Reclaim only its private, recognizable discovery file.
	if err := os.Remove(path); err != nil {
		return err
	}
	return server.WriteDiscovery(path)
}

func existingService(path string) (*service.Client, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("cannot connect to local service: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("service credentials must be a private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var discovery struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &discovery); err != nil {
		return nil, err
	}
	if discovery.URL != "http://127.0.0.1:8799" || discovery.Token == "" {
		return nil, fmt.Errorf("invalid local service credentials")
	}
	api := service.NewClient(discovery.URL, discovery.Token)
	if _, err := api.Bots(); err != nil {
		return nil, err
	}
	return api, nil
}
