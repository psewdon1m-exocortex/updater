package hostrecovery

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"updater/internal/config"
)

func TestRecoveryOriginUsesOwnKernelPrincipalAndFallsBackOnlyOnOutage(t *testing.T) {
	ref := "volt://3518462b-bb66-459a-a1ab-c38a837740ab/4"
	values := map[string]any{"services": map[string]any{"saturn": map[string]any{"sni": ref, "port": ref}}, "credentials": map[string]any{"private": ref}}
	raw, _ := json.Marshal(map[string]any{"values": values})
	sum := sha256.Sum256(raw)
	status := 200
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer host-fixture-token" {
			t.Error("wrong machine principal")
		}
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		if r.URL.Path == "/api/v1/register/snapshot" {
			json.NewEncoder(w).Encode(map[string]any{"schema": "exocortex.register.snapshot.v1", "revision": "one", "checksum": "sha256:" + hex.EncodeToString(sum[:]), "values": values})
			return
		}
		var input struct {
			Keys []string `json:"keys"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Keys) != 2 || input.Keys[0] != "services.saturn.port" || input.Keys[1] != "services.saturn.sni" {
			t.Error("resolved non-Saturn values")
		}
		json.NewEncoder(w).Encode(map[string]any{"schema": "exocortex.register.resolution.v1", "values": map[string]any{"services.saturn.sni": map[string]string{"value": "saturn.test"}, "services.saturn.port": map[string]string{"value": "8443"}}})
	}))
	defer server.Close()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	transport.TLSClientConfig.RootCAs = pool
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = previous; transport.CloseIdleConnections() }()
	directory := t.TempDir()
	token := filepath.Join(directory, "kernel.token")
	if err := os.WriteFile(token, []byte("host-fixture-token"), 0600); err != nil {
		t.Fatal(err)
	}
	runtime := config.Runtime{StateDir: directory, HostConfigPath: filepath.Join(directory, "host.json")}
	cfg := config.HostConfig{KernelURL: server.URL, KernelTokenFile: token, RecoveryGatewayURL: "https://saved.test"}
	if err := config.SaveHost(runtime, cfg); err != nil {
		t.Fatal(err)
	}
	origin, err := ResolveOrigin(runtime, "")
	if err != nil || origin != "https://saturn.test:8443" {
		t.Fatal(origin, err)
	}
	status = 503
	origin, err = ResolveOrigin(runtime, "")
	if err != nil || origin != "https://saved.test" {
		t.Fatal("outage fallback", origin, err)
	}
	for _, denied := range []int{401, 403} {
		status = denied
		if _, err := ResolveOrigin(runtime, ""); err == nil {
			t.Fatal("authorization denial used fallback")
		}
	}
	origin, err = ResolveOrigin(runtime, "https://override.test/")
	if err != nil || origin != "https://override.test" {
		t.Fatal("explicit recovery override", origin, err)
	}
	for _, invalid := range []string{"http://insecure.test", "https://user:secret@host.test", "https://host.test/path", "https://host.test?token=secret"} {
		if _, err := ResolveOrigin(runtime, invalid); err == nil {
			t.Fatal("unsafe override accepted")
		}
	}
}

func TestManagedKeyArchiveCanRestoreOnAnotherHost(t *testing.T) {
	runtime := config.Runtime{StateDir: t.TempDir()}
	key, err := config.ManagedRecoveryKey(runtime, "gryphon", true)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := SealScope([]Entry{{Name: "var/lib/gryphon/state.json", Data: []byte(`{"state":"ready"}`)}}, key, "gryphon")
	if err != nil {
		t.Fatal(err)
	}
	exported := filepath.Join(t.TempDir(), "offline-key.json")
	if err := config.ExportRecoveryKey(runtime, "gryphon", exported); err != nil {
		t.Fatal(err)
	}
	imported, err := config.ReadRecoveryKey(exported, "gryphon")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := OpenScope(archive, imported, "gryphon")
	if err != nil || len(entries) != 1 {
		t.Fatal("exported key cannot restore", err)
	}
	other, _ := config.ManagedRecoveryKey(config.Runtime{StateDir: t.TempDir()}, "gryphon", true)
	if _, err := OpenScope(archive, other, "gryphon"); err == nil {
		t.Fatal("wrong key accepted")
	}
}
