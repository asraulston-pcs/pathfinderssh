// internal/ui/aboutinfo.go
//
// What the About box says, with no toolkit in it.
//
// The dialog next door is a logo, a label and a button. This is the part worth
// testing: which facts are shown, in what order, and what they look like as
// text somebody can paste into a bug report — because that is the only reason
// an About box earns its place beyond the logo. A version number you cannot
// copy is a version number you retype wrong.
package ui

import "strings"

// AboutDetail is one labelled fact.
type AboutDetail struct {
	Label string
	Value string
}

// AboutInfo is everything the About box shows.
//
// The host fills it: this package knows nothing about where a vault lives or
// what version was built, and a UI that went looking would be a second opinion
// on both.
type AboutInfo struct {
	// Name and Tagline head the box. Empty Name falls back to the product
	// name rather than rendering a blank heading.
	Name    string
	Tagline string

	// Version is whatever the build stamped. Empty renders as "dev", which
	// is the honest answer for a binary built without -ldflags rather than
	// a blank that looks like a bug.
	Version string

	// Details are the paths and settings worth having in a bug report.
	// Order is the host's; nothing here is sorted, because "which vault"
	// matters more than alphabetical.
	Details []AboutDetail
}

// DefaultAppName is used when AboutInfo.Name is empty.
const DefaultAppName = "PathfinderSSH"

// Heading is the product name as shown.
func (a AboutInfo) Heading() string {
	if n := strings.TrimSpace(a.Name); n != "" {
		return n
	}
	return DefaultAppName
}

// VersionLine is the version as shown.
func (a AboutInfo) VersionLine() string {
	if v := strings.TrimSpace(a.Version); v != "" {
		return "version " + v
	}
	return "version dev"
}

// Text renders the whole box as plain text, for the Copy button.
//
// Plain text and not the rendered widget: what somebody pastes into an issue
// should be the facts, and a label they cannot select is the reason this
// exists at all. Details with an empty value are dropped — a bug report
// carrying "Vault:" and nothing after it wastes a round trip asking which
// vault.
func (a AboutInfo) Text() string {
	var b strings.Builder
	b.WriteString(a.Heading())
	b.WriteString("\n")
	b.WriteString(a.VersionLine())
	if t := strings.TrimSpace(a.Tagline); t != "" {
		b.WriteString("\n")
		b.WriteString(t)
	}
	for _, d := range a.Details {
		if strings.TrimSpace(d.Value) == "" {
			continue
		}
		b.WriteString("\n")
		b.WriteString(d.Label)
		b.WriteString(": ")
		b.WriteString(d.Value)
	}
	return b.String()
}

// Shown returns the details that will actually render, applying the same
// empty-value rule as Text so the box and the clipboard cannot disagree.
func (a AboutInfo) Shown() []AboutDetail {
	out := make([]AboutDetail, 0, len(a.Details))
	for _, d := range a.Details {
		if strings.TrimSpace(d.Value) == "" {
			continue
		}
		out = append(out, d)
	}
	return out
}