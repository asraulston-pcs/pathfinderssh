// cmd/reach/main.go
// reach — minimal CLI consumer for the sshcore + netexec stack.
//
// Runs one or more commands against a device over an interactive PTY shell
// with prompt detection. Each -c flag (or trailing argument) is ONE command
// sent verbatim; there is no splitting of any kind.
//
// Example (lab):
//
//	reach -host lab-r1.lab.example -user admin -legacy \
//	      -paging "terminal length 0" \
//	      -c "show version" -c "show ip int brief"
//
//	reach -host 10.20.0.5 -user admin \
//	      -jump admin@lab-jump1.lab.example \
//	      -c "show run | include hostname"
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"github.com/scottpeterman/pathfinderssh/internal/netexec"
	"github.com/scottpeterman/pathfinderssh/internal/normalize"
	"github.com/scottpeterman/pathfinderssh/internal/sshcore"
)

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, "; ") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// normalizeTarget applies the CGNAT rule from normalize: an address in
// 100.64.0.0/10 is reverse-resolved and the PTR name adopted only if it
// resolves forward, which also gives known_hosts a stable identity. The rule
// is shared with the crawler and the credential resolver so all three agree
// on what counts as one device; only the logging is local. Nothing is
// substituted silently.
func normalizeTarget(host string) string {
	res := normalize.Resolve(host)
	switch {
	case !res.CGNAT:
	case res.PTR == "":
		fmt.Fprintf(os.Stderr, "reach: %s is a CGNAT (100.64/10) address with no PTR record; using the address as-is\n", host)
	case res.Confirmed:
		fmt.Fprintf(os.Stderr, "reach: %s is a CGNAT (100.64/10) address -> %s; connecting by name\n", host, res.PTR)
	default:
		fmt.Fprintf(os.Stderr, "reach: %s -> %s but the name does not resolve; using the address\n", host, res.PTR)
	}
	return res.Name
}

// parseJump accepts "host", "host:port", "user@host", "user@host:port".
func parseJump(spec, fallbackUser string) (*sshcore.JumpConfig, error) {
	j := &sshcore.JumpConfig{Username: fallbackUser}
	rest := spec
	if i := strings.IndexByte(rest, '@'); i >= 0 {
		j.Username = rest[:i]
		rest = rest[i+1:]
	}
	if host, portStr, err := net.SplitHostPort(rest); err == nil {
		port, perr := strconv.Atoi(portStr)
		if perr != nil {
			return nil, fmt.Errorf("jump port %q: %w", portStr, perr)
		}
		j.Host, j.Port = host, port
	} else {
		j.Host = rest
	}
	if j.Host == "" {
		return nil, fmt.Errorf("jump spec %q has no host", spec)
	}
	return j, nil
}

