// cmd/captureui/main.go
//
// A window with the capture view in it. Three modes:
//
//	go run ./cmd/captureui -demo                        scripted run, no lab
//	go run ./cmd/captureui -store ~/captures            browse only, no run
//	go run ./cmd/captureui -device-file devices.txt -store ~/captures \
//	    -vault ~/.pathfinderssh/vault.json -domain lab.local --legacy
//
// Note the ./ — "go run cmd/captureui" without it is read as a standard
// library path and fails with a confusing message about cmd/ not being in std.
//
// # Browse-only is a first-class mode
//
// Once captures are scheduled, the common reason to open this window is to
// read what was already captured, not to capture. So -store alone is enough:
// no devices, no credentials, no vault unlock, and the Store tab works.
//
// # Ordering
//
// app.New() comes FIRST, before any widget is constructed. Fyne resolves theme
// and driver through the current app, so building a widget beforehand is a nil
// dereference deep inside Button.CreateRenderer, which names nothing useful.
// The redraw loop is released by Start() immediately before ShowAndRun.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/scottpeterman/pathfinderssh/internal/capture"
	"github.com/scottpeterman/pathfinderssh/internal/capturedial"
	"github.com/scottpeterman/pathfinderssh/internal/capturerun"
	"github.com/scottpeterman/pathfinderssh/internal/ui"
)

