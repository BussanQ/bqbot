package main

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// palette holds the app's colors, for the light and the dark appearance.
type palette struct {
	dark     bool
	bg       ui.Color // the chat's background
	sidebar  ui.Color // under the sidebar where no material shows
	text     ui.Color
	muted    ui.Color
	faint    ui.Color
	hairline ui.Color
	hover    ui.Color
	press    ui.Color
	field    ui.Color // the composer's card
	bubble   ui.Color // under code and the person's quoted text
	accent   ui.Color
	onAccent ui.Color
	shade    ui.Color
}

func paletteFor(t *ui.Theme) palette {
	if t.Dark {
		return palette{
			dark:     true,
			bg:       ui.Hex("#141416"),
			sidebar:  ui.Hex("#1c1c1f"),
			text:     ui.Hex("#f5f5f7"),
			muted:    ui.RGBA(235, 235, 245, 0.56),
			faint:    ui.RGBA(235, 235, 245, 0.3),
			hairline: ui.RGBA(255, 255, 255, 0.07),
			hover:    ui.RGBA(255, 255, 255, 0.06),
			press:    ui.RGBA(255, 255, 255, 0.1),
			field:    ui.Hex("#1f1f23"),
			bubble:   ui.RGBA(255, 255, 255, 0.05),
			accent:   ui.Oklch(0.68, 0.16, 265),
			onAccent: ui.Hex("#ffffff"),
			shade:    ui.RGBA(0, 0, 0, 0.45),
		}
	}
	return palette{
		bg:       ui.Hex("#ffffff"),
		sidebar:  ui.Hex("#f4f4f6"),
		text:     ui.Hex("#1d1d1f"),
		muted:    ui.RGBA(60, 60, 67, 0.62),
		faint:    ui.RGBA(60, 60, 67, 0.32),
		hairline: ui.RGBA(0, 0, 0, 0.07),
		hover:    ui.RGBA(0, 0, 0, 0.04),
		press:    ui.RGBA(0, 0, 0, 0.08),
		field:    ui.Hex("#ffffff"),
		bubble:   ui.RGBA(0, 0, 0, 0.035),
		accent:   ui.Oklch(0.58, 0.19, 265),
		onAccent: ui.Hex("#ffffff"),
		shade:    ui.RGBA(0, 0, 0, 0.1),
	}
}

// hoverFade is how a control's background follows the pointer.
var hoverFade = ui.Animation{Duration: 120 * time.Millisecond}

// topBar returns the height of the strip along the top of the window, as
// tall as the window buttons and the room around them.
func topBar(c *ui.Context) float32 {
	return max(c.TitleBar().Height, 52)
}

// roundButton is a borderless round button, which a soft circle marks under
// the pointer and which gives a little as it is pressed.
func roundButton(c *ui.Context, size float32, color ui.Color, label string) ui.Element {
	b := ui.ButtonBase(c).Size(size+14, size+14).Center().Radius(999).Label(label).TextColor(color)
	bg := ui.Transparent
	if b.Hovered() {
		bg = ui.RGBA(128, 128, 128, 0.14)
	}
	b.Background(b.AnimateColor("bg", bg, hoverFade))
	pose := ui.Pose{}
	if b.Pressed() {
		pose.Scale = 0.88
	}
	b.AnimatePose(pose, ui.Snappy)
	return b
}

// pillButton is a rounded button with an icon and a label, filled with bg.
func pillButton(c *ui.Context, icon *ui.SVG, label string, bg, fg ui.Color) ui.Element {
	b := ui.ButtonBase(c).Height(34).PaddingX(16).Gap(8).Radius(999).AlignItems(ui.Center).TextColor(fg)
	fill := bg
	if b.Hovered() {
		fill = bg.Mix(ui.RGB(255, 255, 255), 0.12)
	}
	b.Background(b.AnimateColor("bg", fill, hoverFade))
	pose := ui.Pose{}
	if b.Pressed() {
		pose.Scale = 0.96
	}
	b.AnimatePose(pose, ui.Snappy)
	b.Children(func() {
		if icon != nil {
			ui.Icon(c, icon).Size(14, 14)
		}
		ui.Text(c, label).FontSize(13.5).FontWeight(600).SingleLine()
	})
	return b
}
