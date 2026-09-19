package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"updater/internal/config"
	"updater/internal/model"
	"updater/internal/state"
)

func TestComponentJobSurvivesRestartAndRetriesAreScoped(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, "kernel.env")
	body := `KERNEL_URL=http://127.0.0.1:18180
KERNEL_SERVICE_TOKEN=synthetic-kernel-token
UPDATER_SERVICE_ID=kernel
UPDATER_COMPOSE_PROJECT_DIR=/opt/exocortex/kernel
UPDATER_COMPOSE_SERVICE=kernel
UPDATER_CONTAINER_NAME=exocortex-kernel
UPDATER_IMAGE_VARIABLE=KERNEL_IMAGE
UPDATER_VERSION_VARIABLE=KERNEL_VERSION
KERNEL_VERSION=1.1.0
UPDATER_LOCAL_HEALTH_URL=http://127.0.0.1:18180/api/health
UPDATER_CONTROL_TOKEN=synthetic-control-token
`
	if err := os.WriteFile(env, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	runtime := config.Runtime{StateDir: dir, RegistryPath: filepath.Join(dir, "heads.json")}
	if err := config.RegisterHead(runtime.RegistryPath, "kernel", env); err != nil {
		t.Fatal(err)
	}
	store, _ := state.New(dir)
	now := time.Now()
	if err := store.Save(model.Job{ID: "durable-helper-job", RequestID: "component-request-123", HeadID: "kernel", Service: "neptune-update", Version: "0.1.8", State: "COMPLETED", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	store, _ = state.New(dir)
	handler := Server{Version: "0.5.0", Runtime: runtime, Store: store}.Handler()
	for _, item := range []struct {
		token, version string
		status         int
	}{{"", "0.1.8", 401}, {"synthetic-control-token", "0.1.8", 200}, {"synthetic-control-token", "0.1.9", 409}} {
		request := httptest.NewRequest(http.MethodPost, "http://updater.local/v2/components/neptune/updates", strings.NewReader(`{"head_id":"kernel","request_id":"component-request-123","version":"`+item.version+`"}`))
		request.Header.Set("X-Updater-Token", item.token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != item.status {
			t.Fatalf("%d want %d: %s", response.Code, item.status, response.Body.String())
		}
		if item.status == 200 && (!strings.Contains(response.Body.String(), "COMPLETED") || strings.Contains(response.Body.String(), "backup")) {
			t.Fatal("job status lost or backup unexpectedly required", response.Body.String())
		}
	}
	if len(store.List()) != 1 {
		t.Fatal("retry created a duplicate")
	}
	request := httptest.NewRequest(http.MethodPost, "http://updater.local/v2/components/wyvern/updates", strings.NewReader(`{"head_id":"kernel","request_id":"wyvern-client-update-123","version":"0.0.2"}`))
	request.Header.Set("X-Updater-Token", "synthetic-control-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 403 || len(store.List()) != 1 {
		t.Fatal("consumer can update the shared gateway", response.Code)
	}
}
