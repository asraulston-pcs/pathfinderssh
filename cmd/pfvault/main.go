// cmd/pfvault/main.go
//
// pfvault — create and manage the credential vault that `crawl -vault` reads.
//
// Secrets are never taken from the command line. A password on argv lands in
// shell history, in the process table, and in any shell-integration log the
// terminal happens to keep, so every secret here is prompted or comes from the
// environment. This is the same reason the vault stores nothing in plaintext:
// the file is not the only place a credential can leak from.
//
// Example — the two credentials a crawl usually needs:
//
//	pfvault init
//	pfvault add -name lab-key -user admin -key ~/.ssh/id_ed25519 -tag lab -priority 10
//	pfvault add -name lab-pw  -user admin -tag lab -priority 20
//	pfvault list
//
// Priority orders the ladder, lower first — so the key is tried before the
// password above. Both carry the "lab" tag, which is what
// `crawl -cred-tag lab` selects on.
//
// This collapses into `pathfinder vault ...` when the binaries merge.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/scottpeterman/pathfinderssh/internal/vault"
	"github.com/scottpeterman/pathfinderssh/internal/vaultcli"
)

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

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

const usage = `pfvault — manage the pathfinder credential vault

usage: pfvault [-vault PATH] <command> [flags]

commands:
  init              create a new vault
  add               add a credential (secrets are prompted, never on argv)
  list              list credentials (no secret material)
  rm NAME|ID        remove a credential
  disable NAME|ID   take a credential out of automatic selection
  enable NAME|ID    put it back
  default           report which credential a session naming none would use
  default NAME|ID   make it the default
  default -clear    leave no default
  keyring set       store this vault's master password in the OS keyring
  keyring clear     remove it
  keyring status    report what the unlock path would find

run "pfvault <command> -h" for command flags
`

func main() {
	vaultPath := flag.String("vault", vaultcli.DefaultPath(), "vault file path")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}

	var err error
	switch args[0] {
	case "init":
		err = cmdInit(*vaultPath)
	case "add":
		err = cmdAdd(*vaultPath, args[1:])
	case "list", "ls":
		err = cmdList(*vaultPath)
	case "rm", "remove", "delete":
		err = cmdRemove(*vaultPath, args[1:])
	case "disable":
		err = cmdSetDisabled(*vaultPath, args[1:], true)
	case "enable":
		err = cmdSetDisabled(*vaultPath, args[1:], false)
	case "default":
		err = cmdDefault(*vaultPath, args[1:])
	case "keyring":
		err = cmdKeyring(*vaultPath, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "pfvault: unknown command %q\n\n", args[0])
		flag.Usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "pfvault: %v\n", err)
		os.Exit(1)
	}
}

func cmdInit(path string) error {
	v := vault.New(path)
	if v.Exists() {
		return fmt.Errorf("a vault already exists at %s", path)
	}
	if dir := parentDir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	master, err := vaultcli.MasterNew()
	if err != nil {
		return err
	}
	if err := v.Create(master); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "created %s\n", path)
	return nil
}

// cmdKeyring manages the OS-keyring entry that lets a crawl unlock the vault
// with no human present. Storing an unverified password is how a keyring
// entry becomes a lockout, so `set` opens the vault with the password before
// filing it.
func cmdKeyring(path string, argv []string) error {
	sub := ""
	if len(argv) > 0 {
		sub = argv[0]
	}
	switch sub {
	case "set":
		v := vault.New(path)
		if !v.Exists() {
			return fmt.Errorf("no vault at %s", path)
		}
		master, err := vaultcli.Prompt("vault master password")
		if err != nil {
			return err
		}
		if err := v.Unlock(master); err != nil {
			return fmt.Errorf("not storing an unverified password: %w", err)
		}
		v.Lock()
		if err := vaultcli.KeyringSet(path, master); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "stored master password for %s in the OS keyring\n", path)
		return nil

	case "clear":
		if err := vaultcli.KeyringClear(path); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "cleared any keyring entry for %s\n", path)
		return nil

	case "status":
		st := vaultcli.Keyring(path)
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintf(w, "vault\t%s\n", path)
		fmt.Fprintf(w, "keyring account\t%s\n", st.Account)
		switch {
		case st.Disabled:
			fmt.Fprintf(w, "state\tdisabled (%s is set)\n", vaultcli.NoKeyringEnvVar)
		case st.Err != nil:
			fmt.Fprintf(w, "state\tunavailable: %v\n", st.Err)
		case st.HasEntry:
			fmt.Fprintf(w, "state\tentry present\n")
		default:
			fmt.Fprintf(w, "state\tavailable, no entry\n")
		}
		if _, ok := os.LookupEnv(vaultcli.MasterEnvVar); ok {
			fmt.Fprintf(w, "note\t%s is also set (used only if the keyring has no entry)\n",
				vaultcli.MasterEnvVar)
		}
		return w.Flush()

	default:
		return fmt.Errorf("keyring: expected set, clear, or status")
	}
}

