// internal/ui/sessionform.go
//
// The connection manager: one form, three transports, data in and data out.
//
// # Why there is exactly one of these
//
// TetherSSH ended up with two session dialogs — a quick-connect and an edit —
// and they drifted, so one of them was permanently missing features the other
// had. There is one form here and "quick connect" is that same form with
// Node.Ephemeral set. If a field is worth having in one, it is already in the
// other, because there is no other.
//
// # Data in, data out
//
// The form cannot connect, cannot read a vault, cannot touch a session
// registry and does not own a window. It takes a node, edits it, and hands it
// back through a callback. Everything that has a consequence happens in the
// caller. That boundary is what keeps this from becoming the 2,000-line
// catch-all the last one turned into — a form that can dial acquires a
// connection state machine, and then it acquires error dialogs, and then it is
// the application.
//
// # The transport selector drives the layout, not three dialogs
//
// SSH, telnet and serial share more fields than they differ in, and the
// terminal treats all three identically. The groups that do not apply are
// hidden rather than absent: the node keeps its serial framing while it is an
// SSH session, so switching the selector back does not lose what was typed.
//
// # Follows the view contract
//
// Constructed after app.New() (it builds widgets, and Fyne resolves the theme
// through the current app — building one earlier nil-derefs inside
// Button.CreateRenderer and the panic names a layout function). Content()
// returns a CanvasObject so the host decides placement. Callbacks out,
// imports in.
package ui

import (
	"fmt"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/scottpeterman/pathfinderssh/internal/sessions"
)

// CredentialNone is the credential-selector entry meaning "no vault entry;
// authenticate with what is typed here". It is a label rather than an empty
// string because an empty row in a dropdown reads as a rendering bug.
const CredentialNone = "(none — manual auth)"

// SessionFormOptions is everything the form needs that is not the node.
type SessionFormOptions struct {
	// Node is the session being edited. The zero value is not useful;
	// pass sessions.Defaults() for a new one.
	Node sessions.Node

	// Credentials are vault credential NAMES for the picker. Empty means
	// no vault is unlocked, and the selector says so rather than
	// offering an empty list — "the vault is locked" and "you have no
	// credentials" are different problems and only one of them is fixed
	// by typing a password into this form.
	Credentials []string

	// DefaultCredential is the vault's default credential name, or "" when
	// there is none. It changes only what the blank entry in the picker is
	// CALLED: with a default set, choosing nothing does not mean manual
	// auth, and a selector that still says "(none — manual auth)" is
	// describing the opposite of what will happen.
	DefaultCredential string

	// VaultLocked marks the difference above. It only changes the
	// selector's placeholder text.
	VaultLocked bool

	// ListSerialPorts is called for the port dropdown and again whenever
	// the refresh button is pressed, because a console cable gets plugged
	// in while the dialog is open more often than not. nil disables the
	// button and leaves the port a free-text field.
	ListSerialPorts func() []string

	// ShowSave and ShowConnect select which buttons appear. A quick
	// connect has no Save; an inventory editor has no Connect.
	ShowSave    bool
	ShowConnect bool

	// The three outcomes. Any of them may be nil, in which case the
	// button is not shown at all — a button that does nothing is worse
	// than an absent one.
	OnCancel  func()
	OnSave    func(sessions.Node)
	OnConnect func(sessions.Node)
}

