package api

import (
	"context"
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

func TestComponentJobSurvivesRestartButServiceCannotRetryHostUpdate(t *testing.T) {
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
	}{{"", "0.1.8", 401}, {"synthetic-control-token", "0.1.8", 403}, {"synthetic-control-token", "0.1.9", 403}} {
		request := httptest.NewRequest(http.MethodPost, "http://updater.local/v2/components/neptune/updates", strings.NewReader(`{"head_id":"kernel","request_id":"component-request-123","version":"`+item.version+`"}`))
		request.Header.Set("X-Updater-Token", item.token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != item.status {
			t.Fatalf("%d want %d: %s", response.Code, item.status, response.Body.String())
		}
	}
	status := httptest.NewRequest(http.MethodGet, "http://updater.local/v1/jobs/durable-helper-job", nil)
	status.Header.Set("X-Updater-Token", "synthetic-control-token")
	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, status)
	if statusResponse.Code != 200 || !strings.Contains(statusResponse.Body.String(), "COMPLETED") {
		t.Fatal("existing job status is no longer readable", statusResponse.Code, statusResponse.Body.String())
	}
	if len(store.List()) != 1 {
		t.Fatal("retry created a duplicate")
	}
	if err := store.Save(model.Job{ID: "host-job", RequestID: "host-request-123456", Service: "neptune-update", Version: "0.1.8", State: "COMPLETED", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	host := httptest.NewRequest(http.MethodPost, "http://updater.local/v2/components/neptune/updates",
		strings.NewReader(`{"request_id":"host-request-123456","version":"0.1.8"}`))
	host = host.WithContext(context.WithValue(host.Context(), operatorDispatchKey{}, true))
	hostResponse := httptest.NewRecorder()
	handler.ServeHTTP(hostResponse, host)
	if hostResponse.Code != 200 || !strings.Contains(hostResponse.Body.String(), "COMPLETED") {
		t.Fatal("root TUI dispatch cannot read its existing Neptune job", hostResponse.Code, hostResponse.Body.String())
	}
	request := httptest.NewRequest(http.MethodPost, "http://updater.local/v2/components/wyvern/updates", strings.NewReader(`{"head_id":"kernel","request_id":"wyvern-client-update-123","version":"0.0.2"}`))
	request.Header.Set("X-Updater-Token", "synthetic-control-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 403 || len(store.List()) != 2 {
		t.Fatal("consumer can update the shared gateway", response.Code)
	}
	labEnv := filepath.Join(dir, "laboratory.env")
	if err := os.WriteFile(labEnv, []byte(strings.ReplaceAll(body, "kernel", "laboratory")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := config.RegisterHead(runtime.RegistryPath, "laboratory", labEnv); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(model.Job{ID: "wyvern-durable", RequestID: "wyvern-approved-request", HeadID: "laboratory", Service: "wyvern-update", Version: "0.0.2", State: "COMPLETED", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		token        string
		confirmation bool
		status       int
	}{{"", true, 401}, {"synthetic-control-token", false, 403}, {"synthetic-control-token", true, 403}} {
		confirmation := "false"
		if item.confirmation {
			confirmation = "true"
		}
		request := httptest.NewRequest(http.MethodPost, "http://updater.local/v2/components/wyvern/updates",
			strings.NewReader(`{"head_id":"laboratory","request_id":"wyvern-approved-request","version":"0.0.2","confirm_shared":`+confirmation+"}"))
		request.Header.Set("X-Updater-Token", item.token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != item.status {
			t.Fatalf("shared update authorization: %d want %d: %s", response.Code, item.status, response.Body.String())
		}
	}
	if len(store.List()) != 3 {
		t.Fatal("shared update retry created a duplicate job")
	}
}
