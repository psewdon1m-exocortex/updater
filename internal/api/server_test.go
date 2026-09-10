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
	"updater/internal/model"
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
		func(context.Context, string, string, string, string) (release.Resolved, error) {
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

	request = httptest.NewRequest(
		http.MethodPost,
		"http://updater.local/v1/components/gryphon-linux/check",
		strings.NewReader(`{"head_id":"kernel","current_version":"1.2.3"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected unauthenticated Gryphon check status: %d", response.Code)
	}

	request = httptest.NewRequest(
		http.MethodPost,
		"http://updater.local/v1/components/neptune-linux/initialize",
		strings.NewReader(`{"request_id":"init-one","head_id":"kernel","project_id":"kernel","export_url":"http://127.0.0.1:18180/api/internal/neptune/backup","enrollment_code":"01234567890123456789012345678901"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected unauthenticated Neptune initialization status: %d", response.Code)
	}

	request = httptest.NewRequest(
		http.MethodPost,
		"http://updater.local/v1/components/neptune-linux/initialize",
		strings.NewReader(`{"request_id":"init-two","head_id":"kernel","project_id":"kernel","export_url":"https://external.example/backup","enrollment_code":"not-a-valid-code"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Updater-Token", "head-secret")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unexpected invalid Neptune initialization status: %d", response.Code)
	}

	initializationJob := model.Job{
		ID: "neptune-1-0123456789abcdef", RequestID: "init-status", HeadID: "kernel",
		Service: "neptune-initialization", State: "COMPLETED", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := store.Save(initializationJob); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "http://updater.local/v1/components/neptune-linux/initializations/neptune-1-0123456789abcdef?head_id=kernel", nil)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected unauthenticated Neptune initialization job status: %d", response.Code)
	}
	request.Header.Set("X-Updater-Token", "head-secret")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected authenticated Neptune initialization job status: %d (%s)", response.Code, response.Body.String())
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

func TestNeptuneAgentUpdateRequiresBridgeToken(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "neptune-agent.token")
	if err := os.WriteFile(tokenPath, []byte("bridge-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEPTUNE_UPDATER_TOKEN_FILE", tokenPath)
	runtime := config.Runtime{StateDir: dir, RegistryPath: filepath.Join(dir, "heads.json"), DryRun: true}
	store, _ := state.New(dir)
	server := Server{Version: "1", Runtime: runtime, Store: store, Engine: engine.New(runtime, store, nil)}
	payload := `{"head_id":"kernel","version":"1.2.3"}`

	request := httptest.NewRequest(http.MethodPost, "http://updater.local/v1/agent/neptune-linux/update", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected unauthenticated status: %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "http://updater.local/v1/agent/neptune-linux/update", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Neptune-Updater-Token", "wrong-secret")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected invalid-token status: %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "http://updater.local/v1/agent/neptune-linux/update", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Neptune-Updater-Token", "bridge-secret")
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code == http.StatusUnauthorized {
		t.Fatalf("the configured bridge token was rejected: %s", response.Body.String())
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