func cmdAdd(path string, argv []string) error {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	name := fs.String("name", "", "credential name (required, unique)")
	user := fs.String("user", "", "ssh username (required)")
	keyPath := fs.String("key", "", "private key path; implies -auth publickey")
	authType := fs.String("auth", "", "auth type: password, publickey, agent (default: publickey if -key is set, else password)")
	askPassphrase := fs.Bool("passphrase", false, "prompt for the key passphrase")
	desc := fs.String("desc", "", "free-text description")
	priority := fs.Int("priority", 0, "ladder order within the same scope; lower runs first")
	makeDefault := fs.Bool("default", false, "also make this the default credential for sessions naming none")
	var tags, cidrs, platforms stringList
	fs.Var(&tags, "tag", "tag (repeatable or comma-separated)")
	fs.Var(&cidrs, "scope-cidr", "restrict to targets inside this prefix (repeatable)")
	fs.Var(&platforms, "scope-platform", "restrict to these fingerprinted platforms (repeatable)")
	domain := fs.String("scope-domain", "", "restrict to identities under this domain suffix")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	if *name == "" || *user == "" {
		return fmt.Errorf("-name and -user are required")
	}

	at := inferAuthType(*authType, *keyPath)

	c := vault.Credential{
		Name:        *name,
		Username:    *user,
		AuthType:    at,
		Description: *desc,
		Priority:    *priority,
		Tags:        expandCSV(tags),
		Scope: vault.Scope{
			DomainSuffix: *domain,
			CIDRs:        expandCSV(cidrs),
			Platforms:    expandCSV(platforms),
		},
	}

	// Only the material the declared auth type will actually use is
	// collected, matching how the dialer applies it. Storing a password on a
	// publickey credential would mean carrying a secret nothing ever reads.
	switch c.Method() {
	case vault.AuthPublicKey:
		if *keyPath == "" {
			return fmt.Errorf("-key is required for publickey credentials")
		}
		c.KeyPath = *keyPath
		if *askPassphrase {
			pp, err := vaultcli.Prompt("key passphrase")
			if err != nil {
				return err
			}
			c.KeyPassphrase = pp
		}
	case vault.AuthAgent:
		// Nothing to store; the agent holds the material.
	default:
		pw, err := vaultcli.Prompt(fmt.Sprintf("password for %s@%s", *user, *name))
		if err != nil {
			return err
		}
		if pw == "" {
			return fmt.Errorf("empty password")
		}
		c.Password = pw
	}

	v, err := vaultcli.Open(path)
	if err != nil {
		return err
	}
	defer v.Lock()

	added, err := v.Add(c)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "added %q (%s, %s)\n", added.Name, added.ID, added.AuthType)

	if *makeDefault {
		if err := v.SetDefault(added.ID); err != nil {
			return fmt.Errorf("credential added, but making it the default failed: %w", err)
		}
		fmt.Fprintf(os.Stderr, "%q is now the default credential\n", added.Name)
	}
	return nil
}

