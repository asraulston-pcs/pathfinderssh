// cmd/crawlui/main.go
//
// A window with the crawl view in it. Two modes:
//
//	go run ./cmd/crawlui -demo                       scripted run, no lab needed
//	go run ./cmd/crawlui -seed 172.16.1.2 -vault ~/.pathfinderssh/vault.json \
//	    -domain lab.local -save-run ~/.pathfinderssh/last-run.json -v
//
// Note the ./ — "go run cmd/crawlui" without it is read as a standard library
// path and fails with a confusing message about cmd/ not being in std.
//
// # Ordering
//
// app.New() comes FIRST, before any widget is constructed. Fyne resolves theme
// and driver through the current app, so building a widget beforehand is a nil
// dereference deep inside Button.CreateRenderer, which names nothing useful.
// The view is created after the app, and its redraw loop is released by
// Start() immediately before ShowAndRun — the loop calls fyne.Do, and there is
// no driver to hand work to until the app is actually running.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/scottpeterman/pathfinderssh/internal/crawldial"
	"github.com/scottpeterman/pathfinderssh/internal/crawler"
	"github.com/scottpeterman/pathfinderssh/internal/crawlrun"
	"github.com/scottpeterman/pathfinderssh/internal/topo"
	"github.com/scottpeterman/pathfinderssh/internal/ui"
)

func main() {
	var (
		demo     = flag.Bool("demo", false, "play a scripted run instead of crawling")
		demoStep = flag.Duration("demo-step", 40*time.Millisecond, "pause between scripted events")
		seeds    = flag.String("seed", "", "seed device(s), comma-separated")
		vaultDB  = flag.String("vault", "", "credential vault path; without it -user/-password are used")
		user     = flag.String("user", "", "username, when no vault is given")
		pass     = flag.String("password", "", "password, when no vault is given")
		keyPath  = flag.String("key", "", "private key path, when no vault is given")
		credTags = flag.String("cred-tag", "", "only offer credentials carrying these tags")
		domains  = flag.String("domain", "", "domain suffix(es), comma-separated")
		allowDom = flag.String("allow-domain", "", "only dial neighbors under these suffixes")
		exclude  = flag.String("exclude", "", "exclusion substring(s), comma-separated")
		depth    = flag.Int("depth", 3, "maximum crawl depth")
		conc     = flag.Int("c", 5, "concurrent devices")
		timeout  = flag.Duration("timeout", 30*time.Second, "per-command timeout")
		strictHK = flag.Bool("strict-hostkey", false, "require keys already in known_hosts")
		knownHK  = flag.String("known-hosts", "", "known_hosts path for discovered keys")
		legacy   = flag.Bool("legacy", false, "enable legacy KEX and ciphers")
		trustUni = flag.Bool("trust-unidirectional", false,
			"accept one-sided link claims, so devices that were never crawled "+
				"still appear as leaves on their neighbor's word")
		outPath = flag.String("o", "", "write the topology map here")
		runPath = flag.String("save-run", "", "save this run for the next comparison")
		lastRun = flag.String("last-run", "", "path to a previous run snapshot for the comparison tab")
		verbose = flag.Bool("v", false, "log crawl progress to stderr")
	)
	flag.Parse()

	// The app must exist before any widget is built.
	a := app.New()
	// Chrome is an explicit light/dark setting, not the OS variant.
	ui.ApplyAppTheme(a, ui.CurrentSettings().AppVariant())
	w := a.NewWindow("PathfinderSSH — crawl")
	w.Resize(fyne.NewSize(1200, 760))

	run := crawlrun.New()
	view := ui.NewCrawlView(run)
	defer view.Stop()

	// Clicking a row is the loop the shell exists to close: a device in a
	// crawl result is a device you can open a session to without retyping it.
	view.OnInspect = func(d crawlrun.DeviceRow) {
		fmt.Printf("inspect %s (%s)\n", d.Display(), d.State)
	}
	view.OnConnect = func(d crawlrun.DeviceRow) {
		fmt.Printf("connect %s\n", d.Display())
	}

	switch {
	case *lastRun != "":
		if prev, err := crawlrun.LoadSnapshot(*lastRun); err == nil {
			view.Compare(prev)
		} else {
			fmt.Fprintf(os.Stderr, "crawlui: %v\n", err)
		}
	case *demo:
		view.Compare(crawlrun.DemoPrevious())
	}

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
					"%d reached · %d failed · %d not dialed · %d new host keys · %.2f tries/device",
					c.Reached, c.Failed, c.NotDialed, c.NewHostKeys, c.AttemptsPerReached()))
			})
		}()

		if *demo {
			crawlrun.Demo(run, crawlrun.DemoOptions{Step: *demoStep, Stop: ctx.Done()})
			return
		}

		p := crawlrun.Defaults()
		p.Seeds = crawlrun.ParseSeeds(*seeds)
		p.Depth = *depth
		p.Concurrency = *conc
		p.Timeout = *timeout
		p.Domains = crawlrun.ParseSeeds(*domains)
		p.AllowDomains = crawlrun.ParseSeeds(*allowDom)
		p.Exclude = crawlrun.ParseSeeds(*exclude)
		p.VaultPath = *vaultDB
		p.CredTags = crawlrun.ParseSeeds(*credTags)
		p.KnownHostsPath = *knownHK
		p.Legacy = *legacy
		p.TrustUnidirectional = *trustUni
		if *strictHK {
			p.HostKeys = crawlrun.HostKeyStrict
		}
		if len(p.Seeds) == 0 {
			fyne.Do(func() { status.SetText("no seeds; try -demo") })
			return
		}

		logf := crawler.Logf(nil)
		if *verbose {
			logf = func(format string, args ...any) {
				fmt.Fprintf(os.Stderr, format+"\n", args...)
			}
		}

		built, err := crawldial.Build(p, crawldial.Options{
			Static:  crawldial.StaticCreds{Username: *user, Password: *pass, KeyPath: *keyPath},
			Log:     logf,
			CredLog: logf,
			Emit:    run.Emit(),
		})
		if err != nil {
			fyne.Do(func() { status.SetText(err.Error()) })
			return
		}
		defer built.Close()

		devices := built.Crawler.CrawlContext(ctx, p.Seeds)

		// Same two writes cmd/crawl makes, and for the same reasons: the fold
		// is the only place SysName can reach the binding store, and the
		// snapshot is what makes the NEXT run's comparison tab real.
		crawldial.Fold(built.Bindings, devices, p.Domains, logf)

		if *outPath != "" {
			m := topo.Generate(devices, crawldial.MapOptions(p))
			if data, err := topo.MarshalMap(m); err == nil {
				if err := os.WriteFile(*outPath, data, 0o644); err != nil {
					fmt.Fprintf(os.Stderr, "crawlui: write %s: %v\n", *outPath, err)
				}
			}
		}
		if *runPath != "" {
			if err := run.Snapshot(p.Seeds, p.Domains).Save(*runPath); err != nil {
				fmt.Fprintf(os.Stderr, "crawlui: %v\n", err)
			}
		}
	}()

	// Releases the redraw loop; nothing may call fyne.Do before this.
	view.Start()
	w.ShowAndRun()
}
