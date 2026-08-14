// internal/ui/assets.go
//
// Images compiled into the binary.
//
// # Why the directory is embedded and not the file
//
// //go:embed pathfinderlogo.png would be a BUILD FAILURE on any tree where
// somebody has not put the file there yet — a clone, a CI runner, a machine
// where the asset was gitignored by accident. Embedding the DIRECTORY compiles
// either way, and a missing logo then costs the logo instead of the build.
// Logo() returns nil, and the About box lays itself out without it.
//
// The cost is that a typo in the filename is silent. That is the right trade
// here: the picture is decoration and the application is not, and a tool that
// refuses to compile because an image is missing is a tool that cannot be
// built from a fresh checkout.
package ui

import (
	"embed"

	"fyne.io/fyne/v2"
)

// assetFS holds internal/ui/assets. Add images there; nothing else reads it.
//
//go:embed assets
var assetFS embed.FS

// logoFile is where the splash logo lives when there is one.
const logoFile = "assets/pathfinderlogo.png"

// Logo returns the product logo, or nil when the binary was built without one.
//
// Callers must handle nil. It is not an error worth reporting: an application
// that opened a dialog to say its picture is missing would be worse than the
// missing picture.
func Logo() fyne.Resource {
	data, err := assetFS.ReadFile(logoFile)
	if err != nil || len(data) == 0 {
		return nil
	}
	return fyne.NewStaticResource("pathfinderlogo.png", data)
}
