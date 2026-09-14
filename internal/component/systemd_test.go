package component

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagedSystemdUnitAcceptsOnlyExpectedSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "units", "neptune.service")
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "systemd", "neptune.service")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	actual, err := managedSystemdUnit(link, target)
	if err != nil || actual != target {
		t.Fatalf("managed unit = %q, %v", actual, err)
	}

	unexpected := filepath.Join(directory, "unexpected.service")
	if err := os.WriteFile(unexpected, []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(unexpected, link); err != nil {
		t.Fatal(err)
	}
	if _, err := managedSystemdUnit(link, target); err == nil {
		t.Fatal("unexpected helper-unit target was accepted")
	}
}

func TestValidatePreservedRuntimeUnit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "neptune.service")
	if err := os.WriteFile(path, []byte("[Service]\nRuntimeDirectory=neptune\nRuntimeDirectoryPreserve=yes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validatePreservedRuntimeUnit(path, "neptune"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[Service]\nRuntimeDirectory=neptune\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validatePreservedRuntimeUnit(path, "neptune"); err == nil {
		t.Fatal("unit without RuntimeDirectoryPreserve=yes was accepted")
	}
}
