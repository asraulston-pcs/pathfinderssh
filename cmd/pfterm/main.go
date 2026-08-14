// cmd/pfterm/main.go
// Manual smoke harness for the terminal widget.
//
// This is not an automated test and is not meant to become one. It exists to
// answer the only question the unit tests cannot: does a real session, over a
// real transport, actually render and accept input. Point it at a lab device
// and drive it by hand.
//
//	go run ./cmd/pfterm -ssh admin@lab-sw1
//	go run ./cmd/pfterm -ssh admin@100.64.12.9 -legacy
//	go run ./cmd/pfterm -ports                       # what is plugged in
//	go run ./cmd/pfterm -serial /dev/cu.usbserial-210 -baud 9600
//	go run ./cmd/pfterm -telnet lab-console1.lab.example:2001
//	go run ./cmd/pfterm -themes                      # terminal palettes
//	go run ./cmd/pfterm -ssh admin@lab-sw1 -app light -terminal-theme cyber
//
// What to actually check once it is up, because these are the things that
// broke during the port and none of them fail loudly:
//
//   - a wide "show" output wraps at the right column, and resizing the window
//     reflows it (the Resize path reaching the far end)
//   - scrolling back with the mouse wheel reaches the true top, and the
//     scrollbar thumb tracks it (the viewport/scrollOffset mapping)
//   - a curses program -- btop, top, vi -- paints, and quitting it restores
//     the prior screen (alternate-screen handling)
//   - CJK or box-drawing characters occupy the right number of columns
//     (runewidth, the dependency deliberately pinned at v0.0.16)
//   - Ctrl+C interrupts, Ctrl+D closes, and the window reports the close
//     rather than hanging (Done/Err reaching the UI)
//   - pulling the console cable, or killing the session server-side, produces
//     a reported error and not silence (watchDone's non-nil path)
//   - -app and -terminal-theme are genuinely independent: the shipped default
//     is dark chrome around the light "ice" palette, so anything that derived
//     one from the other shows up here immediately
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"

	"golang.org/x/crypto/ssh"
	xterm "golang.org/x/term"

	"github.com/scottpeterman/pathfinderssh/internal/serialx"
	"github.com/scottpeterman/pathfinderssh/internal/sshcore"
	"github.com/scottpeterman/pathfinderssh/internal/telnetx"
	"github.com/scottpeterman/pathfinderssh/internal/term"
	"github.com/scottpeterman/pathfinderssh/internal/ui"
)

