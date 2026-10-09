package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagedRecoveryKeyPersistenceExportAndIsolation(t *testing.T) {
	runtime := Runtime{StateDir: t.TempDir()}
	first, err := ManagedRecoveryKey(runtime, "updater", true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ManagedRecoveryKey(runtime, "updater", true)
	if err != nil || first != second || len(first) != 64 {
		t.Fatal("persistent key changed", err)
	}
	other, err := ManagedRecoveryKey(runtime, "neptune", true)
	if err != nil || first == other {
		t.Fatal("service keys are not independent", err)
	}
	destination := filepath.Join(t.TempDir(), "offline-key.json")
	if err := ExportRecoveryKey(runtime, "updater", destination); err != nil {
		t.Fatal(err)
	}
	restored, err := ReadRecoveryKey(destination, "updater")
	if err != nil || restored != first {
		t.Fatal("export does not recover the original key", err)
	}
	if _, err := ReadRecoveryKey(destination, "neptune"); err == nil {
		t.Fatal("cross-service key was accepted")
	}
	if err := ExportRecoveryKey(runtime, "updater", destination); !os.IsExist(err) {
		t.Fatal("existing file was overwritten", err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(destination, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRecoveryKey(link, "updater"); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Chmod(destination, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRecoveryKey(destination, "updater"); err == nil {
		t.Fatal("public key file accepted")
	}
	if _, err := ManagedRecoveryKey(Runtime{StateDir: t.TempDir()}, "updater", false); !os.IsNotExist(err) {
		t.Fatal("restore invented a replacement key", err)
	}
}