// SessionForm is the editor. Build it with NewSessionForm.
type SessionForm struct {
	opts SessionFormOptions

	// --- identity ---
	name      *widget.Entry
	transport *widget.Select

	// --- network (ssh + telnet) ---
	host *widget.Entry
	port *widget.Entry

	// --- ssh auth ---
	username   *widget.Entry
	credential *widget.Select
	authType   *widget.Select
	password   *widget.Entry
	keyPath    *widget.Entry
	keyPass    *widget.Entry

	// --- telnet ---
	telnetCRLF *widget.Check

	// --- serial ---
	serialPort    *widget.Entry
	serialPortSel *widget.Select
	baud          *widget.Select
	dataBits      *widget.Select
	parity        *widget.Select
	stopBits      *widget.Select

	// --- terminal ---
	termTheme    *widget.Select
	fontSize     *widget.Select
	scrollback   *widget.Entry
	termType     *widget.Entry
	pasteDelay   *widget.Entry
	pasteWarn    *widget.Entry
	consoleBaud  *widget.Select
	logEnabled   *widget.Check
	antiIdleMode *widget.Select
	antiIdleSecs *widget.Entry

	// --- advanced / ssh policy ---
	hostKeyPolicy *widget.Select
	knownHosts    *widget.Entry
	legacyAlgos   *widget.Check
	timeoutSecs   *widget.Entry

	// --- jump ---
	jumpHost *widget.Entry
	jumpPort *widget.Entry
	jumpUser *widget.Entry
	jumpCred *widget.Select
	jumpKey  *widget.Entry
	jumpPass *widget.Entry

	// --- inventory ---
	vendor     *widget.Entry
	model      *widget.Entry
	deviceType *widget.Entry
	notes      *widget.Entry

	// Groups shown and hidden by the transport selector.
	networkGroup *fyne.Container
	sshGroup     *fyne.Container
	telnetGroup  *fyne.Container
	serialGroup  *fyne.Container
	sshAdvanced  *fyne.Container

	tabs    *container.AppTabs
	status  *widget.Label
	buttons []*widget.Button
	content fyne.CanvasObject

	// The palette selector shows display labels; the node stores registry
	// keys. Both directions are needed, so both maps are kept.
	labelToThemeKey map[string]string
	themeKeyToLabel map[string]string
}

// NewSessionForm builds the form. Call it after app.New().
func NewSessionForm(o SessionFormOptions) *SessionForm {
	f := &SessionForm{opts: o}
	f.build()
	f.SetNode(o.Node)
	return f
}

// Content is the object to place in a window, dialog or split. The form does
// not decide where it lives.
func (f *SessionForm) Content() fyne.CanvasObject { return f.content }

// Node reads the form back into a node and reports whether it is usable.
//
// It always returns the node, valid or not: an invalid node is still the thing
// the user typed, and a caller that wants to keep editing needs it. Callers
// that act on it check the bool.
func (f *SessionForm) Node() (sessions.Node, bool) {
	n := f.read()
	errs := n.Validate()
	if len(errs) == 0 {
		f.status.SetText("")
		return n, true
	}
	f.showErrors(errs)
	return n, false
}

// SetNode loads a node into the form, replacing whatever was there.
func (f *SessionForm) SetNode(n sessions.Node) {
	n = n.Normalize()

	f.name.SetText(n.Name)
	f.transport.SetSelected(string(n.Transport))

	f.host.SetText(n.Host)
	f.port.SetText(itoa(n.Port))

	f.username.SetText(n.Username)
	f.credential.SetSelected(f.credLabel(n.Credential))
	f.authType.SetSelected(n.AuthType)
	f.password.SetText(n.Password)
	f.keyPath.SetText(n.KeyPath)
	f.keyPass.SetText(n.KeyPassphrase)

	f.telnetCRLF.SetChecked(n.CRLF())

	f.setSerialPort(n.SerialPort)
	f.baud.SetSelected(itoa(n.Baud))
	f.dataBits.SetSelected(itoa(n.DataBits))
	f.parity.SetSelected(n.Parity)
	f.stopBits.SetSelected(n.StopBits)

	f.termTheme.SetSelected(f.themeLabelFor(n.TerminalTheme))
	f.fontSize.SetSelected(fontSizeLabel(n.FontSize))
	f.scrollback.SetText(itoa(n.ScrollbackLines))
	f.termType.SetText(n.TermType)
	f.pasteDelay.SetText(itoa(n.PasteLineDelayMs))
	f.pasteWarn.SetText(itoa(n.PasteWarnLines))
	f.consoleBaud.SetSelected(consoleBaudLabel(n.ConsoleBaud))
	f.logEnabled.SetChecked(n.LogEnabled)
	f.antiIdleMode.SetSelected(string(n.AntiIdle.Mode))
	f.antiIdleSecs.SetText(itoa(n.AntiIdle.IntervalSec))

	f.hostKeyPolicy.SetSelected(n.HostKeyPolicy)
	f.knownHosts.SetText(n.KnownHostsPath)
	f.legacyAlgos.SetChecked(n.LegacyAlgorithms)
	f.timeoutSecs.SetText(itoa(n.ConnectTimeoutSec))

	f.jumpHost.SetText(n.Jump.Host)
	f.jumpPort.SetText(itoa(n.Jump.Port))
	f.jumpUser.SetText(n.Jump.Username)
	f.jumpCred.SetSelected(f.credLabel(n.Jump.Credential))
	f.jumpKey.SetText(n.Jump.KeyPath)
	f.jumpPass.SetText(n.Jump.KeyPassphrase)

	f.vendor.SetText(n.Vendor)
	f.model.SetText(n.Model)
	f.deviceType.SetText(n.DeviceType)
	f.notes.SetText(n.Notes)

	f.opts.Node = n
	f.applyTransport()
	f.status.SetText("")
}