func main() {
	var (
		sshTarget = flag.String("ssh", "", "user@host[:port]")
		serialDev = flag.String("serial", "", "serial port, e.g. /dev/tty.usbserial-A900 or COM3")
		telnetTgt = flag.String("telnet", "", "host[:port] for a telnet session (default port 23)")
		baud      = flag.Int("baud", 9600, "serial baud rate")
		dataBits  = flag.Int("databits", 8, "serial data bits (5-8)")
		parity    = flag.String("parity", "none", "serial parity: none|odd|even|mark|space")
		stopBits  = flag.String("stopbits", "1", "serial stop bits: 1|1.5|2")
		keyPath   = flag.String("key", "", "private key file; empty means agent, then password prompt")
		legacy    = flag.Bool("legacy", false, "allow legacy KEX/cipher/MAC (old lab gear)")
		insecure  = flag.Bool("insecure", false, "skip host-key verification entirely (disposable lab gear only)")
		appTheme  = flag.String("app", "dark", "application chrome: dark|light")
		termTheme = flag.String("terminal-theme", "", "terminal palette name (default "+ui.DefaultTerminalTheme+")")
		listThm   = flag.Bool("themes", false, "list terminal palettes and exit")
		rowOff    = flag.Int("rowoffset", 0, "rows to subtract from the computed grid; non-zero is evidence of a measurement bug, not configuration")
		colOff    = flag.Int("coloffset", 0, "columns to subtract from the computed grid")
		fontSize  = flag.Int("font", 12, "terminal font size in points")
		logSess   = flag.Bool("log", false, "write a session transcript")
		listPorts = flag.Bool("ports", false, "list serial ports and exit")
	)
	flag.Parse()

	if *listThm {
		printThemes()
		return
	}

	if *listPorts {
		printPorts()
		return
	}

	chosen := 0
	for _, f := range []string{*sshTarget, *serialDev, *telnetTgt} {
		if f != "" {
			chosen++
		}
	}
	if chosen != 1 {
		fmt.Fprintln(os.Stderr, "give exactly one of -ssh, -serial or -telnet")
		flag.Usage()
		os.Exit(2)
	}

	// Widget logging goes to stderr with timestamps; the widget is chatty and
	// that chatter is most of the diagnostic value here.
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	// Grid sizing is the thing most likely to need dialling in, so it is on
	// flags rather than in a settings file: compare the widget's computed
	// rows/cols against `stty size` in the session and adjust without a
	// rebuild.
	cfg := ui.Defaults()
	cfg.RowOffset = *rowOff
	cfg.ColOffset = *colOff
	cfg.FontSize = *fontSize
	cfg.AppTheme = ui.AppVariant(*appTheme)
	if *termTheme != "" {
		cfg.TerminalTheme = *termTheme
	}
	ui.SetSettings(cfg)

	a := app.New()
	// Without this the chrome follows the OS, which is the behaviour the
	// two-value setting exists to replace.
	ui.ApplyAppTheme(a, cfg.AppVariant())
	w := a.NewWindow("pfterm")
	w.Resize(fyne.NewSize(1100, 700))

	sess := ui.NewSession()
	sess.SetSessionLogEnabled(*logSess)
	switch {
	case *sshTarget != "":
		sess.SetName(*sshTarget)
	case *telnetTgt != "":
		sess.SetName(*telnetTgt)
	default:
		sess.SetName(*serialDev)
	}

	sess.SetStateChangeHandler(func(st ui.ConnectionState) {
		log.Printf("[state] %s", st)
		w.SetTitle("pfterm - " + st.String())
	})
	sess.SetErrorHandler(func(err error) {
		// The point of the harness: a session that dies must say so.
		log.Printf("[error] %v", err)
	})
	sess.SetReconnectRequestHandler(func() {
		log.Printf("[reconnect] input arrived on a dead session")
	})

	// ui.Themed is what makes the grid actually render at -font. Without it the
	// widget's arithmetic uses the configured size while the glyphs render at
	// the application theme's, so changing -font reflows the remote without
	// changing anything on screen.
	w.SetContent(ui.Themed(sess))
	w.Canvas().Focus(sess)

	// Dial off the UI goroutine: a device that is slow to answer, or a
	// password prompt on the controlling terminal, must not freeze the window.
	go func() {
		tp, err := dial(dialOpts{
			ssh:      *sshTarget,
			serial:   *serialDev,
			telnet:   *telnetTgt,
			baud:     *baud,
			dataBits: *dataBits,
			parity:   *parity,
			stopBits: *stopBits,
			keyPath:  *keyPath,
			legacy:   *legacy,
			insecure: *insecure,
		})
		if err != nil {
			log.Printf("[dial] %v", err)
			fyne.Do(func() { w.SetTitle("pfterm - dial failed") })
			return
		}
		fyne.Do(func() {
			if err := sess.Attach(tp); err != nil {
				log.Printf("[attach] %v", err)
				return
			}
			log.Printf("[attach] ok")
		})
	}()

	// Off the UI goroutine. Fyne calls SetOnClosed on the goroutine that runs
	// the event loop, and closing a transport whose device has gone away can
	// block in the driver -- a serial port whose adapter was unplugged, or
	// whose far end was powered off, is the case that actually happens. Doing
	// it inline freezes the window instead of closing it.
	w.SetOnClosed(func() {
		go func() {
			if err := sess.Close(); err != nil {
				log.Printf("[close] %v", err)
			}
		}()
	})

	w.ShowAndRun()
}

