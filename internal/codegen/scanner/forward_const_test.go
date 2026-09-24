package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanDeviceConstantsResolvesForwardReferences(t *testing.T) {
	dir := t.TempDir()
	src := `package fwd

const Combined = FlagA | FlagB

const (
	FlagA = 0x10
	FlagB = 0x0A
)
`
	if err := os.WriteFile(filepath.Join(dir, "const.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := ScanDeviceConstants(dir)
	if err != nil {
		t.Fatalf("ScanDeviceConstants: %v", err)
	}
	for _, c := range result.Constants {
		if c.Name != "Combined" {
			continue
		}
		v, ok := toUint64(c.Value)
		if !ok || v != 0x1A {
			t.Fatalf("Combined = %#v, want 0x1A", c.Value)
		}
		return
	}
	t.Fatal("Combined not found")
}