// SetStatus writes the line under the form. Used by the host for dial
// progress and dial failures — the form has no way to learn either.
func (f *SessionForm) SetStatus(msg string) { f.status.SetText(msg) }

// SetBusy disables the buttons while a connection is in flight, so a slow
// device does not collect three dial attempts from an impatient double-click.
func (f *SessionForm) SetBusy(busy bool) {
	for _, b := range f.buttons {
		if busy {
			b.Disable()
		} else {
			b.Enable()
		}
	}
}

// --- construction ---------------------------------------------------------

func (f *SessionForm) build() {
	f.name = entry("lab-r1")
	f.transport = widget.NewSelect(transportNames(), func(string) { f.applyTransport() })

	f.host = entry("lab-r1.lab.example or 172.16.1.2")
	f.port = entry("22")

	f.username = entry("admin")
	f.credential = widget.NewSelect(f.credentialChoices(), func(string) { f.applyCredentialState() })
	f.authType = widget.NewSelect(sessions.AuthTypes, func(string) { f.applyCredentialState() })
	f.password = widget.NewPasswordEntry()
	f.password.SetPlaceHolder("not saved to the session file")
	f.keyPath = entry("~/.ssh/id_ed25519")
	f.keyPass = widget.NewPasswordEntry()
	f.keyPass.SetPlaceHolder("not saved to the session file")

	f.telnetCRLF = widget.NewCheck("Send CR as CR LF", nil)

	f.serialPort = entry("/dev/ttyUSB0 or COM3")
	f.baud = widget.NewSelect(intStrings(sessions.BaudRates), nil)
	f.dataBits = widget.NewSelect(intStrings(sessions.DataBits), nil)
	f.parity = widget.NewSelect(sessions.Parities, nil)
	f.stopBits = widget.NewSelect(sessions.StopBits, nil)

	labels, labelToKey, keyToLabel := ThemeMenuData()
	f.labelToThemeKey, f.themeKeyToLabel = labelToKey, keyToLabel
	f.termTheme = widget.NewSelect(labels, nil)
	f.fontSize = widget.NewSelect(fontSizeChoices(), nil)
	f.scrollback = entry("inherit")
	f.termType = entry("xterm-256color")
	f.pasteDelay = entry("0")
	f.pasteWarn = entry("0")
	f.consoleBaud = widget.NewSelect(consoleBaudChoices(), nil)
	f.logEnabled = widget.NewCheck("Write a session transcript", nil)
	f.antiIdleMode = widget.NewSelect(antiIdleModeNames(), nil)
	f.antiIdleSecs = entry("inherit")

	f.hostKeyPolicy = widget.NewSelect(sessions.HostKeyPolicies, nil)
	f.knownHosts = entry("~/.ssh/known_hosts")
	f.legacyAlgos = widget.NewCheck("Allow legacy KEX/cipher/MAC (older gear)", nil)
	f.timeoutSecs = entry("20")

	f.jumpHost = entry("blank for a direct connection")
	f.jumpPort = entry("22")
	f.jumpUser = entry("admin")
	f.jumpCred = widget.NewSelect(f.credentialChoices(), nil)
	f.jumpKey = entry("~/.ssh/id_ed25519")
	f.jumpPass = widget.NewPasswordEntry()
	f.jumpPass.SetPlaceHolder("not saved to the session file")

	f.vendor = entry("Cisco")
	f.model = entry("ISR 4331")
	f.deviceType = entry("Router")
	f.notes = entry("free text")

	f.status = widget.NewLabel("")
	f.status.Wrapping = fyne.TextWrapWord

	f.tabs = container.NewAppTabs(
		container.NewTabItem("Connection", f.connectionTab()),
		container.NewTabItem("Terminal", f.terminalTab()),
		container.NewTabItem("Advanced", f.advancedTab()),
	)

	f.content = container.NewBorder(nil, f.footer(), nil, nil, f.tabs)
}

