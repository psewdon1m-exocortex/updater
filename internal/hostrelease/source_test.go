package hostrelease

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"updater/internal/config"
)

func TestHostSourceFallsBackOnlyWhenKernelConnectionIsUnavailable(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires a root-owned Kernel machine token")
	}
	root := t.TempDir()
	runtime := config.Runtime{StateDir: root, RegistryPath: filepath.Join(root, "heads.json"), HostConfigPath: filepath.Join(root, "host.json")}
	token := filepath.Join(root, "machine.token")
	if err := os.WriteFile(token, []byte("scoped-machine-credential\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fallback := "https://github.com/example/neptune"
	host := config.HostConfig{KernelURL: "https://127.0.0.1:1", KernelTokenFile: token, HostID: "host-test", ReleaseSources: map[string]string{"neptune": fallback}}
	if err := config.SaveHost(runtime, host); err != nil {
		t.Fatal(err)
	}
	source, err := Resolve(runtime, "neptune")
	if err != nil || source.Repository != fallback || source.Origin != "tui-fallback" || source.Reason == "" {
		t.Fatalf("connection outage did not use the visible fallback: %+v %v", source, err)
	}
	if err := os.Remove(token); err != nil {
		t.Fatal(err)
	}
	if source, err := Resolve(runtime, "neptune"); err != nil || source.Origin != "tui-fallback" {
		t.Fatalf("missing machine token did not use the visible fallback: %+v %v", source, err)
	}
	if err := os.WriteFile(token, []byte("scoped-machine-credential\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer server.Close()
	previous := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = previous }()
	host.KernelURL = server.URL
	if err := config.SaveHost(runtime, host); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(runtime, "neptune"); err == nil {
		t.Fatal("reachable but unauthorized Kernel silently used fallback")
	}
	values := map[string]any{"repositories": map[string]any{"neptune": map[string]any{"url": "volt://11111111-1111-4111-8111-111111111111/1"}, "gryphon": map[string]any{"url": "volt://22222222-2222-4222-8222-222222222222/1"}}}
	canonical, _ := json.Marshal(map[string]any{"values": values})
	checksum := sha256.Sum256(canonical)
	snapshot, _ := json.Marshal(map[string]any{"schema": "exocortex.register.snapshot.v1", "revision": "r1", "checksum": "sha256:" + hex.EncodeToString(checksum[:]), "values": values})
	server2 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Write(snapshot)
			return
		}
		var input struct {
			Keys []string `json:"keys"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &input)
		if len(input.Keys) != 1 || input.Keys[0] != "repositories.neptune.url" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"schema":"exocortex.register.resolution.v1","values":{"repositories.neptune.url":{"value":"https://github.com/example/live-neptune"}}}`))
	}))
	defer server2.Close()
	http.DefaultTransport = server2.Client().Transport
	host.KernelURL = server2.URL
	if err := config.SaveHost(runtime, host); err != nil {
		t.Fatal(err)
	}
	source, err = Resolve(runtime, "neptune")
	if err != nil || source.Origin != "kernel" || source.Repository != "https://github.com/example/live-neptune" {
		t.Fatalf("scoped live Kernel source was not selected: %+v %v", source, err)
	}
}
