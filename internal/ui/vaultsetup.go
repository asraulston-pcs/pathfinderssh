// internal/ui/vaultsetup.go
// First-run vault setup: whether to offer to create one, and what to say.
//
// Same split as aboutinfo/settingsfields — the decision and the wording live
// here where they can be tested, and the Fyne side is left deciding only which
// widgets carry them.
//
// The question this answers is narrower than it looks. A vault is not required
// to use the application: a session can carry its own username and password,
// and a crawl or a capture accepts a static credential typed into its launch
// form. What a vault buys is UNATTENDED work — one stored set of credentials
// that a whole-estate map run or config backup can resolve per device without
// anyone at the keyboard. So the prompt is a warning about what will not work
// well, not a gate, and declining it has to stay a legitimate answer.
package ui

import (
	"fmt"
	"strings"
)

// VaultMinMaster is the shortest master password a new vault will accept.
//
// It duplicates the rule in internal/vault deliberately: this package cannot
// import the vault (the terminal would drag credential storage behind it), and
// a form that lets a person type a six-character password, confirm it, and only
// then be refused by the layer underneath has asked twice for nothing.
const VaultMinMaster = 8

// VaultCheck is what a run knows about its vault before the window opens.
type VaultCheck struct {
	// Path is the vault file this run resolved: the one named on the
	// command line, or the standard location.
	Path string

	// Present reports whether a vault file exists at Path.
	Present bool

	// Unlocked reports whether a vault was already opened from the keyring
	// or the environment. Separate from Present because a run that unlocked
	// silently has nothing to ask about even if the check ran first.
	Unlocked bool

	// Declined records that this person has already been asked on a
	// previous run and said no. A warning that returns every launch is a
	// warning that gets clicked through, and someone using static
	// credentials is answering the question correctly.
	Declined bool
}

// ShouldOffer reports whether this run should raise the first-run prompt.
func (c VaultCheck) ShouldOffer() bool {
	if c.Unlocked || c.Present || c.Declined {
		return false
	}
	return strings.TrimSpace(c.Path) != ""
}

// VaultSetupTitle is the heading for the first-run prompt.
const VaultSetupTitle = "No credential vault"

// VaultSetupWarning is the body of the first-run prompt.
//
// It names the two things that get worse rather than saying the vault is
// required, because it is not, and a first-run dialog that overstates its case
// teaches people to dismiss the next one.
func VaultSetupWarning(path string) string {
	return fmt.Sprintf(`No vault was found at:

    %s

The vault is where crawl and capture read their credentials, which is what lets
a map run or a config backup work through an estate unattended — each device
resolved against the stored credentials rather than one password typed into the
launch form.

Without a vault both still run, but only with a single username and password
entered by hand each time, and per-device credential selection is unavailable.
Terminal sessions are unaffected: a session can carry its own credentials.

Create a vault now?`, path)
}

// VaultCreateForm is the create-a-vault form as data.
//
// Confirm is not friction. A new master password has nothing to check against,
// so a typo is unrecoverable in the worst way available: the vault opens for
// nobody, and every credential added afterwards is encrypted to a password no
// one knows. The second field is the only thing standing between a slip and
// that.
type VaultCreateForm struct {
	Path    string
	Master  string
	Confirm string
}

// Validate returns every problem with the form, in the order a person reading
// the dialog top to bottom would meet them.
func (f VaultCreateForm) Validate() []ValidationProblem {
	var errs []ValidationProblem
	add := func(field, msg string) {
		errs = append(errs, ValidationProblem{Field: field, Message: msg})
	}

	if strings.TrimSpace(f.Path) == "" {
		add("vault", "required — name the file to create")
	}

	switch {
	case f.Master == "":
		add("master password", "required — an unprotected vault is a plaintext credential file")
	case len(f.Master) < VaultMinMaster:
		add("master password", fmt.Sprintf("at least %d characters", VaultMinMaster))
	}

	// Only worth reporting once the password itself is usable, or a short
	// password produces two complaints about one mistake.
	if f.Master != "" && len(f.Master) >= VaultMinMaster && f.Master != f.Confirm {
		add("confirm", "does not match — a mistyped master password cannot be recovered")
	}

	return errs
}

// VaultCreatedNote is shown after a vault is created.
//
// An empty credential picker does not explain itself, and the person who just
// created a vault is the one person guaranteed not to know yet how anything
// gets into it.
func VaultCreatedNote(path string) string {
	return fmt.Sprintf(`Created an empty vault at:

    %s

Add credentials from Vault → Manage credentials…, or from the command line with
pfvault add. A crawl or capture started before then still has nothing to
authenticate with.`, path)
}