func (f *SessionForm) connectionTab() fyne.CanvasObject {
	head := form(
		row("Name", f.name),
		row("Transport", f.transport),
	)

	f.networkGroup = form(
		row("Host", f.host),
		row("Port", f.port),
	)

	f.sshGroup = form(
		row("Username", f.username),
		row("Vault credential", f.credential),
		row("Auth type", f.authType),
		row("Password", f.password),
		row("Key path", f.keyPath),
		row("Key passphrase", f.keyPass),
	)

	f.telnetGroup = form(
		row("Newlines", f.telnetCRLF),
	)

	// The port is a free-text entry with the detected list on its own row
	// beneath, rather than the two side by side.
	//
	// Side by side was the first attempt and it pushed the whole dialog
	// wider than the window: a form layout takes its minimum width from its
	// widest row, an entry and a select in a grid ask for twice one entry's
	// minimum, and the excess propagates out through the tab to the buttons,
	// which then sit off-screen. A composite cell in a form row is worth
	// suspecting whenever a dialog will not fit.
	//
	// Two rows also says the right thing: the entry is authoritative and the
	// list is a convenience, because the enumerator does not see every
	// adapter on every OS and a port that is not in the list still has to be
	// typeable.
	serialRows := [][2]fyne.CanvasObject{row("Serial port", f.serialPort)}
	if f.opts.ListSerialPorts != nil {
		f.serialPortSel = widget.NewSelect(f.opts.ListSerialPorts(), func(s string) {
			if s != "" {
				f.serialPort.SetText(s)
			}
		})
		f.serialPortSel.PlaceHolder = "(detected ports)"
		refresh := widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), func() {
			f.serialPortSel.SetOptions(f.opts.ListSerialPorts())
			f.serialPortSel.Refresh()
		})
		// Border with the button on the trailing edge: the button keeps its
		// own minimum and the select absorbs the rest, so this row never
		// asks for more width than one field.
		serialRows = append(serialRows,
			row("Detected", container.NewBorder(nil, nil, nil, refresh, f.serialPortSel)))
	}
	serialRows = append(serialRows,
		row("Baud", f.baud),
		row("Data bits", f.dataBits),
		row("Parity", f.parity),
		row("Stop bits", f.stopBits),
	)

	f.serialGroup = form(serialRows...)

	return container.NewVScroll(container.NewVBox(
		head,
		widget.NewSeparator(),
		f.networkGroup,
		f.sshGroup,
		f.telnetGroup,
		f.serialGroup,
	))
}

func (f *SessionForm) terminalTab() fyne.CanvasObject {
	// Deliberately absent: the application chrome. Light or dark is one
	// setting for the whole window — per-session chrome would repaint the
	// app every time you changed tabs. The terminal palette below is the
	// per-session one, and the two are independent by design: the shipped
	// pairing is dark chrome around a light terminal.
	return container.NewVScroll(container.NewVBox(
		form(
			row("Terminal theme", f.termTheme),
			row("Font size", f.fontSize),
			row("Scrollback lines", f.scrollback),
			row("Terminal type", f.termType),
			row("Paste line delay (ms)", f.pasteDelay),
			row("Warn at paste lines", f.pasteWarn),
			row("Console line speed", f.consoleBaud),
		),
		widget.NewSeparator(),
		form(
			row("Logging", f.logEnabled),
			row("Anti-idle", f.antiIdleMode),
			row("Anti-idle interval (s)", f.antiIdleSecs),
		),
		widget.NewLabel("Anti-idle sends a harmless keystroke after a quiet interval so a\n"+
			"session is not reaped while it is being read. Inherit follows the\n"+
			"application setting."),
	))
}

func (f *SessionForm) advancedTab() fyne.CanvasObject {
	f.sshAdvanced = container.NewVBox(
		form(
			row("Host key policy", f.hostKeyPolicy),
			row("known_hosts path", f.knownHosts),
			row("Algorithms", f.legacyAlgos),
			row("Connect timeout (s)", f.timeoutSecs),
		),
		widget.NewLabel("Trust on first use accepts a key never seen before and remembers it.\n"+
			"A key that CHANGED is refused under every policy except insecure."),
		widget.NewSeparator(),
		widget.NewLabel("Jump host — leave the host blank for a direct connection."),
		form(
			row("Jump host", f.jumpHost),
			row("Jump port", f.jumpPort),
			row("Jump username", f.jumpUser),
			row("Jump credential", f.jumpCred),
			row("Jump key path", f.jumpKey),
			row("Jump key passphrase", f.jumpPass),
		),
	)

	return container.NewVScroll(container.NewVBox(
		f.sshAdvanced,
		widget.NewSeparator(),
		widget.NewLabel("Inventory — free text, never used to pick a code path."),
		form(
			row("Vendor", f.vendor),
			row("Model", f.model),
			row("Device type", f.deviceType),
			row("Notes", f.notes),
		),
	))
}

