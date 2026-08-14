package jump

import (
	"os"
	"testing"
)

func TestShippedExampleIsValid(t *testing.T) {
	raw, err := os.ReadFile("../../examples/jump_hosts.yaml")
	if err != nil {
		t.Skipf("example not present: %v", err)
	}
	cfg, err := ParseConfig(raw)
	if err != nil {
		t.Fatalf("shipped example does not parse: %v", err)
	}
	if _, err := NewResolver(cfg, nil); err != nil {
		t.Fatalf("shipped example does not validate: %v", err)
	}
}
