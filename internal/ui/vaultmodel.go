// internal/ui/vaultmodel.go
//
// The vault manager's rules, with no toolkit in them.
//
// Same split as treemodel.go and shellmodel.go, and for the same reason: what
// a credential row says, what a form field means and what makes a credential
// invalid are rules, and rules that need a display to test stop being tested.
// The widget file next door is layout and callbacks over this.
//
// # Why a form struct rather than editing a vault.Credential
//
// Every field on screen is text, including the ones that are not text in the
// model — priority is a number, tags and CIDRs are lists, scope is a struct.
// Parsing them in the widget means parsing them in the place that cannot be
// tested, and reporting the failures in whatever way each field's callback
// happens to. CredentialForm carries the text, Validate says everything wrong
// with it at once, and Credential() is the single conversion.
//
// # The secret is never loaded into the form
//
// An edit form prefilled with a decrypted password is a password sitting in a
// widget, in a window, for as long as the dialog is open — and the vault's own
// Meta type exists precisely so a list can be shown without one. So the form
// carries no secret on the way in: a blank password field on an existing
// credential means KEEP, and typing one means REPLACE. The cost is that there
// is no way to see what the password is, which is not something a credential
// manager owes anybody.
package ui

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/scottpeterman/pathfinderssh/internal/vault"
)

// VaultRow is one line of the credential list, already rendered to strings.
type VaultRow struct {
	ID       string
	Name     string
	Username string
	Auth     string
	Scope    string
	Tags     string
	Priority string

	// Flags is the human summary of default/disabled, which are the two
	// states that change what a run does without changing any field a
	// person set.
	Flags string

	LastUsed string

	// Disabled is carried separately so the view can gray the row rather
	// than relying on the operator reading the Flags column.
	Disabled bool
}

// VaultRows renders the redacted list the vault returns.
//
// Order: default first, then enabled before disabled, then priority, then
// name. The default leads because it is the one that answers when a session
// names nothing, which is the question a person opens this list to ask.
func VaultRows(metas []vault.Meta) []VaultRow {
	sorted := append([]vault.Meta(nil), metas...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.IsDefault != b.IsDefault {
			return a.IsDefault
		}
		if a.Disabled != b.Disabled {
			return !a.Disabled
		}
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})

	out := make([]VaultRow, 0, len(sorted))
	for _, m := range sorted {
		out = append(out, VaultRow{
			ID:       m.ID,
			Name:     m.Name,
			Username: m.Username,
			Auth:     m.AuthLabel,
			Scope:    ScopeSummary(m.Scope),
			Tags:     strings.Join(m.Tags, ", "),
			Priority: strconv.Itoa(m.Priority),
			Flags:    flagSummary(m),
			LastUsed: lastUsedSummary(m.LastUsed),
			Disabled: m.Disabled,
		})
	}
	return out
}

func flagSummary(m vault.Meta) string {
	var parts []string
	if m.IsDefault {
		parts = append(parts, "default")
	}
	if m.Disabled {
		parts = append(parts, "disabled")
	}
	// A credential whose method needs a secret and has none will fail every
	// device it is offered to, and the list is the only place that is
	// visible before a run.
	switch m.AuthLabel {
	case "Password", "Keyboard Interactive", "Public Key":
		if !m.HasSecret {
			parts = append(parts, "no secret")
		}
	}
	return strings.Join(parts, ", ")
}

func lastUsedSummary(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Local().Format("2006-01-02 15:04")
}

// ScopeSummary renders a scope the way the list shows it. An unrestricted
// scope reads "any" rather than empty: a blank cell looks like missing data,
// and "matches everything" is a decision worth seeing.
func ScopeSummary(s vault.Scope) string {
	if s.IsZero() {
		return "any"
	}
	var parts []string
	if s.DomainSuffix != "" {
		parts = append(parts, "*."+s.DomainSuffix)
	}
	if len(s.CIDRs) > 0 {
		parts = append(parts, strings.Join(s.CIDRs, " "))
	}
	if len(s.Platforms) > 0 {
		parts = append(parts, strings.Join(s.Platforms, "/"))
	}
	return strings.Join(parts, "  ")
}

// AuthChoices are the auth types the form offers, in the order it offers them.
//
// The stored strings, not the display labels: what goes in the file is what
// the resolver reads, and a display label that has to be mapped back is one
// more place the two can disagree.
var AuthChoices = []string{"password", "publickey", "agent", "keyboard-interactive"}

// CredentialForm is the editable text of one credential.
type CredentialForm struct {
	Name        string
	Username    string
	AuthType    string
	Password    string
	KeyPath     string
	Passphrase  string
	Description string
	Priority    string
	Tags        string

	DomainSuffix string
	CIDRs        string
	Platforms    string

	Disabled bool

	// HasSecret records whether the credential being edited already holds
	// one, so a blank Password can mean KEEP on an edit and MISSING on a
	// new credential. It is set by FormFor and never by a widget.
	HasSecret bool
}

// FormFor builds the form for an existing credential. No secret crosses over.
func FormFor(m vault.Meta) CredentialForm {
	return CredentialForm{
		Name:        m.Name,
		Username:    m.Username,
		AuthType:    authTypeOf(m.AuthLabel),
		Description: m.Description,
		Priority:    strconv.Itoa(m.Priority),
		Tags:        strings.Join(m.Tags, ", "),

		DomainSuffix: m.Scope.DomainSuffix,
		CIDRs:        strings.Join(m.Scope.CIDRs, ", "),
		Platforms:    strings.Join(m.Scope.Platforms, ", "),

		Disabled:  m.Disabled,
		HasSecret: m.HasSecret,
	}
}