func (f *SessionForm) footer() fyne.CanvasObject {
	bar := container.NewHBox(layout.NewSpacer())

	if f.opts.OnCancel != nil {
		b := widget.NewButtonWithIcon("Cancel", theme.CancelIcon(), func() {
			f.opts.OnCancel()
		})
		f.buttons = append(f.buttons, b)
		bar.Add(b)
	}
	if f.opts.ShowSave && f.opts.OnSave != nil {
		b := widget.NewButtonWithIcon("Save", theme.DocumentSaveIcon(), func() {
			if n, ok := f.Node(); ok {
				f.opts.OnSave(n)
			}
		})
		f.buttons = append(f.buttons, b)
		bar.Add(b)
	}
	if f.opts.ShowConnect && f.opts.OnConnect != nil {
		b := widget.NewButtonWithIcon("Connect", theme.ConfirmIcon(), func() {
			if n, ok := f.Node(); ok {
				f.opts.OnConnect(n)
			}
		})
		b.Importance = widget.HighImportance
		f.buttons = append(f.buttons, b)
		bar.Add(b)
	}

	return container.NewVBox(widget.NewSeparator(), f.status, bar)
}

// --- reading --------------------------------------------------------------

// read pulls the widgets into a node. It does not validate: an unparseable
// port becomes 0, which Normalize turns into the transport default and
// Validate then judges. Rejecting text at the keystroke is how a field becomes
// impossible to clear.
func (f *SessionForm) read() sessions.Node {
	n := f.opts.Node

	n.Name = f.name.Text
	n.Transport = sessions.Transport(f.transport.Selected)

	n.Host = f.host.Text
	n.Port = atoi(f.port.Text)

	n.Username = f.username.Text
	n.Credential = f.credRef(f.credential.Selected)
	n.AuthType = f.authType.Selected
	n.Password = f.password.Text
	n.KeyPath = f.keyPath.Text
	n.KeyPassphrase = f.keyPass.Text

	n.SetCRLF(f.telnetCRLF.Checked)

	n.SerialPort = f.serialPort.Text
	n.Baud = atoi(f.baud.Selected)
	n.DataBits = atoi(f.dataBits.Selected)
	n.Parity = f.parity.Selected
	n.StopBits = f.stopBits.Selected

	n.TerminalTheme = f.themeKeyFor(f.termTheme.Selected)
	n.FontSize = atoi(f.fontSize.Selected) // "(inherit)" parses to 0
	n.ScrollbackLines = atoi(f.scrollback.Text)
	n.TermType = f.termType.Text
	n.PasteLineDelayMs = atoi(f.pasteDelay.Text)
	n.PasteWarnLines = atoi(f.pasteWarn.Text)    // 0 inherits, negative never asks
	n.ConsoleBaud = atoi(f.consoleBaud.Selected) // "(full speed)" parses to 0
	n.LogEnabled = f.logEnabled.Checked
	n.AntiIdle = sessions.AntiIdleSpec{
		Mode:        sessions.AntiIdleMode(f.antiIdleMode.Selected),
		IntervalSec: atoi(f.antiIdleSecs.Text),
	}

	n.HostKeyPolicy = f.hostKeyPolicy.Selected
	n.KnownHostsPath = f.knownHosts.Text
	n.LegacyAlgorithms = f.legacyAlgos.Checked
	n.ConnectTimeoutSec = atoi(f.timeoutSecs.Text)

	n.Jump = sessions.JumpSpec{
		Host:          f.jumpHost.Text,
		Port:          atoi(f.jumpPort.Text),
		Username:      f.jumpUser.Text,
		Credential:    f.credRef(f.jumpCred.Selected),
		KeyPath:       f.jumpKey.Text,
		KeyPassphrase: f.jumpPass.Text,
	}

	n.Vendor = f.vendor.Text
	n.Model = f.model.Text
	n.DeviceType = f.deviceType.Text
	n.Notes = f.notes.Text

	return n.Normalize()
}