func promptSecret(label string) (string, error) {
	fmt.Fprintf(os.Stderr, "%s ", label)
	b, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func main() {
	var (
		host        = flag.String("host", "", "target host or address (required)")
		port        = flag.Int("port", 22, "target SSH port")
		user        = flag.String("user", "", "username (required)")
		keyPath     = flag.String("key", "", "private key path (~/ expands)")
		askPass     = flag.Bool("p", false, "prompt for password (also read from REACH_PASSWORD)")
		useAgent    = flag.Bool("agent", true, "try SSH agent first")
		jumpSpec    = flag.String("jump", "", "jump host: [user@]host[:port]")
		jumpKey     = flag.String("jump-key", "", "private key path for the jump host")
		legacy      = flag.Bool("legacy", false, "enable legacy KEX/ciphers/MACs for old gear")
		hostkeys    = flag.String("hostkey", "tofu", "host key policy: strict | tofu | insecure")
		knownHosts  = flag.String("known-hosts", "", "known_hosts path (default ~/.ssh/known_hosts)")
		paging      = flag.String("paging", "", `paging disable command, e.g. "terminal length 0"`)
		fingerprint = flag.Bool("fingerprint", false, "auto-detect platform and paging command (overridden by an explicit -paging)")
		fpOnly      = flag.Bool("fingerprint-only", false, "connect, fingerprint the platform, print the result, and exit (no commands)")
		promptRe    = flag.String("prompt", "", "prompt regex (default matches #, >, $, % prompts)")
		enablePw    = flag.Bool("enable", false, "enter privileged mode after connect (prompts for enable password)")
		timeout     = flag.Duration("timeout", 30*time.Second, "connect and per-command timeout")
	)
	var commands stringList
	flag.Var(&commands, "c", "command to run (repeatable; each -c is ONE command)")
	flag.Parse()
	commands = append(commands, flag.Args()...)

	if *host == "" || *user == "" {
		flag.Usage()
		os.Exit(2)
	}
	if len(commands) == 0 && !*fpOnly {
		fmt.Fprintln(os.Stderr, "reach: nothing to run (add -c \"show version\", or -fingerprint-only)")
		os.Exit(2)
	}

	var policy sshcore.HostKeyPolicy
	switch *hostkeys {
	case "strict":
		policy = sshcore.HostKeyStrict
	case "tofu":
		policy = sshcore.HostKeyTOFU
	case "insecure":
		policy = sshcore.HostKeyInsecure
		fmt.Fprintln(os.Stderr, "reach: WARNING host key verification disabled (-hostkey insecure)")
	default:
		fmt.Fprintf(os.Stderr, "reach: unknown -hostkey %q\n", *hostkeys)
		os.Exit(2)
	}

	password := os.Getenv("REACH_PASSWORD")
	if *askPass && password == "" {
		p, err := promptSecret(fmt.Sprintf("Password for %s@%s:", *user, *host))
		if err != nil {
			fmt.Fprintf(os.Stderr, "reach: read password: %v\n", err)
			os.Exit(1)
		}
		password = p
	}

	cfg := sshcore.Config{
		Host:             normalizeTarget(*host),
		Port:             *port,
		Timeout:          *timeout,
		Username:         *user,
		Password:         password,
		PrivateKeyPath:   *keyPath,
		UseAgent:         *useAgent,
		HostKeys:         policy,
		KnownHostsPath:   *knownHosts,
		LegacyAlgorithms: *legacy,
		HostKeyPrompt: func(hostname string, _ net.Addr, key ssh.PublicKey) (bool, error) {
			fmt.Fprintf(os.Stderr, "Unknown host %s\n  %s key: %s\nAccept and save? [y/N] ",
				hostname, key.Type(), ssh.FingerprintSHA256(key))
			var answer string
			fmt.Fscanln(os.Stdin, &answer)
			answer = strings.ToLower(strings.TrimSpace(answer))
			return answer == "y" || answer == "yes", nil
		},
		AuthPrompt: func(prompt string, echo bool) (string, error) {
			if echo {
				fmt.Fprintf(os.Stderr, "%s ", prompt)
				var answer string
				fmt.Fscanln(os.Stdin, &answer)
				return answer, nil
			}
			return promptSecret(prompt)
		},
	}

	if *jumpSpec != "" {
		j, err := parseJump(*jumpSpec, *user)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reach: %v\n", err)
			os.Exit(2)
		}
		j.PrivateKeyPath = *jumpKey
		if j.PrivateKeyPath == "" {
			p, err := promptSecret(fmt.Sprintf("Password for jump host %s@%s:", j.Username, j.Host))
			if err != nil {
				fmt.Fprintf(os.Stderr, "reach: read jump password: %v\n", err)
				os.Exit(1)
			}
			j.Password = p
		}
		cfg.Jump = j
	}

	// Ctrl-C reaches a command in flight, not just the loop between
	// commands. A "show tech-support" on a loaded chassis is minutes of
	// output, and without this the only way out is killing the process
	// while the device keeps writing.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client, err := sshcore.Dial(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reach: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	sess, err := netexec.Open(ctx, client, netexec.Options{
		PromptRegex:    *promptRe,
		PagingDisable:  *paging,
		CommandTimeout: *timeout,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "reach: %v\n", err)
		os.Exit(1)
	}
	defer sess.Close()

	if *fpOnly {
		plat, err := netexec.Fingerprint(ctx, sess)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reach: fingerprint: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("platform: %s\n", plat.Name)
		fmt.Printf("paging: %s\n", plat.PagingDisable)
		fmt.Printf("version-command: %s\n", plat.VersionCommand)
		if plat.VersionOutput != "" {
			fmt.Printf("\n%s\n", plat.VersionOutput)
		}
		if plat.Name == "unknown" {
			os.Exit(3) // connected and probed, but no classification
		}
		return
	}

	if *fingerprint && *paging == "" {
		plat, err := netexec.Fingerprint(ctx, sess)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reach: fingerprint: %v\n", err)
			os.Exit(1)
		}
		if plat.PagingDisable != "" {
			fmt.Fprintf(os.Stderr, "reach: detected %s (paging: %q)\n", plat.Name, plat.PagingDisable)
		} else {
			fmt.Fprintf(os.Stderr, "reach: detected %s\n", plat.Name)
		}
	}

	if *enablePw {
		p, err := promptSecret("Enable password:")
		if err != nil {
			fmt.Fprintf(os.Stderr, "reach: read enable password: %v\n", err)
			os.Exit(1)
		}
		if err := sess.Enable(ctx, "enable", p); err != nil {
			fmt.Fprintf(os.Stderr, "reach: enable: %v\n", err)
			os.Exit(1)
		}
	}

	exit := 0
	for _, cmd := range commands {
		out, err := sess.Run(ctx, cmd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "reach: %q: %v\n", cmd, err)
			exit = 1
			break
		}
		if len(commands) > 1 {
			fmt.Printf("### %s\n", cmd)
		}
		fmt.Println(out)
	}
	os.Exit(exit)
}
