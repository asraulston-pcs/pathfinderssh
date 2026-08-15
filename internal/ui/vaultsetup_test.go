// internal/ui/vaultsetup_test.go
package ui

import (
	"strings"
	"testing"
)

func TestAFreshMachineIsOffered(t *testing.T) {
	c := VaultCheck{Path: "/home/lab/.pathfinderssh/vault.json"}
	if !c.ShouldOffer() {
		t.Fatal("no vault, never asked: should offer")
	}
}

func TestAnExistingVaultIsNotOffered(t *testing.T) {
	c := VaultCheck{Path: "/home/lab/.pathfinderssh/vault.json", Present: true}
	if c.ShouldOffer() {
		t.Fatal("a vault file exists: nothing to offer")
	}
}

func TestAVaultUnlockedFromTheKeyringIsNotOffered(t *testing.T) {
	// Present is false here on purpose: this is the case where the check
	// runs before anything stats the file but the keyring already opened
	// it. Offering to CREATE the vault that is open would be absurd.
	c := VaultCheck{Path: "/home/lab/.pathfinderssh/vault.json", Unlocked: true}
	if c.ShouldOffer() {
		t.Fatal("a vault is already unlocked: nothing to offer")
	}
}

func TestDecliningIsRememberedRatherThanReasked(t *testing.T) {
	c := VaultCheck{Path: "/home/lab/.pathfinderssh/vault.json", Declined: true}
	if c.ShouldOffer() {
		t.Fatal("already said no: a prompt every launch is one that gets clicked through")
	}
}

func TestNoResolvedPathAsksNothing(t *testing.T) {
	// There is no sensible file to offer to create, so the honest move is
	// silence rather than a dialog naming "".
	if (VaultCheck{Path: "   "}).ShouldOffer() {
		t.Fatal("blank path: nothing to offer to create")
	}
}

func TestTheWarningNamesThePathAndBothAutomationConsumers(t *testing.T) {
	msg := VaultSetupWarning("/home/lab/.pathfinderssh/vault.json")
	for _, want := range []string{"/home/lab/.pathfinderssh/vault.json", "crawl", "capture"} {
		if !strings.Contains(msg, want) {
			t.Errorf("warning does not mention %q:\n%s", want, msg)
		}
	}
}

func TestTheWarningDoesNotClaimTheVaultIsRequired(t *testing.T) {
	// The claim would be false — a static credential in the launch form
	// works — and a first-run dialog that overstates its case is the reason
	// the next one gets dismissed unread.
	msg := strings.ToLower(VaultSetupWarning("/tmp/vault.json"))
	if strings.Contains(msg, "required") {
		t.Errorf("warning claims the vault is required:\n%s", msg)
	}
}

func TestACompleteCreateFormPasses(t *testing.T) {
	f := VaultCreateForm{Path: "/tmp/vault.json", Master: "lab-master-1", Confirm: "lab-master-1"}
	if errs := f.Validate(); len(errs) != 0 {
		t.Fatalf("valid form refused: %s", ProblemText(errs))
	}
}

func TestAMissingPathIsRefused(t *testing.T) {
	f := VaultCreateForm{Path: "  ", Master: "lab-master-1", Confirm: "lab-master-1"}
	errs := f.Validate()
	if len(errs) != 1 || errs[0].Field != "vault" {
		t.Fatalf("want one vault problem, got %s", ProblemText(errs))
	}
}

func TestAnEmptyMasterIsRefused(t *testing.T) {
	f := VaultCreateForm{Path: "/tmp/vault.json"}
	errs := f.Validate()
	if len(errs) != 1 || errs[0].Field != "master password" {
		t.Fatalf("want one master problem, got %s", ProblemText(errs))
	}
}

func TestAShortMasterIsRefusedWithTheLength(t *testing.T) {
	f := VaultCreateForm{Path: "/tmp/vault.json", Master: "short", Confirm: "short"}
	errs := f.Validate()
	if len(errs) != 1 {
		t.Fatalf("want one problem, got %s", ProblemText(errs))
	}
	if !strings.Contains(errs[0].Message, "8") {
		t.Errorf("the message should name the minimum, got %q", errs[0].Message)
	}
}

func TestAShortMasterDoesNotAlsoComplainAboutConfirm(t *testing.T) {
	// Two complaints about one mistake reads as two mistakes.
	f := VaultCreateForm{Path: "/tmp/vault.json", Master: "short", Confirm: ""}
	if errs := f.Validate(); len(errs) != 1 {
		t.Fatalf("want one problem, got %s", ProblemText(errs))
	}
}

func TestAMistypedConfirmIsRefused(t *testing.T) {
	f := VaultCreateForm{Path: "/tmp/vault.json", Master: "lab-master-1", Confirm: "lab-master-2"}
	errs := f.Validate()
	if len(errs) != 1 || errs[0].Field != "confirm" {
		t.Fatalf("want one confirm problem, got %s", ProblemText(errs))
	}
}

func TestTheCreatedNoteSaysHowToAddCredentials(t *testing.T) {
	msg := VaultCreatedNote("/tmp/vault.json")
	if !strings.Contains(msg, "/tmp/vault.json") {
		t.Errorf("the note should name the file it created:\n%s", msg)
	}
	if !strings.Contains(msg, "pfvault") || !strings.Contains(msg, "Manage credentials") {
		t.Errorf("the note should name both ways in:\n%s", msg)
	}
}
