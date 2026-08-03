package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"updater/internal/config"
	"updater/internal/engine"
	"updater/internal/kernel"
	"updater/internal/release"
	"updater/internal/state"
)

func TestHealthAndInvalidUpdate(t *testing.T) {
	dir := t.TempDir()
	runtime := config.Runtime{StateDir: dir, RegistryPath: filepath.Join(dir, "heads.json")}
	store, err := state.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	server := Server{Version: "1.2.3", Runtime: runtime, Store: store, Engine: engine.New(runtime, store, nil)}
	request := httptest.NewRequest(http.MethodGet, "http://updater.local/v1/health", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected health status: %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "http://updater.local/v1/updates", nil)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected invalid update status: %d", response.Code)
	}
}

func TestUpdateRequiresTheRegisteredHeadToken(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "kernel.env")
	err := os.WriteFile(envPath, []byte(`KERNEL_URL=https://kernel.internal
KERNEL_SERVICE_TOKEN=kernel-token
UPDATER_SERVICE_ID=kernel
UPDATER_COMPOSE_PROJECT_DIR=/opt/exocortex/kernel
UPDATER_COMPOSE_SERVICE=kernel
UPDATER_CONTAINER_NAME=exocortex-kernel
UPDATER_IMAGE_VARIABLE=KERNEL_IMAGE
UPDATER_VERSION_VARIABLE=KERNEL_VERSION
KERNEL_VERSION=1.1.0
UPDATER_LOCAL_HEALTH_URL=http://127.0.0.1:18180/api/health
UPDATER_CONTROL_TOKEN=head-secret
`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	runtime := config.Runtime{
		StateDir: dir, RegistryPath: filepath.Join(dir, "heads.json"), DryRun: true,
	}
	if err := config.RegisterHead(runtime.RegistryPath, "kernel", envPath); err != nil {
		t.Fatal(err)
	}
	store, _ := state.New(dir)
	instance := engine.New(runtime, store, nil)
	instance.SetTestDependencies(
		func(string, string, string, time.Duration) (kernel.Snapshot, error) {
			return kernel.Snapshot{Values: map[string]interface{}{
				"repositories": map[string]interface{}{"kernel": map[string]interface{}{"url": "https://github.com/example/platform"}},
			}}, nil
		},
		func(context.Context, string, string, string, string, bool) (release.Resolved, error) {
			var resolved release.Resolved
			resolved.Manifest.Service = "kernel"
			resolved.Manifest.Version = "1.0.0"
			return resolved, nil
		},
	)
	server := Server{Version: "1", Runtime: runtime, Store: store, Engine: instance}
	backup := []byte("backup")
	checksum := sha256.Sum256(backup)
	payload := `{"request_id":"one","head_id":"kernel","service":"kernel","version":"1.0.0","backup":{"filename":"backup.json","sha256":"` +
		hex.EncodeToString(checksum[:]) + `","data_base64":"` + base64.StdEncoding.EncodeToString(backup) + `"}}`

	request := httptest.NewRequest(http.MethodPost, "http://updater.local/v1/updates", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected unauthenticated status: %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "http://updater.local/v1/updates", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Updater-Token", "head-secret")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("unexpected authenticated status: %d (%s)", response.Code, response.Body.String())
	}
	deadline := time.Now().Add(time.Second)
	for instance.Busy() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}

func TestRejectsUnexpectedHost(t *testing.T) {
	dir := t.TempDir()
	runtime := config.Runtime{StateDir: dir, RegistryPath: filepath.Join(dir, "heads.json")}
	store, _ := state.New(dir)
	server := Server{Version: "1", Runtime: runtime, Store: store, Engine: engine.New(runtime, store, nil)}
	request := httptest.NewRequest(http.MethodGet, "http://external.example/v1/health", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status: %d", response.Code)
	}
}

func TestSecondUpdaterCannotReplaceAnActiveUnixSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "updater.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	server := Server{
		Runtime: config.Runtime{SocketPath: socketPath},
	}
	err = server.ListenAndServe()
	if err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Fatalf("expected active-socket collision, got %v", err)
	}

	connection, err := net.DialTimeout("unix", socketPath, time.Second)
	if err != nil {
		t.Fatalf("the original updater socket was replaced: %v", err)
	}
	_ = connection.Close()
}
