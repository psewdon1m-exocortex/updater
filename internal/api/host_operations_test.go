package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"updater/internal/config"
	"updater/internal/console"
	"updater/internal/hostrecovery"
	"updater/internal/state"
)

func TestOperatorHostConnectionHistorySurvivesRecovery(t *testing.T) {
	root := t.TempDir()
	runtime := config.Runtime{
		StateDir:     filepath.Join(root, "var/lib/updater"),
		RegistryPath: filepath.Join(root, "etc/exocortex/heads.json"),
	}
	store, err := state.New(runtime.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(root, "machine.token")
	if err := os.WriteFile(tokenFile, []byte("synthetic-private-machine-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Server{Runtime: runtime, Store: store}
	actions := []console.Action{
		{Component: "updater", Kind: "set-kernel", RequestID: "tui-host-kernel-fixture", KernelURL: "https://kernel.example", KernelTokenFile: tokenFile, HostID: "fixture-host"},
		{Component: "updater", Kind: "set-source", RequestID: "tui-host-source-fixture", RepositoryURL: "https://github.com/example/updater"},
	}
	for _, action := range actions {
		body, err := json.Marshal(action)
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		s.operatorHandler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "http://updater.local/v1/actions", strings.NewReader(string(body))))
		if response.Code != http.StatusOK {
			t.Fatalf("host configuration failed: %d %s", response.Code, response.Body.String())
		}
	}
	entries, err := hostrecovery.CollectScope(root, "updater")
	if err != nil || len(entries) != len(actions) {
		t.Fatalf("host operation history was not collected: %d, %v", len(entries), err)
	}
	password := "synthetic recovery passphrase"
	encrypted, err := hostrecovery.SealScope(entries, password, "updater")
	if err != nil {
		t.Fatal("TUI-generated host history prevents recovery export", err)
	}
	decoded, err := hostrecovery.OpenScope(encrypted, password, "updater")
	if err != nil {
		t.Fatal(err)
	}
	restoredRoot := t.TempDir()
	if err := hostrecovery.ApplyScoped(restoredRoot, decoded, "updater", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	restored, err := state.New(filepath.Join(restoredRoot, "var/lib/updater"))
	if err != nil || len(restored.List()) != len(store.List()) {
		t.Fatal("Updater could not load the restored job history", err)
	}
	for _, action := range actions {
		original, ok := store.ByRequestID(action.RequestID)
		if !ok || original.HeadID != "" || original.FinishedAt == nil {
			t.Fatal("test did not create a finished host-owned job")
		}
		actual, ok := restored.ByRequestID(action.RequestID)
		before, _ := json.Marshal(original)
		after, _ := json.Marshal(actual)
		if !ok || string(before) != string(after) {
			t.Fatal("recovery changed or lost the generated host job", action.Kind)
		}
	}
}
