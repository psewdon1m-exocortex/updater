package hostrecovery

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"updater/internal/config"
)

func hostRecoveryFixture(phase string) map[string]map[string][]byte {
	return map[string]map[string][]byte{
		"updater": {
			"var/lib/updater/jobs/component-fixture.json": []byte(fmt.Sprintf(`{"id":"component-fixture","service":"updater-self-update","state":"COMPLETED","finished_at":"2026-10-10T18:20:24Z","message":%q}`, phase)),
			"var/lib/updater/backups/fixture.zip":         []byte("synthetic-backup-" + phase),
		},
		"neptune": {
			"etc/neptune/neptune.env":                  []byte("Neptune__RegistryPath=/var/lib/neptune/projects.json\nNeptune__KernelOrigin=https://kernel.example\n"),
			"etc/neptune/kernel.token":                 []byte("synthetic-secret-" + phase),
			"etc/neptune/projects/saturn.env":          []byte("NEPTUNE_BACKUP_EXPORT_URL=http://127.0.0.1:18080/api/backup\nNEPTUNE_CONTROL_TOKEN_FILE=/etc/neptune/clients/saturn.control.token\nNEPTUNE_BACKUP_ENABLED=false\n"),
			"etc/neptune/clients/saturn.control.token": []byte("synthetic-secret-" + phase),
			"var/lib/neptune/projects.json":            []byte(fmt.Sprintf(`{"projects":[],"fixture":%q}`, phase)),
		},
		"gryphon": {
			"etc/gryphon/gryphon.env":          []byte("GRYPHON_PUBLIC_ORIGIN=https://gryphon.example\n"),
			"etc/gryphon/clients/kernel.token": []byte("synthetic-secret-" + phase),
			"var/lib/gryphon/bots.db":          []byte("synthetic-bot-state-" + phase),
		},
		"wyvern": {
			"etc/wyvern/identity/kernel.token":                  []byte("synthetic-secret-" + phase),
			"etc/exocortex/wyvern/manager.json":                 []byte(fmt.Sprintf(`{"manager_token":%q}`, "synthetic-secret-"+phase)),
			"etc/exocortex/wyvern/clients/mastermind/link.json": []byte(fmt.Sprintf(`{"token":%q}`, "synthetic-secret-"+phase)),
			"var/lib/wyvern/media.json":                         []byte(fmt.Sprintf(`{"records":[],"fixture":%q}`, phase)),
		},
	}
}

func writeHostRecoveryFixture(t *testing.T, root string, fixture map[string]map[string][]byte) {
	t.Helper()
	for _, files := range fixture {
		for name, body := range files {
			filename := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filename, body, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func assertHostRecoveryFixture(t *testing.T, root string, fixture map[string]map[string][]byte) {
	t.Helper()
	for _, files := range fixture {
		for name, expected := range files {
			body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
			if err != nil || !bytes.Equal(body, expected) {
				t.Fatalf("changed or missing host file %s: %v", name, err)
			}
		}
	}
}

func TestHostRecoveryAllServicesRoundTripAndFailureIsolation(t *testing.T) {
	source := t.TempDir()
	snapshot := hostRecoveryFixture("snapshot")
	writeHostRecoveryFixture(t, source, snapshot)
	runtime := config.Runtime{StateDir: filepath.Join(source, "var/lib/updater")}
	keys := map[string]string{}
	for _, scope := range RecoveryScopes {
		key, err := config.ManagedRecoveryKey(runtime, scope, true)
		if err != nil {
			t.Fatal(err)
		}
		for _, other := range keys {
			if key == other {
				t.Fatal("host services share a recovery key")
			}
		}
		keys[scope] = key
	}
	for _, scope := range RecoveryScopes {
		t.Run(scope, func(t *testing.T) {
			entries, err := CollectScope(source, scope)
			if err != nil || len(entries) != len(snapshot[scope]) {
				t.Fatalf("wrong service collection: %d entries, %v", len(entries), err)
			}
			for _, entry := range entries {
				if expected, ok := snapshot[scope][entry.Name]; !ok || !bytes.Equal(entry.Data, expected) {
					t.Fatal("archive crossed service boundaries or changed source bytes", entry.Name)
				}
			}
			encrypted, err := SealScope(entries, keys[scope], scope)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(encrypted, []byte("synthetic-secret-snapshot")) {
				t.Fatal("archive leaked a secret")
			}
			for _, other := range RecoveryScopes {
				if other == scope {
					continue
				}
				if _, err := OpenScope(encrypted, keys[scope], other); err == nil {
					t.Fatal("archive was accepted for another service", other)
				}
				if _, err := OpenScope(encrypted, keys[other], scope); err == nil {
					t.Fatal("archive accepted another service's key", other)
				}
			}
			tampered := bytes.Clone(encrypted)
			tampered[len(tampered)-1] ^= 1
			if _, err := OpenScope(tampered, keys[scope], scope); err == nil {
				t.Fatal("damaged archive was accepted")
			}
			decoded, err := OpenScope(encrypted, keys[scope], scope)
			if err != nil {
				t.Fatal(err)
			}
			target := t.TempDir()
			live := hostRecoveryFixture("live")
			writeHostRecoveryFixture(t, target, live)
			injected := errors.New("synthetic health failure")
			if err := ApplyScoped(target, decoded, scope, func() error { return injected }); !errors.Is(err, injected) {
				t.Fatal("failed restore was not reported", err)
			}
			assertHostRecoveryFixture(t, target, live)
			if PendingRecovery(target) {
				t.Fatal("completed rollback left an unresolved recovery journal")
			}
			if err := ApplyScoped(target, decoded, scope, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			live[scope] = snapshot[scope]
			assertHostRecoveryFixture(t, target, live)
			assertHostRecoveryFixture(t, source, snapshot)
		})
	}
}

func TestOtherHostRecoveryScopesIgnoreInvalidUpdaterHistory(t *testing.T) {
	root := t.TempDir()
	fixture := hostRecoveryFixture("snapshot")
	fixture["updater"]["var/lib/updater/jobs/component-fixture.json"] = []byte(`{"invalid":`)
	writeHostRecoveryFixture(t, root, fixture)
	if _, err := CollectScope(root, "updater"); err == nil || !strings.Contains(err.Error(), "component-fixture.json") {
		t.Fatal("damaged Updater history was silently omitted", err)
	}
	for _, scope := range []string{"neptune", "gryphon", "wyvern"} {
		t.Run(scope, func(t *testing.T) {
			entries, err := CollectScope(root, scope)
			if err != nil {
				t.Fatal("another service depended on Updater job contents", err)
			}
			encrypted, err := SealScope(entries, "synthetic recovery passphrase", scope)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := OpenScope(encrypted, "synthetic recovery passphrase", scope); err != nil {
				t.Fatal(err)
			}
		})
	}
}