func main() {
	var (
		demo       = flag.Bool("demo", false, "play a scripted run instead of capturing")
		demoStep   = flag.Duration("demo-step", 40*time.Millisecond, "pause between scripted events")
		devices    = flag.String("device", "", "device(s) to capture, comma-separated")
		deviceFile = flag.String("device-file", "", "file of devices, one per line, # for comments")
		typeList   = flag.String("type", "", "capture type(s), comma-separated; default running-config")
		storeRoot  = flag.String("store", "", "capture store root directory")
		domains    = flag.String("domain", "", "domain suffix(es), comma-separated")
		vaultPath  = flag.String("vault", "", "credential vault path")
		bindings   = flag.String("bindings", "", "credential binding store path (default: alongside the vault)")
		user       = flag.String("user", "", "username, when no vault is given")
		pass       = flag.String("password", "", "password, when no vault is given")
		keyPath    = flag.String("key", "", "private key path, when no vault is given")
		credTags   = flag.String("cred-tag", "", "only offer credentials carrying these tags")
		tofu       = flag.Bool("tofu", false, "trust unknown host keys on first contact; a CHANGED key still fails closed")
		knownHosts = flag.String("known-hosts", "", "known_hosts path (default ~/.ssh/known_hosts)")
		conc       = flag.Int("concurrency", 5, "devices visited at once")
		expConc    = flag.Int("expensive-concurrency", 1, "concurrent expensive commands across the run")
		timeout    = flag.Duration("timeout", 60*time.Second, "default per-command timeout")
		legacy     = flag.Bool("legacy", false, "enable legacy KEX and ciphers")
		verbose    = flag.Bool("v", false, "log capture progress to stderr")
	)
	flag.Parse()

	// The app must exist before any widget is built.
	a := app.New()
	// Chrome is an explicit light/dark setting, not the OS variant.
	ui.ApplyAppTheme(a, ui.CurrentSettings().AppVariant())
	w := a.NewWindow("PathfinderSSH — capture")
	w.Resize(fyne.NewSize(1240, 800))

	// The browser is opened before the engine and independently of it, so
	// the Store tab works when there is nothing to capture. Failing to open
	// it is not fatal: the Run tab does not need it.
	var browser capture.Browser
	if *storeRoot != "" {
		if fs, err := capture.OpenFileStore(*storeRoot); err == nil {
			browser = fs
		} else {
			fmt.Fprintf(os.Stderr, "captureui: %v\n", err)
		}
	}

	run := capturerun.New()
	view := ui.NewCaptureView(run, browser)
	defer view.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Ctrl-C should behave like the Stop button, not like a kill.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		cancel()
	}()

	var stopBtn *widget.Button
	stopBtn = widget.NewButton("Stop", func() {
		cancel()
		stopBtn.SetText("Stopping…")
		stopBtn.Disable()
	})
	status := widget.NewLabel("")
	bar := container.NewHBox(stopBtn, status)
	w.SetContent(container.NewBorder(nil, bar, nil, nil, view.Content()))

	go func() {
		defer func() {
			run.Finish()
			fyne.Do(func() {
				stopBtn.SetText("Done")
				stopBtn.Disable()
				c := run.Counts()
				status.SetText(fmt.Sprintf(
					"%d stored · %d unchanged · %d not applicable · %d failed · %d new host key(s)",
					c.Stored, c.Unchanged, c.NotApplicable, c.Failed, c.NewHostKeys))
			})
		}()

		if *demo {
			capturerun.Demo(run, capturerun.DemoOptions{Step: *demoStep, Stop: ctx.Done()})
			return
		}

		// Start from Defaults rather than a bare struct, for the same
		// reason cmd/crawlui does: a field that gains a non-zero
		// default later would otherwise be silently zero here and
		// nowhere else, and the two front ends would drift with no
		// error to show for it.
		p := capturerun.Defaults()
		p.Devices = capturerun.ParseDevices(*devices)
		p.DeviceFile = *deviceFile
		p.Types = capturerun.ParseDevices(*typeList)
		p.Domains = capturerun.ParseDevices(*domains)
		p.StorePath = *storeRoot
		p.VaultPath = *vaultPath
		p.CredTags = capturerun.ParseDevices(*credTags)
		p.KnownHostsPath = *knownHosts
		p.Concurrency = *conc
		p.ExpensiveConcurrency = *expConc
		p.Timeout = *timeout
		p.Legacy = *legacy
		// Strict is the default and TOFU is the opt-in, which is the
		// opposite of crawl. A crawl meets devices it has never seen; a
		// capture works from a list of devices someone already
		// administers.
		if *tofu {
			p.HostKeys = capturerun.HostKeyTOFU
		}

		// Browse-only. Not an error and not a warning: opening the
		// window against a store to read last night's config is the
		// expected use once captures are scheduled.
		if len(p.Devices) == 0 && p.DeviceFile == "" {
			msg := "no devices; browsing the store"
			if browser == nil {
				msg = "no devices and no store; try -demo, or -store to browse"
			}
			fyne.Do(func() { status.SetText(msg) })
			return
		}

		logf := func(string, ...any) {}
		if *verbose {
			logf = func(format string, args ...any) {
				fmt.Fprintf(os.Stderr, format+"\n", args...)
			}
		}

		built, err := capturedial.Build(p, capturedial.Options{
			Static: capturedial.StaticCreds{
				Username: *user, Password: *pass, KeyPath: *keyPath,
			},
			BindingsPath: *bindings,
			Log:          logf,
			CredLog:      logf,
			Emit:         run.Emit(),
		})
		if err != nil {
			fyne.Do(func() { status.SetText(err.Error()) })
			return
		}
		defer built.Close()

		// What is about to happen, before it happens. A capture is the
		// case where being told afterwards is too late — and the CGNAT
		// notes in particular are decisions, not trivia.
		plan := fmt.Sprintf("%d device(s) x %d type(s) -> %s",
			len(built.Devices), len(built.Specs), p.StorePath)
		if len(built.Notes) > 0 {
			var notes []string
			for id, n := range built.Notes {
				notes = append(notes, id+": "+n)
			}
			plan += "  ·  " + strings.Join(notes, "; ")
		}
		fyne.Do(func() { status.SetText(plan) })

		built.Engine.Capture(ctx, built.Devices)
	}()

	// Releases the redraw loop; nothing may call fyne.Do before this.
	view.Start()
	w.ShowAndRun()
}
