// cmd/crawl/main.go
// crawl — SSH-only topology crawler on the sshcore + netexec + crawler
// stack. BFS from one or more seeds, fingerprint each device, collect and
// parse CDP/LLDP neighbors, and emit a topology map JSON compatible with
// the existing viewer/seed-artifact toolchain.
//
// Example (lab):
//
//	crawl -seed lab-r1.lab.example -user admin -depth 3 \
//	      -exclude "linux,idrac,poweredge" -o map.json
//
//	crawl -seed 10.20.0.5 -user admin -jump admin@lab-jump1.lab.example \
//	      -trust-unidirectional -o map.json
//
// With a vault, credentials are resolved per device instead of being fixed on
// the command line. The master password comes from PATHFINDER_VAULT_PASSWORD
// or a terminal prompt:
//
//	crawl -seed lab-r1.lab.example -vault ~/.pathfinder/vault.json \
//	      -cred-tag lab -depth 3 -o map.json
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/scottpeterman/pathfinderssh/internal/crawldial"
	"github.com/scottpeterman/pathfinderssh/internal/crawler"
	"github.com/scottpeterman/pathfinderssh/internal/crawlrun"
	"github.com/scottpeterman/pathfinderssh/internal/credres"
	"github.com/scottpeterman/pathfinderssh/internal/dial"
	"github.com/scottpeterman/pathfinderssh/internal/netexec"
	"github.com/scottpeterman/pathfinderssh/internal/sshcore"
	"github.com/scottpeterman/pathfinderssh/internal/topo"
	"github.com/scottpeterman/pathfinderssh/internal/vaultcli"
)

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func parseJump(spec, fallbackUser string) (*sshcore.JumpConfig, error) {
	j := &sshcore.JumpConfig{Username: fallbackUser}
	rest := spec
	if i := strings.IndexByte(rest, '@'); i >= 0 {
		j.Username = rest[:i]
		rest = rest[i+1:]
	}
	if host, portStr, err := net.SplitHostPort(rest); err == nil {
		p, perr := strconv.Atoi(portStr)
		if perr != nil {
			return nil, fmt.Errorf("jump port %q: %v", portStr, perr)
		}
		j.Host, j.Port = host, p
	} else {
		j.Host = rest
	}
	if j.Host == "" {
		return nil, fmt.Errorf("jump spec %q has no host", spec)
	}
	return j, nil
}

