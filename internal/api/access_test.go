package api

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"updater/internal/config"
	"updater/internal/engine"
	"updater/internal/model"
	"updater/internal/state"
)

func TestJobAndHostRecoveryStayInsideHeadBoundary(t *testing.T) {
	root := t.TempDir()
	runtime := config.Runtime{StateDir: root, RegistryPath: filepath.Join(root, "heads.json")}
	for _, id := range []string{"kernel", "saturn"} {
		filename := filepath.Join(root, id+".env")
		content := "KERNEL_URL=https://kernel.internal\nKERNEL_SERVICE_TOKEN=machine\nUPDATER_SERVICE_ID=" + id + "\nUPDATER_COMPOSE_PROJECT_DIR=/opt/exocortex\nUPDATER_COMPOSE_SERVICE=" + id + "\nUPDATER_CONTAINER_NAME=" + id + "\nUPDATER_IMAGE_VARIABLE=IMAGE\nUPDATER_VERSION_VARIABLE=VERSION\nVERSION=1.0.0\nUPDATER_LOCAL_HEALTH_URL=http://127.0.0.1/health\nUPDATER_CONTROL_TOKEN=" + id + "-token\n"
		if err := os.WriteFile(filename, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if err := config.RegisterHead(runtime.RegistryPath, id, filename); err != nil {
			t.Fatal(err)
		}
	}
	store, _ := state.New(root)
	now := time.Now().UTC()
	_ = store.Save(model.Job{ID: "private-job", HeadID: "saturn", Service: "saturn", State: "COMPLETED", FinishedAt: &now})
	server := Server{Runtime: runtime, Store: store, Engine: engine.New(runtime, store, nil)}
	handler := server.Handler()
	for _, item := range []struct {
		method, path, token, body string
		status                    int
	}{
		{"GET", "/v1/jobs/private-job", "kernel-token", "", 401},
		{"GET", "/v1/jobs/private-job", "saturn-token", "", 200},
		{"GET", "/v1/jobs", "kernel-token", "", 401},
		{"POST", "/v1/lifecycle/gryphon-initialization", "kernel-token", `{"head_id":"kernel"}`, 403},
		{"POST", "/v1/host-recovery/export", "saturn-token", `{"head_id":"saturn","passphrase":"synthetic recovery password"}`, 403},
	} {
		request := httptest.NewRequest(item.method, "http://updater.local"+item.path, strings.NewReader(item.body))
		request.Header.Set("X-Updater-Token", item.token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != item.status {
			t.Errorf("%s got %d expected %d", item.path, response.Code, item.status)
		}
	}
}