// --- reactions ------------------------------------------------------------

// applyTransport shows the groups the selected transport uses and hides the
// rest. Hidden, not cleared: switching to serial and back must not lose the
// hostname that was typed.
func (f *SessionForm) applyTransport() {
	t := sessions.Transport(f.transport.Selected)

	show(f.networkGroup, t.IsNetwork())
	show(f.sshGroup, t == sessions.TransportSSH)
	show(f.telnetGroup, t == sessions.TransportTelnet)
	show(f.serialGroup, t == sessions.TransportSerial)
	show(f.sshAdvanced, t == sessions.TransportSSH)

	// Move the port to the new transport's default, but only when it is
	// still sitting on the other one's — an explicit 2201 is the whole
	// reason the field exists and must survive a transport change.
	if t.IsNetwork() {
		cur := atoi(f.port.Text)
		for _, other := range sessions.Transports {
			if other != t && other.IsNetwork() && cur == other.DefaultPort() {
				f.port.SetText(itoa(t.DefaultPort()))
				break
			}
		}
	}

	f.applyCredentialState()
	if f.content != nil {
		f.content.Refresh()
	}
}

// applyCredentialState grays out the fields a chosen credential supplies, and
// the auth fields the chosen auth type does not use.
//
// Disabling rather than hiding: a password box that vanishes when you pick a
// key looks like the form lost your password. Grayed out, with the credential
// name visible above it, says where the value is coming from instead.
func (f *SessionForm) applyCredentialState() {
	// A NAMED credential supplies these fields, so they are grayed. The
	// blank entry leaves them ENABLED even when a default stands behind
	// it: the default is all-or-nothing and stays out of a node that
	// states auth of its own, so typing a username here is exactly how a
	// session opts back out to manual auth.
	usingVault := f.credRef(f.credential.Selected) != ""

	setEnabled(f.password, !usingVault && f.authType.Selected == sessions.AuthPassword)
	setEnabled(f.keyPath, !usingVault && f.authType.Selected == sessions.AuthPublicKey)
	setEnabled(f.keyPass, !usingVault && f.authType.Selected == sessions.AuthPublicKey)
	setEnabled(f.authType, !usingVault)
	setEnabled(f.username, !usingVault)
}

// showErrors puts every field error on the status line and jumps to the tab
// holding the first one, so a problem on a tab you cannot see does not read as
// a button that stopped working.
func (f *SessionForm) showErrors(errs []sessions.FieldError) {
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		parts = append(parts, e.Error())
	}
	f.status.SetText("⚠  " + strings.Join(parts, "   •   "))
	if len(errs) > 0 && f.tabs != nil {
		f.tabs.SelectIndex(tabForField(errs[0].Field))
	}
}

// tabForField maps a model field name onto the tab that edits it. When the two
// drift, an error points at the wrong tab — which is a smaller failure than
// pointing at none, and the reason the default is the Connection tab.
func tabForField(field string) int {
	switch {
	case strings.HasPrefix(field, "jump."),
		field == "host_key_policy",
		field == "connect_timeout_sec":
		return 2
	case strings.HasPrefix(field, "anti_idle"),
		field == "font_size",
		field == "scrollback_lines",
		field == "paste_line_delay_ms":
		return 1
	default:
		return 0
	}
}

// --- small helpers --------------------------------------------------------

func (f *SessionForm) credentialChoices() []string {
	out := []string{f.blankCredentialLabel()}
	return append(out, f.opts.Credentials...)
}

func (f *SessionForm) setSerialPort(p string) {
	f.serialPort.SetText(p)
	if f.serialPortSel != nil && p != "" {
		f.serialPortSel.SetSelected(p)
	}
}

func (f *SessionForm) themeLabelFor(key string) string {
	if key == "" {
		key = DefaultTerminalTheme
	}
	if label, ok := f.themeKeyToLabel[key]; ok {
		return label
	}
	return key
}

func (f *SessionForm) themeKeyFor(label string) string {
	if key, ok := f.labelToThemeKey[label]; ok {
		return key
	}
	return label
}

// CredentialDefaultPrefix opens the blank entry's label when a default exists.
// Both spellings of the blank entry map back to "" — the default can change
// while a dialog is open, and a stale label must never be saved as a
// credential name.
const CredentialDefaultPrefix = "(vault default"