// expandCSV splits comma-separable repeatable flag values.
func expandCSV(in []string) []string {
	var out []string
	for _, s := range in {
		for _, p := range strings.Split(s, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func main() {
	var seeds, exclude, allowDomains, domains, credTags stringList
	flag.Var(&seeds, "seed", "seed device (repeatable or comma-separated)")
	flag.Var(&exclude, "exclude", "exclusion substring(s), comma-separable, matched vs platform/hostname/sysname")
	flag.Var(&allowDomains, "allow-domain", "only dial neighbors under these domain suffixes (repeatable); others map as leaves")
	flag.Var(&domains, "domain", "domain suffix (repeatable): stripped from node names in the map and appended when resolving bare neighbor names")
	user := flag.String("user", os.Getenv("USER"), "ssh username")
	keyPath := flag.String("key", "", "private key path")
	askPass := flag.Bool("password", false, "prompt for a password")
	jumpSpec := flag.String("jump", "", "jump host: [user@]host[:port]")
	jumpKey := flag.String("jump-key", "", "jump host private key path")
	legacy := flag.Bool("legacy", false, "enable legacy KEX/ciphers")
	insecure := flag.Bool("insecure-hostkey", false, "skip host key verification (lab only; off by default)")
	depth := flag.Int("depth", 3, "max crawl depth (0 = seeds only)")
	conc := flag.Int("concurrency", 5, "concurrent devices per depth")
	timeout := flag.Duration("timeout", 30*time.Second, "per-command timeout")
	trustUni := flag.Bool("trust-unidirectional", false, "accept one-sided link claims between discovered devices (legacy-parity)")
	vaultPath := flag.String("vault", "", "credential vault path; enables multi-credential resolution (overrides -user/-key/-password)")
	bindingsPath := flag.String("bindings", "", "credential binding store path (default: alongside the vault)")
	flag.Var(&credTags, "cred-tag", "only offer credentials carrying all of these tags (repeatable or comma-separated)")
	maxCreds := flag.Int("max-creds", 0, "cap credentials tried per device (0 = default, negative = unlimited)")
	credBreaker := flag.Int("cred-breaker", 0, "park a credential after this many distinct devices reject it (0 = default, negative = off)")
	knownHosts := flag.String("known-hosts", "", "known_hosts path for discovered keys (default ~/.ssh/known_hosts; use a dedicated file to keep discovery keys separate)")
	outPath := flag.String("o", "map.json", "output topology file")
	verbose := flag.Bool("v", false, "verbose progress")
	flag.Parse()

	var expanded []string
	for _, s := range seeds {
		for _, part := range strings.Split(s, ",") {
			if part = strings.TrimSpace(part); part != "" {
				expanded = append(expanded, part)
			}
		}
	}
	if len(expanded) == 0 {
		fmt.Fprintln(os.Stderr, "crawl: at least one -seed is required")
		os.Exit(2)
	}

	var password string
	if *askPass {
		fmt.Fprint(os.Stderr, "password: ")
		b, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "crawl: reading password: %v\n", err)
			os.Exit(1)
		}
		password = string(b)
	}

	policy := sshcore.HostKeyTOFU
	if *insecure {
		policy = sshcore.HostKeyInsecure
	}

	var jump *sshcore.JumpConfig
	if *jumpSpec != "" {
		j, err := parseJump(*jumpSpec, *user)
		if err != nil {
			fmt.Fprintf(os.Stderr, "crawl: %v\n", err)
			os.Exit(2)
		}
		j.PrivateKeyPath = *jumpKey
		jump = j
	}

	base := crawldial.BaseConfig{
		Announce:       dial.Stderr("crawl"),
		Legacy:         *legacy,
		HostKeys:       policy,
		Jump:           jump,
		KnownHostsPath: *knownHosts,
	}

	credLog := func(string, ...any) {}
	if *verbose {
		credLog = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}
	}

	// Two dial modes. Without a vault the flags supply one credential for the
	// whole crawl, which is unchanged behavior. With a vault, credres decides
	// per device and learns across the crawl.
	var (
		dial      crawler.DialFunc
		resolver  *credres.Resolver
		bindings  *credres.FileBindings
		credNames map[string]string
	)
	if *vaultPath == "" {
		dial = crawldial.StaticDialer(base, *user, password, *keyPath)
	} else {
		v, err := vaultcli.Open(*vaultPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "crawl: %v\n", err)
			os.Exit(1)
		}
		defer v.Lock()

		bp := *bindingsPath
		if bp == "" {
			bp = vaultcli.BindingsPath(*vaultPath)
		}
		bindings, err = credres.OpenFileBindings(bp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "crawl: %v\n", err)
			os.Exit(1)
		}

		resolver = credres.New(v, bindings, credres.Config{
			BreakerThreshold: *credBreaker,
			MaxPerHost:       *maxCreds,
			Log:              credLog,
		})
		dial = crawldial.NewVaultDialer(resolver, base, expandCSV(credTags), credLog)

		// Names for reporting only; never secret material.
		credNames = map[string]string{}
		if metas, err := v.List(); err == nil {
			for _, m := range metas {
				credNames[m.ID] = m.Name
			}
		}
		if flagWasSet("user") || flagWasSet("key") || flagWasSet("password") {
			fmt.Fprintln(os.Stderr, "crawl: -vault is set; -user/-key/-password are ignored")
		}
	}

	logf := crawler.Logf(nil)
	if *verbose {
		logf = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}
	}

	c := crawler.New(crawler.Config{
		Dial:            dial,
		MaxDepth:        *depth,
		Concurrency:     *conc,
		ExcludePatterns: exclude,
		AllowDomains:    expandCSV(allowDomains),
		Domains:         expandCSV(domains),
		SessionOpts:     netexec.Options{CommandTimeout: *timeout},
		Log:             logf,
	})

	devices := c.Crawl(expanded)

	crawldial.Fold(bindings, devices, expandCSV(domains), logf)

	if resolver != nil {
		reportCredentialStats(resolver, credNames)
	}

	// Through the shared helper, so the CLI and the window cannot generate
	// different maps from the same run.
	topoMap := topo.Generate(devices, crawldial.MapOptions(crawlrun.Params{
		Domains:             expandCSV(domains),
		TrustUnidirectional: *trustUni,
	}))
	data, err := topo.MarshalMap(topoMap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "crawl: marshal: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*outPath, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "crawl: write %s: %v\n", *outPath, err)
		os.Exit(1)
	}

	ok, failed := 0, 0
	for _, d := range devices {
		if d.Failed {
			failed++
		} else {
			ok++
		}
	}
	fmt.Fprintf(os.Stderr, "crawl: %d discovered, %d failed, %d nodes in %s\n",
		ok, failed, len(topoMap), *outPath)
	if ok == 0 {
		os.Exit(1)
	}
}