// cmdDefault reports or changes the credential a session naming none uses.
//
// The BARE form REPORTS. A command whose no-argument spelling silently changes
// something is one typo away from a bad afternoon, and "which one is it" is the
// question asked far more often than "make it that one".
func cmdDefault(path string, argv []string) error {
	fs := flag.NewFlagSet("default", flag.ExitOnError)
	clear := fs.Bool("clear", false, "leave no default credential")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	rest := fs.Args()
	if *clear && len(rest) > 0 {
		return fmt.Errorf("usage: pfvault default [NAME|ID] | pfvault default -clear")
	}
	if len(rest) > 1 {
		return fmt.Errorf("usage: pfvault default [NAME|ID] | pfvault default -clear")
	}

	v, err := vaultcli.Open(path)
	if err != nil {
		return err
	}
	defer v.Lock()

	switch {
	case *clear:
		if err := v.ClearDefault(); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "no default credential")
		return nil

	case len(rest) == 1:
		c, err := v.Get(rest[0])
		if err != nil {
			return err
		}
		// Refused rather than silently set: Default() skips a disabled
		// credential, so this would look like it worked and then do
		// nothing on every connection.
		if c.Disabled {
			return fmt.Errorf("%q is disabled; enable it first", c.Name)
		}
		if err := v.SetDefault(c.ID); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%q is now the default credential\n", c.Name)
		return nil

	default:
		name := v.DefaultName()
		if name == "" {
			fmt.Fprintln(os.Stderr, "no default credential; sessions naming none authenticate with what they carry")
			return nil
		}
		fmt.Fprintf(os.Stderr, "default credential: %s\n", name)
		return nil
	}
}

func cmdList(path string) error {
	v, err := vaultcli.Open(path)
	if err != nil {
		return err
	}
	defer v.Lock()

	metas, err := v.List()
	if err != nil {
		return err
	}
	if len(metas) == 0 {
		fmt.Fprintln(os.Stderr, "vault is empty")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tUSER\tAUTH\tPRIO\tTAGS\tSCOPE\tSTATE")
	for _, m := range metas {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			m.Name, m.Username, m.AuthLabel, m.Priority,
			joinOr(m.Tags, "-"), scopeSummary(m.Scope), state(m))
	}
	return w.Flush()
}

func cmdRemove(path string, argv []string) error {
	if len(argv) != 1 {
		return fmt.Errorf("usage: pfvault rm NAME|ID")
	}
	v, err := vaultcli.Open(path)
	if err != nil {
		return err
	}
	defer v.Lock()

	c, err := v.Get(argv[0])
	if err != nil {
		return err
	}
	if err := v.Delete(c.ID); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "removed %q\n", c.Name)
	return nil
}

func cmdSetDisabled(path string, argv []string, disabled bool) error {
	if len(argv) != 1 {
		return fmt.Errorf("usage: pfvault disable|enable NAME|ID")
	}
	v, err := vaultcli.Open(path)
	if err != nil {
		return err
	}
	defer v.Lock()

	c, err := v.Get(argv[0])
	if err != nil {
		return err
	}
	if err := v.SetDisabled(c.ID, disabled); err != nil {
		return err
	}
	verb := "enabled"
	if disabled {
		verb = "disabled"
	}
	fmt.Fprintf(os.Stderr, "%s %q\n", verb, c.Name)
	return nil
}

// inferAuthType fills in -auth when it was not given. Supplying -key without
// -auth is the common case and means publickey; anything else defaults to
// password, which is what a bare -name/-user pair is asking for.
func inferAuthType(authFlag, keyPath string) string {
	if authFlag != "" {
		return authFlag
	}
	if keyPath != "" {
		return "publickey"
	}
	return "password"
}

func state(m vault.Meta) string {
	var parts []string
	if m.Disabled {
		parts = append(parts, "disabled")
	}
	if m.IsDefault {
		parts = append(parts, "default")
	}
	if !m.HasSecret && m.AuthLabel != "agent" {
		parts = append(parts, "no-secret")
	}
	return joinOr(parts, "ok")
}

func scopeSummary(s vault.Scope) string {
	var parts []string
	if s.DomainSuffix != "" {
		parts = append(parts, "*."+s.DomainSuffix)
	}
	parts = append(parts, s.CIDRs...)
	parts = append(parts, s.Platforms...)
	return joinOr(parts, "any")
}

func joinOr(parts []string, empty string) string {
	if len(parts) == 0 {
		return empty
	}
	return strings.Join(parts, ",")
}

// parentDir is filepath.Dir without importing path/filepath for one call on a
// path that is already slash-separated by construction.
func parentDir(path string) string {
	i := strings.LastIndexAny(path, `/\`)
	if i <= 0 {
		return ""
	}
	return path[:i]
}
