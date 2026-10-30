// Package webgo connects the bundled desktop frontend to the local service.
package webgo

import (
	"log"
	"runtime"

	"bqbot/internal/service"
	"bqbot/resources"
	"bqbot/web"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
)

var desktopChanged = mygo.NewEvent[bool]("desktop:changed")

// Run opens the desktop window and releases its subscriptions and audio on exit.
// Lifecycle methods stay private so MyGo does not expose them through the bridge.
func Run(api *service.Client) error {
	desktop, err := newDesktop(api)
	if err != nil {
		return err
	}
	defer desktop.close()
	mygo.Use(updater.New(updater.Options{DisableAutomaticChecks: true}))
	mygo.SetFrontend(web.Assets)
	mygo.Bind(desktop)
	mygo.App.WhenReady(func() {
		// Bare development binaries have no app bundle supplying a Dock icon.
		if runtime.GOOS == "darwin" {
			if err := mygo.App.Dock.SetIcon(resources.Icon); err != nil {
				log.Printf("set Dock icon: %v", err)
			}
		}
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:                "bqbot",
			Width:                1200,
			Height:               800,
			MinWidth:             720,
			MinHeight:            520,
			StateKey:             "main",
			URL:                  "/",
			TitleBarStyle:        mygo.TitleBarHidden,
			TrafficLightPosition: &mygo.Point{X: 20, Y: 20},
			BackgroundColor:      "light-dark(#faf9f6, #171a18)",
		})
		// Keep the privileged bridge on the bundled application page.
		win.Page().OnWillNavigate(func(e *mygo.NavigateEvent) { e.PreventDefault() })
		desktop.start(func() { _ = desktopChanged.Emit(win, true) })
		win.OnClosed(desktop.close)
	})
	return mygo.App.Run()
}
