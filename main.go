// Bqbot is a chat app whose contacts are AI agents running on the agent
// CLIs of this machine (claude, codex, pi), drawn with MyGo's native UI.
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"bqbot/internal/driver"
	"bqbot/internal/harness"
	"bqbot/internal/mcp"
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
	fixPath()
	dir, err := store.DefaultDir()
	if err != nil {
		log.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		log.Fatal(err)
	}
	h := harness.New(st, &driver.Claude{}, &driver.Codex{}, &driver.Pi{}, &driver.Echo{})
	// Without the endpoint the bots still chat, but cannot ask each other.
	if exe, err := os.Executable(); err != nil {
		log.Printf("bot handoff is off: %v", err)
	} else if err := h.Listen(exe); err != nil {
		log.Printf("bot handoff is off: %v", err)
	}
	defer h.Close()

	var win *mygo.Window
	a, err := newApp(st, h, func(fn func()) { win.Update(fn) })
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
		a.probeEngines()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