// blankCredentialLabel is what "name no credential" is called in the picker.
func (f *SessionForm) blankCredentialLabel() string {
	if n := strings.TrimSpace(f.opts.DefaultCredential); n != "" {
		return CredentialDefaultPrefix + " — " + n + ")"
	}
	return CredentialNone
}

func (f *SessionForm) credLabel(ref string) string {
	if strings.TrimSpace(ref) == "" {
		return f.blankCredentialLabel()
	}
	return ref
}

func (f *SessionForm) credRef(label string) string {
	label = strings.TrimSpace(label)
	if label == CredentialNone || strings.HasPrefix(label, CredentialDefaultPrefix) {
		return ""
	}
	return label
}

func entry(placeholder string) *widget.Entry {
	e := widget.NewEntry()
	e.SetPlaceHolder(placeholder)
	return e
}

// row is one label/field pair for a form layout.
func row(label string, field fyne.CanvasObject) [2]fyne.CanvasObject {
	return [2]fyne.CanvasObject{widget.NewLabel(label), field}
}

// form lays rows out with the labels aligned in one column.
func form(rows ...[2]fyne.CanvasObject) *fyne.Container {
	objs := make([]fyne.CanvasObject, 0, len(rows)*2)
	for _, r := range rows {
		objs = append(objs, r[0], r[1])
	}
	return container.New(layout.NewFormLayout(), objs...)
}

func show(c *fyne.Container, visible bool) {
	if c == nil {
		return
	}
	if visible {
		c.Show()
	} else {
		c.Hide()
	}
	c.Refresh()
}

func setEnabled(w fyne.Disableable, enabled bool) {
	if enabled {
		w.Enable()
	} else {
		w.Disable()
	}
}

func transportNames() []string {
	out := make([]string, 0, len(sessions.Transports))
	for _, t := range sessions.Transports {
		out = append(out, string(t))
	}
	return out
}

func antiIdleModeNames() []string {
	out := make([]string, 0, len(sessions.AntiIdleModes))
	for _, m := range sessions.AntiIdleModes {
		out = append(out, string(m))
	}
	return out
}

func intStrings(vals []int) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		out = append(out, strconv.Itoa(v))
	}
	return out
}

// SizeInherit is the font-size choice meaning "follow the application
// setting". A saved session that pinned a size the day it was created would
// stop following the app forever, which is not what picking nothing means.
const SizeInherit = "(inherit)"

func fontSizeChoices() []string {
	out := make([]string, 0, MaxTerminalFontSize-MinTerminalFontSize+2)
	out = append(out, SizeInherit)
	for s := MinTerminalFontSize; s <= MaxTerminalFontSize; s++ {
		out = append(out, strconv.Itoa(s))
	}
	return out
}

func fontSizeLabel(size int) string {
	if size <= 0 {
		return SizeInherit
	}
	return strconv.Itoa(ClampTerminalFontSize(size))
}

// ConsoleBaudFull is the console-speed choice meaning "do not pace at all".
// It is the right answer for SSH and for anything that buffers properly, so it
// is the default rather than a speed somebody has to turn off.
const ConsoleBaudFull = "(full speed)"

// consoleBaudRates are the speeds a console server is actually set to. Free
// text was the alternative and it buys nothing: nobody has a 7,200 baud
// console, and a typo in a field like this shows up as a paste that is
// mysteriously slow rather than as an error.
var consoleBaudRates = []int{1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200}

func consoleBaudChoices() []string {
	out := make([]string, 0, len(consoleBaudRates)+1)
	out = append(out, ConsoleBaudFull)
	for _, r := range consoleBaudRates {
		out = append(out, strconv.Itoa(r))
	}
	return out
}

func consoleBaudLabel(baud int) string {
	if baud <= 0 {
		return ConsoleBaudFull
	}
	return strconv.Itoa(baud)
}

// atoi is deliberately forgiving: blank and unparseable both mean zero, which
// the model reads as "unset" and fills in.
func atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// itoa renders zero as blank, so an unset numeric field shows its placeholder
// rather than a 0 the user has to delete.
func itoa(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// Describe is a one-line summary of what this form would connect to, for a
// title bar or a log line.
func (f *SessionForm) Describe() string {
	n := f.read()
	return fmt.Sprintf("%s (%s) %s", n.Label(), n.Transport, n.Target())
}
