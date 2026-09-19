package hostrecovery

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptedCleanRestoreAndRollback(t *testing.T) {
	entries := []Entry{{Name: "var/lib/gryphon/secrets/bot-123.token", Data: []byte("synthetic-secret-canary")}, {Name: "var/lib/neptune/projects.json", Data: []byte(`{"projects":[]}`)}}
	password := "synthetic recovery password 123"
	archive, err := Seal(entries, password)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(archive, entries[0].Data) {
		t.Fatal("plaintext secret leaked")
	}
	if _, err := Open(archive, "incorrect recovery password"); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
	changed := bytes.Clone(archive)
	changed[len(changed)-1] ^= 1
	if _, err := Open(changed, password); err == nil {
		t.Fatal("tampered archive accepted")
	}
	decoded, err := Open(archive, password)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := Apply(root, decoded, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(root, filepath.FromSlash(entries[0].Name))
	restored, _ := os.ReadFile(tokenPath)
	if !bytes.Equal(restored, entries[0].Data) {
		t.Fatal("secret not restored")
	}
	original := bytes.Clone(restored)
	decoded[0].Data = []byte("replacement")
	if err := Apply(root, decoded, func() error { return errors.New("injected health failure") }); err == nil {
		t.Fatal("failed verification accepted")
	}
	restored, _ = os.ReadFile(tokenPath)
	if !bytes.Equal(restored, original) {
		t.Fatal("rollback lost the original state")
	}
}

func TestWyvernRecoveryPreservesIdentitiesAndLegacyArchivesDoNotEraseThem(t *testing.T) {
	entries := []Entry{
		{Name: "etc/wyvern/identity/kernel.token", Data: []byte("synthetic-runtime-identity")},
		{Name: "etc/exocortex/wyvern/manager.json", Data: []byte(`{"manager_token":"synthetic-manager"}`)},
		{Name: "etc/exocortex/wyvern/clients/mastermind/link.json", Data: []byte(`{"token":"synthetic-client"}`)},
		{Name: "var/lib/wyvern/media.json", Data: []byte(`{"records":[]}`)},
	}
	archive, err := Seal(entries, "synthetic recovery passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(archive, []byte("synthetic-runtime-identity")) {
		t.Fatal("plaintext identity exposed")
	}
	decoded, err := Open(archive, "synthetic recovery passphrase")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := Apply(root, decoded, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := Apply(root, []Entry{{Name: "var/lib/gryphon/state.db", Data: []byte("legacy")}}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(root, entry.Name))
		if err != nil || !bytes.Equal(body, entry.Data) {
			t.Fatal("legacy archive erased Wyvern state", entry.Name, err)
		}
	}
	for _, name := range []string{"etc/wyvern/wyvern.env", "etc/exocortex/units/wyvern.service", "var/lib/updater/components/wyvern/current.json"} {
		if _, err := Seal([]Entry{{Name: name, Data: []byte("executable-state")}}, "synthetic recovery passphrase"); err == nil {
			t.Fatal("executable deployment state was accepted", name)
		}
	}
}

func TestRejectEscapingExecutableAndSymlinkTargets(t *testing.T) {
	for _, name := range []string{"../etc/passwd", "/etc/passwd", "etc/exocortex/units/updater.service", "etc/neptune/script.sh"} {
		if _, err := Seal([]Entry{{Name: name, Data: []byte("x")}}, "synthetic recovery passphrase"); err == nil {
			t.Fatalf("unsafe member accepted: %s", name)
		}
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "var/lib"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "var/lib/gryphon")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := Apply(root, []Entry{{Name: "var/lib/gryphon/state.db", Data: []byte("x")}}, func() error { return nil }); err == nil {
		t.Fatal("symlink target accepted")
	}
}

func TestInterruptedTransactionRecoversPreviousDirectories(t *testing.T) {
	root := t.TempDir()
	original := []Entry{{Name: "var/lib/gryphon/state.db", Data: []byte("original-state")}}
	if err := Apply(root, original, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() { _ = recover() }()
		_ = Apply(root, []Entry{{Name: original[0].Name, Data: []byte("candidate-state")}}, func() error { panic("simulated process death before commit") })
	}()
	if !PendingRecovery(root) {
		t.Fatal("interrupted restore has no durable journal")
	}
	if err := RecoverInterrupted(root); err != nil {
		t.Fatal(err)
	}
	if err := RecoverInterrupted(root); err != nil {
		t.Fatal("recovery is not idempotent", err)
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(original[0].Name)))
	if err != nil || !bytes.Equal(data, original[0].Data) {
		t.Fatal("previous state was not recovered")
	}
}