// printPorts lists the serial ports the OS reports, with USB metadata where it
// has any. On macOS prefer a /dev/cu.* entry over the matching /dev/tty.*: the
// tty device blocks on open until carrier is asserted, which a console cable
// with no modem control never does, so the app appears to hang. cu is the
// callout device and does not wait.
func printPorts() {
	ports, err := serialx.ListDetailed()
	if err != nil {
		// The detailed enumerator can fail where the bare list still works.
		names, lerr := serialx.List()
		if lerr != nil {
			fmt.Fprintf(os.Stderr, "listing ports: %v\n", err)
			return
		}
		for _, n := range names {
			fmt.Println(n)
		}
		return
	}
	if len(ports) == 0 {
		fmt.Println("no serial ports found")
		return
	}
	for _, p := range ports {
		if p.IsUSB {
			fmt.Printf("%-32s USB %s:%s %s\n", p.Name, p.VID, p.PID, p.SerialNumber)
		} else {
			fmt.Println(p.Name)
		}
	}
}

// dialOpts is what the flags amount to. A struct rather than seven positional
// parameters, which is how the wrong one ends up in the wrong slot.
type dialOpts struct {
	ssh      string
	serial   string
	telnet   string
	baud     int
	dataBits int
	parity   string
	stopBits string
	keyPath  string
	legacy   bool
	insecure bool
}

// dial builds whichever transport was asked for. All three return a
// term.Transport, which is the entire reason the widget does not care which
// one it gets -- and the reason this function is the only place in the program
// that knows there is more than one kind.
func dial(o dialOpts) (term.Transport, error) {
	switch {
	case o.serial != "":
		return dialSerial(o)
	case o.telnet != "":
		return dialTelnet(o)
	default:
		return dialSSH(o)
	}
}

func dialSerial(o dialOpts) (term.Transport, error) {
	cfg := serialx.Config{
		Port:     o.serial,
		Baud:     o.baud,
		DataBits: o.dataBits,
		Parity:   o.parity,
		StopBits: o.stopBits,
		// Block until a byte arrives rather than polling. A console is idle
		// most of the time, and a positive timeout makes every expiry a read
		// that returned nothing -- which the read loop then has to pace around.
		ReadTimeout: 0,
	}
	b := serialx.New(cfg)
	if err := b.Connect(); err != nil {
		return nil, err
	}
	// Framing is worth logging in full: 8N1 is almost always right, and the
	// times it is not are exactly the times nobody remembers what was set.
	log.Printf("[serial] %s %d %d%s%s", o.serial, o.baud, o.dataBits,
		strings.ToUpper(o.parity[:1]), o.stopBits)
	return b, nil
}

// dialTelnet opens a plaintext session. There is no auth or host-key step: the
// device's login prompt, if it has one, arrives as ordinary data.
func dialTelnet(o dialOpts) (term.Transport, error) {
	host, port := o.telnet, 23
	if h, p, err := net.SplitHostPort(o.telnet); err == nil {
		n, convErr := strconv.Atoi(p)
		if convErr != nil {
			return nil, fmt.Errorf("bad port in %q", o.telnet)
		}
		host, port = h, n
	}

	host, err := resolveTarget(host)
	if err != nil {
		return nil, err
	}

	b := telnetx.New(telnetx.Config{Host: host, Port: port})
	if err := b.Connect(); err != nil {
		return nil, err
	}
	log.Printf("[telnet] %s:%d (plaintext)", host, port)
	return b, nil
}

