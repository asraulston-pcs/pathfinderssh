// cmd/pfconnect/main.go
// Manual harness for the connection manager.
//
// It is the counterpart to cmd/pfterm. pfterm proves the terminal renders and
// moves bytes; this proves a session described as DATA — a node the form
// produced — reaches the same place. Everything between the two is what the
// shell will eventually do for real:
//
//	form -> sessions.Node -> sessiondial.Connect -> ui.Session
//
//	go run ./cmd/pfconnect
//	go run ./cmd/pfconnect -vault ~/.pathfinderssh/vault.json
//	go run ./cmd/pfconnect -load lab-r1.yaml
//	go run ./cmd/pfconnect -app light
//
// Save is a stub, deliberately: there is no session file yet. It prints the
// YAML the node would be written as, which is also the cheapest possible proof
// that a password typed into the dialog does not reach disk — the field is
// simply not in the output.
//
// # What to check once it is up
//
//   - all three transports connect from the same dialog, with no CLI flags
//   - switching the transport selector and switching back does not lose what
//     was typed in the other group
//   - an explicit port (2201, a container console) survives a transport change
//     while a default one moves 22 <-> 23
//   - Terminal tab: font size and palette both take effect on the NEXT
//     connect, and the app chrome does not move when the palette does
//   - Back disconnects, returns to the form with the node intact, and a second
//     Connect works — the state that leaks between sessions shows up here
//   - a bad field marks the status line AND jumps to the tab holding it
//   - with -vault, picking a credential grays out username/auth and the
//     connection still authenticates
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"golang.org/x/crypto/ssh"

	"github.com/scottpeterman/pathfinderssh/internal/serialx"
	"github.com/scottpeterman/pathfinderssh/internal/sessiondial"
	"github.com/scottpeterman/pathfinderssh/internal/sessions"
	"github.com/scottpeterman/pathfinderssh/internal/term"
	"github.com/scottpeterman/pathfinderssh/internal/ui"
	"github.com/scottpeterman/pathfinderssh/internal/vault"
	"github.com/scottpeterman/pathfinderssh/internal/vaultcli"
)

func main() {
	var (
		vaultPath = flag.String("vault", "", "vault file for the credential picker; empty means manual auth only")
		appTheme  = flag.String("app", "dark", "application chrome: dark|light")
		loadPath  = flag.String("load", "", "load a session node from a YAML file")
		startNode = flag.String("name", "", "prefill the session name")
	)
	flag.Parse()

	log.SetFlags(log.Ltime | log.Lmicroseconds)

	node := sessions.Defaults()
	if *loadPath != "" {
		data, err := os.ReadFile(*loadPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reading %s: %v\n", *loadPath, err)
			os.Exit(1)
		}
		node, err = sessions.Unmarshal(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "parsing %s: %v\n", *loadPath, err)
			os.Exit(1)
		}
	}
	if *startNode != "" {
		node.Name = *startNode
	}

	// Vault first: it prompts on the controlling terminal, and doing that
	// after a window exists means prompting somewhere nobody is looking.
	creds, defaultCred, lookup := openVault(*vaultPath)

	base := ui.Defaults()
	base.AppTheme = ui.AppVariant(*appTheme)
	ui.SetSettings(base)

	// app.New() before ANY widget is constructed. Fyne resolves the theme
	// and driver through the current app, so building a widget first
	// nil-derefs inside Button.CreateRenderer and the panic names a layout
	// function rather than this line.
	a := app.New()
	ui.ApplyAppTheme(a, base.AppVariant())
	w := a.NewWindow("pfconnect")
	w.Resize(fyne.NewSize(760, 720))

	h := &host{app: a, win: w, base: base, node: node, creds: creds, defaultCred: defaultCred, lookup: lookup}
	h.showForm()

	w.SetOnClosed(func() {
		// Off the UI goroutine: closing a serial port whose adapter was
		// unplugged can block in the driver, and doing it inline freezes
		// the window instead of closing it.
		go h.disconnect()
	})
	w.ShowAndRun()
}

// host owns the window and the one live session. It is the small piece the
// form deliberately does not contain: the form edits a node, the host decides
// what happens to it.
type host struct {
	app  fyne.App
	win  fyne.Window
	base ui.Settings

	node        sessions.Node
	creds       []string
	defaultCred string
	lookup      sessiondial.Lookup

	form *ui.SessionForm
	sess *ui.Session
}