// NewCredentialForm is the form a new credential opens with.
func NewCredentialForm() CredentialForm {
	return CredentialForm{AuthType: "password", Priority: "0"}
}

func authTypeOf(label string) string {
	switch label {
	case "Public Key":
		return "publickey"
	case "Agent":
		return "agent"
	case "Keyboard Interactive":
		return "keyboard-interactive"
	default:
		return "password"
	}
}

// Validate reports everything wrong with the form, not the first thing.
//
// taken is every OTHER credential's name; a duplicate is refused here because
// the vault resolves by name as well as by id, and two credentials answering
// to one name means the one that runs depends on iteration order.
func (f CredentialForm) Validate(taken []string) []ValidationProblem {
	var errs []ValidationProblem
	add := func(field, msg string) {
		errs = append(errs, ValidationProblem{Field: field, Message: msg})
	}

	name := strings.TrimSpace(f.Name)
	if name == "" {
		add("name", "required — it is how a session names this credential")
	}
	for _, t := range taken {
		if strings.EqualFold(strings.TrimSpace(t), name) && name != "" {
			add("name", fmt.Sprintf("%q is already used by another credential", name))
			break
		}
	}

	method := vault.StringToAuthType(f.AuthType)
	switch method {
	case vault.AuthPublicKey:
		if strings.TrimSpace(f.KeyPath) == "" {
			add("key_path", "a public-key credential needs a key file")
		}
	case vault.AuthAgent:
		// Nothing to hold: the agent has the key. A password typed here
		// would be stored and never offered, which is worse than
		// refusing it.
		if strings.TrimSpace(f.Password) != "" {
			add("password", "an agent credential offers no password; clear it or change the auth type")
		}
		if strings.TrimSpace(f.KeyPath) != "" {
			add("key_path", "an agent credential offers no key file; clear it or change the auth type")
		}
	default:
		// Password and keyboard-interactive both answer with a password.
		if strings.TrimSpace(f.Password) == "" && !f.HasSecret {
			add("password", "required for this auth type")
		}
	}

	if strings.TrimSpace(f.Username) == "" && method != vault.AuthAgent {
		add("username", "required — a credential with no username cannot authenticate")
	}

	if p := strings.TrimSpace(f.Priority); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			add("priority", "must be a whole number")
		} else if n < 0 {
			add("priority", "must be 0 or more; lower runs first")
		}
	}

	for _, c := range splitList(f.CIDRs) {
		if _, err := netip.ParsePrefix(c); err != nil {
			add("cidrs", fmt.Sprintf("%q is not a prefix — write it like 10.0.0.0/8", c))
		}
	}

	if s := strings.TrimSpace(f.DomainSuffix); strings.HasPrefix(s, ".") {
		add("domain_suffix", "write it without the leading dot, e.g. lab.example")
	}

	return errs
}

// ValidationProblem is one thing wrong with a form.
//
// A local type rather than one borrowed from a run model: this package cannot
// import capturerun or crawlrun without the terminal dragging a run model
// behind it, and the shape is three lines.
type ValidationProblem struct {
	Field   string
	Message string
}

func (p ValidationProblem) Error() string { return p.Field + ": " + p.Message }

// ProblemText joins problems the way a status line shows them.
func ProblemText(errs []ValidationProblem) string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Error())
	}
	return strings.Join(out, " · ")
}

// Credential applies the form to base and returns what to store.
//
// base is the credential being edited, so its ID, CreatedAt, LastUsed and —
// when the password field was left blank — its existing secret survive the
// edit. For a new credential pass the zero value.
func (f CredentialForm) Credential(base vault.Credential) vault.Credential {
	c := base
	c.Name = strings.TrimSpace(f.Name)
	c.Username = strings.TrimSpace(f.Username)
	c.AuthType = strings.ToLower(strings.TrimSpace(f.AuthType))
	c.Description = strings.TrimSpace(f.Description)
	c.Disabled = f.Disabled
	c.Priority, _ = strconv.Atoi(strings.TrimSpace(f.Priority))
	c.Tags = splitList(f.Tags)
	c.Scope = vault.Scope{
		DomainSuffix: strings.TrimSpace(f.DomainSuffix),
		CIDRs:        splitList(f.CIDRs),
		Platforms:    lowerAll(splitList(f.Platforms)),
	}

	// Secret material, by method. Changing the auth type CLEARS what the
	// old method used: a publickey credential that keeps a stale password
	// is a credential that authenticates in a way nobody chose, and it is
	// the kind of thing that only shows up as an audit-log surprise.
	switch vault.StringToAuthType(c.AuthType) {
	case vault.AuthPublicKey:
		c.Password = ""
		c.KeyPath = strings.TrimSpace(f.KeyPath)
		if p := f.Passphrase; p != "" {
			c.KeyPassphrase = p
		}
	case vault.AuthAgent:
		c.Password, c.KeyPath, c.KeyPassphrase = "", "", ""
	default:
		c.KeyPath, c.KeyPassphrase = "", ""
		if p := f.Password; p != "" {
			c.Password = p
		}
	}
	return c
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == ';'
	}) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func lowerAll(in []string) []string {
	for i := range in {
		in[i] = strings.ToLower(in[i])
	}
	return in
}

// OtherNames is every name in metas except the one with id, for Validate.
func OtherNames(metas []vault.Meta, id string) []string {
	out := make([]string, 0, len(metas))
	for _, m := range metas {
		if m.ID == id {
			continue
		}
		out = append(out, m.Name)
	}
	return out
}
