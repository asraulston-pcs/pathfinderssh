// internal/ui/about.go
//
// The About box: the logo, the version, and a Copy button.
//
// The facts and their formatting are in aboutinfo.go, which has no toolkit in
// it and is tested. This file is layout.
//
// # Sizing a "fairly large" logo
//
// A canvas.Image reports the pixel size of its source as its minimum, so a
// 1600-pixel-wide splash asset makes a 1600-pixel-wide dialog and Fyne will
// happily open one bigger than the screen. SetMinSize replaces that with a box
// we choose, and ImageFillContain fits the picture inside it — letterboxing
// rather than stretching, so an asset of any aspect ratio arrives undistorted
// and nobody has to re-export it to match a number in this file.
//
// # Why there is a Copy button
//
// The version and the paths are what somebody pastes into a bug report, and a
// Fyne Label cannot be selected. Without this the only way to report which
// build you are running is to read it off the screen and retype it.
package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Logo box, in Fyne's device-independent units. The picture is fitted inside
// this; it is never scaled up past its own size by Contain, so a small asset
// stays crisp and a large one is bounded.
const (
	aboutLogoWidth  float32 = 420
	aboutLogoHeight float32 = 200
)

// ShowAbout opens the About box over w.
func ShowAbout(w fyne.Window, info AboutInfo) {
	heading := widget.NewLabelWithStyle(info.Heading(), fyne.TextAlignCenter,
		fyne.TextStyle{Bold: true})
	version := widget.NewLabelWithStyle(info.VersionLine(), fyne.TextAlignCenter,
		fyne.TextStyle{})

	head := []fyne.CanvasObject{}
	if res := Logo(); res != nil {
		img := canvas.NewImageFromResource(res)
		img.FillMode = canvas.ImageFillContain
		img.SetMinSize(fyne.NewSize(aboutLogoWidth, aboutLogoHeight))
		head = append(head, img)
	}
	head = append(head, heading, version)

	if t := info.Tagline; t != "" {
		tag := widget.NewLabelWithStyle(t, fyne.TextAlignCenter, fyne.TextStyle{Italic: true})
		tag.Wrapping = fyne.TextWrapWord
		head = append(head, tag)
	}

	body := container.NewVBox(head...)

	// The details go in a form-shaped grid rather than one long label, so
	// a long path wraps in its own column instead of pushing the labels
	// out of alignment.
	if shown := info.Shown(); len(shown) > 0 {
		pairs := make([]any, 0, len(shown)*2)
		for _, d := range shown {
			value := widget.NewLabel(d.Value)
			value.Wrapping = fyne.TextWrapBreak
			pairs = append(pairs, d.Label, value)
		}
		body.Add(widget.NewSeparator())
		body.Add(formOf(pairs...))
	}

	status := statusLabel()
	copyBtn := widget.NewButtonWithIcon("Copy details", theme.ContentCopyIcon(), func() {
		// fyne.Window.Clipboard is deprecated in v2.6; the app-level one
		// is the supported route and is the same clipboard.
		fyne.CurrentApp().Clipboard().SetContent(info.Text())
		status.SetText("copied")
	})
	actions := container.NewHBox(copyBtn, status)

	content := container.NewBorder(nil, actions, nil, nil, body)

	d := dialog.NewCustom("About "+info.Heading(), "Close", content, w)
	d.Resize(fyne.NewSize(aboutLogoWidth+120, 520))
	d.Show()
}