func (h *host) showForm() {
	h.form = ui.NewSessionForm(ui.SessionFormOptions{
		Node:              h.node,
		Credentials:       h.creds,
		DefaultCredential: h.defaultCred,
		VaultLocked:       h.lookup == nil,
		ListSerialPorts:   listPorts,
		ShowSave:          true,
		ShowConnect:       true,

		OnCancel: func() { h.win.Close() },

		// The stub. Printing the marshalled node is more useful than a
		// dialog saying "saved": it is exactly what a session file would
		// contain, so anything missing from it is missing from disk.
		OnSave: func(n sessions.Node) {
			h.node = n
			data, err := sessions.Marshal(n)
			if err != nil {
				dialog.ShowError(err, h.win)
				return
			}
			fmt.Fprintf(os.Stderr, "--- save (stub): %s ---\n%s---\n", n.Label(), data)
			h.form.SetStatus("Save is a stub — the YAML that would be written is on stderr.")
		},

		OnConnect: func(n sessions.Node) {
			h.node = n
			h.connect(n)
		},
	})

	h.win.SetContent(h.form.Content())
	h.win.SetTitle("pfconnect")
}

func (h *host) connect(n sessions.Node) {
	h.form.SetBusy(true)
	h.form.SetStatus("Connecting to " + n.Target() + " …")

	opts := sessiondial.Options{
		Credentials:   h.lookup,
		HostKeyPrompt: h.promptHostKey,
		OnNewHostKey: func(host, keyType, fingerprint string) {
			log.Printf("[hostkey] trusted on first contact: %s %s %s", host, keyType, fingerprint)
		},
		AuthPrompt: h.promptSecret,
		Log:        log.Printf,
	}

	// Dial off the UI goroutine. A device that is slow to answer, or a
	// host-key prompt waiting on a click, must not freeze the window --
	// and the prompt below cannot be answered by a frozen one.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		tp, err := sessiondial.Connect(ctx, n, opts)
		if err != nil {
			log.Printf("[dial] %v", err)
			fyne.Do(func() {
				h.form.SetBusy(false)
				h.form.SetStatus("⚠  " + err.Error())
			})
			return
		}
		fyne.Do(func() { h.attach(n, tp) })
	}()
}

// attach swaps the window over to a terminal. Runs on the UI goroutine.
func (h *host) attach(n sessions.Node, tp term.Transport) {
	// Settings BEFORE the widget. widget.TextGrid renders at the
	// application theme's text size, so a per-session font size only
	// reaches the glyphs through the theme override ui.Themed installs --
	// which reads the settings that are current when it is called. Doing
	// this after construction reflows the remote and changes nothing on
	// screen.
	//
	// Safe here because there is exactly one session and it is not running
	// yet; with tabs this becomes ui.Themed taking an explicit size.
	ui.SetSettings(ui.SettingsFor(h.base, n))

	sess := ui.NewSession()
	// Before Attach: anti-idle is read when the transport is attached, so
	// setting it afterwards silently does nothing until a reconnect.
	ui.ApplySession(sess, n)

	sess.SetStateChangeHandler(func(st ui.ConnectionState) {
		log.Printf("[state] %s", st)
		h.win.SetTitle("pfconnect — " + n.Label() + " — " + st.String())
	})
	sess.SetErrorHandler(func(err error) { log.Printf("[error] %v", err) })
	sess.SetReconnectRequestHandler(func() {
		log.Printf("[reconnect] input arrived on a dead session")
	})

	if err := sess.Attach(tp); err != nil {
		log.Printf("[attach] %v", err)
		tp.Close()
		h.form.SetBusy(false)
		h.form.SetStatus("⚠  attach: " + err.Error())
		return
	}
	h.sess = sess

	back := widget.NewButton("Back to settings", func() {
		go func() {
			h.disconnect()
			fyne.Do(func() {
				// Restore the application-level settings before the
				// form is rebuilt, so the next session starts from the
				// app's values rather than the last session's.
				ui.SetSettings(h.base)
				h.showForm()
				h.form.SetNode(h.node)
			})
		}()
	})
	bar := container.NewHBox(back, widget.NewLabel(n.Label()+"  "+n.Target()))

	// ui.Themed is what makes the grid render at the configured size.
	// Without it the widget's arithmetic uses Settings.FontSize while the
	// glyphs render at the application theme's, and the two disagree.
	h.win.SetContent(container.NewBorder(bar, nil, nil, nil, ui.Themed(sess)))
	h.win.Canvas().Focus(sess)
	log.Printf("[attach] ok")
}

func (h *host) disconnect() {
	if h.sess == nil {
		return
	}
	s := h.sess
	h.sess = nil
	if err := s.Close(); err != nil {
		log.Printf("[close] %v", err)
	}
}