func dialSSH(o dialOpts) (term.Transport, error) {
	sshTarget, keyPath, legacy, insecure := o.ssh, o.keyPath, o.legacy, o.insecure

	user, host, port, err := splitTarget(sshTarget)
	if err != nil {
		return nil, err
	}

	host, err = resolveTarget(host)
	if err != nil {
		return nil, err
	}

	cfg := sshcore.Config{
		Host:             host,
		Port:             port,
		Username:         user,
		PrivateKeyPath:   keyPath,
		UseAgent:         keyPath == "",
		Timeout:          20 * time.Second,
		LegacyAlgorithms: legacy,
		HostKeys:         sshcore.HostKeyTOFU,
		HostKeyPrompt:    promptHostKey,
		AuthPrompt:       promptSecret,
	}
	if insecure {
		// Deliberately available: verifying a throwaway lab device's key is
		// noise, and forcing it just teaches people to answer yes on reflex.
		cfg.HostKeys = sshcore.HostKeyInsecure
		cfg.HostKeyPrompt = nil
		log.Printf("[ssh] host-key verification disabled")
	}

	c, err := sshcore.Dial(cfg)
	if err != nil {
		return nil, err
	}

	s, err := term.Open(c, term.Options{
		Term: "xterm-256color",
		// OwnsClient: this client was dialed for this session and nothing else
		// holds it, so closing the window should take the connection with it.
		OwnsClient: true,
	})
	if err != nil {
		c.Close()
		return nil, err
	}
	for _, e := range s.EnvErrors {
		log.Printf("[ssh] env rejected: %v", e)
	}
	log.Printf("[ssh] %s@%s:%d", user, host, port)
	return s, nil
}

// cgnat is the shared address space from RFC 6598. An address in this range is
// carrier-side NAT, not a stable identity: the same address reaches different
// equipment depending on where you are standing. Resolve it to a name and use
// that, so the session is pinned to a device rather than to an address that may
// be pointed somewhere else tomorrow.
var cgnat = mustCIDR("100.64.0.0/10")

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

func resolveTarget(host string) (string, error) {
	ip := net.ParseIP(host)
	if ip == nil || !cgnat.Contains(ip) {
		return host, nil
	}

	names, err := net.LookupAddr(host)
	if err != nil || len(names) == 0 {
		log.Printf("[cgnat] %s is in 100.64.0.0/10 and does not resolve; using the address as given", host)
		return host, nil
	}
	name := strings.TrimSuffix(names[0], ".")
	log.Printf("[cgnat] %s is in 100.64.0.0/10, resolved to %s", host, name)
	return name, nil
}

// splitTarget parses user@host[:port].
func splitTarget(s string) (user, host string, port int, err error) {
	at := strings.LastIndex(s, "@")
	if at < 1 || at == len(s)-1 {
		return "", "", 0, fmt.Errorf("expected user@host[:port], got %q", s)
	}
	user, host = s[:at], s[at+1:]

	if h, p, splitErr := net.SplitHostPort(host); splitErr == nil {
		n, convErr := strconv.Atoi(p)
		if convErr != nil {
			return "", "", 0, fmt.Errorf("bad port in %q", s)
		}
		return user, h, n, nil
	}
	return user, host, 22, nil
}

// promptHostKey is the TOFU prompt. It reads the controlling terminal, not the
// GUI, because the GUI has nothing on screen yet at this point.
func promptHostKey(hostname string, remote net.Addr, key ssh.PublicKey) (bool, error) {
	fmt.Fprintf(os.Stderr, "unknown host %s (%s)\n  %s %s\naccept and remember? [y/N] ",
		hostname, remote, key.Type(), ssh.FingerprintSHA256(key))
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(line), "y"), nil
}

// promptSecret serves password and keyboard-interactive prompts.
func promptSecret(prompt string, echo bool) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	if echo {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		return strings.TrimSpace(line), err
	}
	b, err := xterm.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(b), err
}

// printThemes lists the registered terminal palettes. Terminal palettes are a
// library; the application chrome is only -app dark|light.
func printThemes() {
	labels, labelToKey, _ := ui.ThemeMenuData()
	if len(labels) == 0 {
		fmt.Println("no terminal palettes registered")
		return
	}
	fmt.Printf("%d terminal palette(s); default %q\n", len(labels), ui.DefaultTerminalTheme)
	for _, label := range labels {
		name := labelToKey[label]
		kind := "dark"
		if !ui.GetThemeDef(name).IsDark() {
			kind = "light"
		}
		fmt.Printf("  %-24s %-5s %s\n", name, kind, label)
	}
}
