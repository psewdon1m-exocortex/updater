package hostrecovery

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdaterRecoveryRoundTripsFinishedHostOperationHistory(t *testing.T) {
	root := t.TempDir()
	services := []string{
		"updater-kernel-connection", "updater-self-update", "updater-source",
		"host-recovery", "host-recovery-export", "host-recovery-restore",
		"host-recovery-configuration", "host-recovery-key-export",
		"neptune-initialization", "neptune-installation", "neptune-update", "neptune-source",
		"gryphon-initialization", "gryphon-update", "gryphon-source", "gryphon-bot",
		"wyvern-installation", "wyvern-update", "wyvern-source",
		"wyvern-reload", "wyvern-drain", "wyvern-resume", "wyvern-repair",
		"wyvern-connect-kernel", "wyvern-adapter-put", "wyvern-profile-put",
		"wyvern-adapter-disable", "wyvern-adapter-enable", "wyvern-adapter-delete",
		"wyvern-client-grant", "wyvern-client-revoke",
		"window-installation", "window-update", "window-source",
	}
	original := map[string][]byte{}
	for i, service := range services {
		state := "COMPLETED"
		if i%2 != 0 {
			state = "FAILED"
		}
		body, err := json.Marshal(map[string]string{
			"id": service, "service": service, "state": state,
			"finished_at": "2026-10-10T18:20:24Z", "message": "synthetic-history-canary",
		})
		if err != nil {
			t.Fatal(err)
		}
		name := "var/lib/updater/jobs/" + service + ".json"
		filename := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, body, 0o600); err != nil {
			t.Fatal(err)
		}
		original[name] = body
	}
	entries, err := CollectScope(root, "updater")
	if err != nil || len(entries) != len(original) {
		t.Fatalf("finished host operations were lost during collection: %d, %v", len(entries), err)
	}
	password := "synthetic scoped recovery passphrase"
	for _, scoped := range []bool{false, true} {
		var encrypted []byte
		if scoped {
			encrypted, err = SealScope(entries, password, "updater")
		} else {
			encrypted, err = Seal(entries, password)
		}
		if err != nil {
			t.Fatalf("finished host operation history cannot be sealed (scoped=%t): %v", scoped, err)
		}
		if bytes.Contains(encrypted, []byte("synthetic-history-canary")) {
			t.Fatal("job history was exposed in plaintext")
		}
		var decoded []Entry
		if scoped {
			decoded, err = OpenScope(encrypted, password, "updater")
		} else {
			decoded, err = Open(encrypted, password)
		}
		if err != nil || len(decoded) != len(original) {
			t.Fatalf("finished host operations cannot be opened (scoped=%t): %d, %v", scoped, len(decoded), err)
		}
		restored := t.TempDir()
		if err := ApplyScoped(restored, decoded, "updater", func() error { return nil }); err != nil {
			t.Fatal("job history cannot be restored", err)
		}
		for name, expected := range original {
			actual, err := os.ReadFile(filepath.Join(restored, filepath.FromSlash(name)))
			if err != nil || !bytes.Equal(actual, expected) {
				t.Fatalf("restore changed or lost job history %s: %v", name, err)
			}
		}
	}
}

func TestUpdaterRecoveryRejectsInvalidJobMetadataWithoutDisclosingContent(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{"json", `{"id":"fixture",`},
		{"headless-service", `{"id":"fixture","service":"saturn","message":"synthetic-secret-canary"}`},
		{"unknown-multipart-operation", `{"id":"fixture","service":"updater-kernel-connect","message":"synthetic-secret-canary"}`},
		{"extra-suffix", `{"id":"fixture","service":"updater-self-update-extra","message":"synthetic-secret-canary"}`},
		{"invalid-head", `{"id":"fixture","head_id":"../saturn","service":"updater-self-update","message":"synthetic-secret-canary"}`},
		{"filename", `{"id":"different","service":"updater-kernel-connection","message":"synthetic-secret-canary"}`},
		{"backup-path", `{"id":"fixture","service":"updater-self-update","backup_path":"/var/lib/updater/backups/../jobs/fixture.json","message":"synthetic-secret-canary"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry := Entry{Name: "var/lib/updater/jobs/fixture.json", Data: []byte(test.body)}
			for _, seal := range []func([]Entry, string) ([]byte, error){Seal, func(entries []Entry, password string) ([]byte, error) {
				return SealScope(entries, password, "updater")
			}} {
				_, err := seal([]Entry{entry}, "synthetic recovery passphrase")
				if err == nil {
					t.Fatal("invalid job was accepted")
				}
				if !strings.Contains(err.Error(), entry.Name) || strings.Contains(err.Error(), "synthetic-secret-canary") {
					t.Fatalf("diagnostic must identify the file without its content: %v", err)
				}
			}
		})
	}
}