// promptHostKey asks in the GUI rather than on stderr: by the time this fires
// there is a window on screen and nobody is watching the terminal it was
// launched from. Called on the dial goroutine, so it hops to the UI goroutine
// to show the dialog and blocks on the answer.
func (h *host) promptHostKey(hostname string, remote net.Addr, key ssh.PublicKey) (bool, error) {
	answer := make(chan bool, 1)
	msg := fmt.Sprintf("%s (%s)\n\n%s\n%s\n\nAccept and remember this key?",
		hostname, remote, key.Type(), ssh.FingerprintSHA256(key))
	fyne.Do(func() {
		dialog.ShowConfirm("Unknown host key", msg, func(ok bool) { answer <- ok }, h.win)
	})
	select {
	case ok := <-answer:
		return ok, nil
	case <-time.After(60 * time.Second):
		// A prompt nobody answers must resolve to no. Timing out into
		// "yes" would make the policy meaningless in exactly the case
		// where it matters.
		return false, fmt.Errorf("host key prompt timed out")
	}
}

// promptSecret answers password and keyboard-interactive challenges the node
// did not supply material for.
func (h *host) promptSecret(prompt string, echo bool) (string, error) {
	answer := make(chan string, 1)
	fyne.Do(func() {
		field := widget.NewPasswordEntry()
		if echo {
			field = widget.NewEntry()
		}
		field.SetPlaceHolder(strings.TrimSpace(prompt))
		d := dialog.NewForm("Authentication", "Send", "Cancel",
			[]*widget.FormItem{widget.NewFormItem(strings.TrimSpace(prompt), field)},
			func(ok bool) {
				if ok {
					answer <- field.Text
					return
				}
				answer <- ""
			}, h.win)
		d.Resize(fyne.NewSize(420, 200))
		d.Show()
	})
	select {
	case s := <-answer:
		return s, nil
	case <-time.After(120 * time.Second):
		return "", fmt.Errorf("authentication prompt timed out")
	}
}

// openVault unlocks the vault and returns the picker's names plus a lookup.
//
// A vault that will not open is not fatal: the form still works, the picker
// says so, and a node that names a credential fails at connect time with a
// message about that credential. Refusing to start would be a worse trade -- a
// serial console needs no vault at all.
func openVault(path string) ([]string, string, sessiondial.Lookup) {
	if strings.TrimSpace(path) == "" {
		return nil, "", nil
	}
	v, err := vaultcli.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vault %s: %v (continuing without it)\n", path, err)
		return nil, "", nil
	}
	names := v.Names()
	log.Printf("[vault] %s unlocked, %d credential(s)", v.Path(), len(names))

	return names, v.DefaultName(), func(ref string) (sessiondial.Credential, error) {
		// An EMPTY ref asks what this store uses when a session names
		// nothing, and is not a lookup failure -- see the same case in
		// cmd/pathfinder.
		if strings.TrimSpace(ref) == "" {
			c, ok := v.Default()
			if !ok {
				return sessiondial.Credential{}, nil
			}
			return dialCredential(c), nil
		}
		// Get accepts an id or a name, which is what lets a session file
		// store the portable one and still resolve a node written by
		// something that stored the other.
		c, err := v.Get(ref)
		if err != nil {
			return sessiondial.Credential{}, err
		}
		return dialCredential(c), nil
	}
}

// dialCredential is the ONE vault-to-dial conversion, so the named path and
// the default path cannot drift.
func dialCredential(c vault.Credential) sessiondial.Credential {
	return sessiondial.Credential{
		Username:      c.Username,
		AuthType:      authTypeName(c),
		Password:      c.Password,
		KeyPath:       c.KeyPath,
		KeyPassphrase: c.KeyPassphrase,
	}
}

// authTypeName maps the vault's auth enum onto the session model's spelling.
// Two vocabularies for the same three values is a wart, but translating in one
// function is better than making either store adopt the other's constants.
func authTypeName(c vault.Credential) string {
	switch c.Method() {
	case vault.AuthPublicKey:
		return sessions.AuthPublicKey
	case vault.AuthPassword:
		return sessions.AuthPassword
	default:
		return ""
	}
}

// listPorts feeds the serial dropdown. On macOS prefer a /dev/cu.* entry over
// the matching /dev/tty.*: the tty device blocks on open until carrier is
// asserted, which a console cable with no modem control never does, so the app
// appears to hang.
func listPorts() []string {
	if ports, err := serialx.ListDetailed(); err == nil {
		out := make([]string, 0, len(ports))
		for _, p := range ports {
			out = append(out, p.Name)
		}
		return out
	}
	names, err := serialx.List()
	if err != nil {
		log.Printf("[serial] listing ports: %v", err)
		return nil
	}
	return names
}
