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
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Logo box, in Fyne's device-independent units. The picture is fitted inside
// this; it is never scaled up past its own size by Contain, so a small asset
// stays crisp and a large one is bounded.
const (
	aboutLogoWidth  float32 = 420
	aboutLogoHeight float32 = 200

	// Tall enough for the logo, the three heading lines and three detail
	// rows without scrolling. The scroll is the overflow path for a host
	// that reports more than that, not the normal case.
	aboutDialogHeight float32 = 600
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
	//
	// Built here rather than through formOf, which wraps its grid in a
	// VScroll. That is right for a launch dialog, which is taller than
	// the window it opens in, and wrong here: a scroll container's
	// minimum size is small, and a VBox gives every child exactly its
	// minimum, so the whole details block collapsed to one visible row
	// with a scrollbar and the rest below the fold. The scroll this box
	// does want is around the WHOLE body, below, where the border
	// layout stretches it instead of shrinking it.
	if shown := info.Shown(); len(shown) > 0 {
		rows := make([]fyne.CanvasObject, 0, len(shown)*2)
		for _, d := range shown {
			value := widget.NewLabel(d.Value)
			value.Wrapping = fyne.TextWrapBreak
			rows = append(rows, widget.NewLabel(d.Label), value)
		}
		body.Add(widget.NewSeparator())
		body.Add(container.New(layout.NewFormLayout(), rows...))
	}

	status := statusLabel()
	copyBtn := widget.NewButtonWithIcon("Copy details", theme.ContentCopyIcon(), func() {
		// fyne.Window.Clipboard is deprecated in v2.6; the app-level one
		// is the supported route and is the same clipboard.
		fyne.CurrentApp().Clipboard().SetContent(info.Text())
		status.SetText("copied")
	})
	actions := container.NewHBox(copyBtn, status)

	// Center of a border layout, so the scroll is given the space that is
	// left rather than asked how little it can take. Actions stay pinned
	// at the bottom: Copy and Close must not be what scrolls away when a
	// host reports four long paths instead of one.
	content := container.NewBorder(nil, actions, nil, nil, container.NewVScroll(body))

	d := dialog.NewCustom("About "+info.Heading(), "Close", content, w)
	d.Resize(fyne.NewSize(aboutLogoWidth+120, aboutDialogHeight))
	d.Show()
}