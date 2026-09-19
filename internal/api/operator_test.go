package api

import (
	"context"
	"encoding/json"
	"net/http"
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

func operatorFixture(t *testing.T) Server {
	t.Helper()
	dir := t.TempDir()
	runtime := config.Runtime{StateDir: dir, RegistryPath: filepath.Join(dir, "heads.json")}
	for _, id := range []string{"kernel", "saturn"} {
		filename := filepath.Join(dir, id+".env")
		body := "KERNEL_URL=https://kernel.invalid\nKERNEL_SERVICE_TOKEN=never-disclose-machine\nUPDATER_SERVICE_ID=" + id + "\nUPDATER_COMPOSE_PROJECT_DIR=/opt/exocortex\nUPDATER_COMPOSE_SERVICE=" + id + "\nUPDATER_CONTAINER_NAME=" + id + "\nUPDATER_IMAGE_VARIABLE=IMAGE\nUPDATER_VERSION_VARIABLE=VERSION\nVERSION=1.0.0\nUPDATER_LOCAL_HEALTH_URL=http://127.0.0.1:18180/api/health\nUPDATER_CONTROL_TOKEN=never-disclose-control\n"
		if err := os.WriteFile(filename, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := config.RegisterHead(runtime.RegistryPath, id, filename); err != nil {
			t.Fatal(err)
		}
	}
	store, err := state.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return Server{Version: "0.5.0", Runtime: runtime, Store: store, Engine: engine.New(runtime, store, nil)}
}

func TestOperatorRoutesAreAbsentFromServiceListener(t *testing.T) {
	s := operatorFixture(t)
	for _, path := range []string{"/v1/overview", "/v1/actions", "/v1/check", "/v1/bots"} {
		req := httptest.NewRequest("GET", "http://updater.local"+path, nil)
		req.Header.Set("X-Updater-Token", "never-disclose-control")
		res := httptest.NewRecorder()
		s.Handler().ServeHTTP(res, req)
		if res.Code != 404 {
			t.Fatalf("operator route exposed on service listener: %s = %d", path, res.Code)
		}
	}
}

func TestOperatorRejectsInvalidActionsBeforeDispatch(t *testing.T) {
	s := operatorFixture(t)
	for _, body := range []string{
		`{"component":"wyvern","head_id":"kernel","kind":"install","request_id":"tui-request-123456"}`,
		`{"component":"gryphon","head_id":"kernel","kind":"install","request_id":"tui-request-123456"}`,
		`{"component":"updater","head_id":"kernel","kind":"shell","request_id":"tui-request-123456"}`,
		`{"component":"updater","head_id":"kernel","kind":"update","version":"latest","request_id":"tui-request-123456"}`,
		`{"component":"updater","head_id":"kernel","kind":"update","version":"0.5.1","bot_token":"never-disclose","request_id":"tui-request-123456"}`,
		`{"component":"gryphon","head_id":"saturn","kind":"connect-bot","alias":"bad alias","bot_token":"secret","request_id":"tui-request-123456"}`,
		`{"head_id":"kernel","path":"/bin/sh"}`,
		`{} {}`,
		strings.Repeat("x", 17000),
	} {
		res := httptest.NewRecorder()
		s.operatorHandler().ServeHTTP(res, httptest.NewRequest("POST", "http://updater.local/v1/actions", strings.NewReader(body)))
		if res.Code != 400 {
			t.Fatalf("invalid input returned %d: %s", res.Code, res.Body.String())
		}
		if strings.Contains(res.Body.String(), "never-disclose") {
			t.Fatal("credential echoed")
		}
	}
	if len(s.Store.List()) != 0 {
		t.Fatal("invalid input created jobs")
	}
}

func TestOperatorSnapshotWhitelistsMetadataAndBotCredentials(t *testing.T) {
	s := operatorFixture(t)
	now := time.Now().UTC()
	if err := s.Store.Save(model.Job{ID: "test-job", RequestID: "test-request-123456", HeadID: "saturn", Service: "gryphon-bot", State: "FAILED", Message: "token=never-disclose-token\x1b]52;c;c2VjcmV0\a", DeploymentSnapshot: "never-disclose-env", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	snapshot := s.operatorSnapshot(ctx)
	body, _ := json.Marshal(snapshot)
	if strings.Contains(string(body), "never-disclose") || strings.Contains(string(body), "c2VjcmV0") {
		t.Fatalf("private metadata exposed: %s", body)
	}
	if len(snapshot.Heads) != 2 || len(snapshot.Jobs) != 1 {
		t.Fatal("missing public operator metadata")
	}
	if snapshot.Heads[0].ID != "kernel" || len(snapshot.Heads[0].Helpers) != 1 {
		t.Fatal("helper eligibility incorrect")
	}
}

func TestOperatorLifecycleRetryReturnsExistingJobWithoutMutation(t *testing.T) {
	s := operatorFixture(t)
	now := time.Now().UTC()
	job := model.Job{ID: "already-accepted", RequestID: "tui-operation-123456", HeadID: "saturn", Service: "gryphon-initialization", State: "COMPLETED", CreatedAt: now, UpdatedAt: now}
	if err := s.Store.Save(job); err != nil {
		t.Fatal(err)
	}
	body := `{"component":"gryphon","head_id":"saturn","kind":"install","request_id":"tui-operation-123456"}`
	for range 2 {
		res := httptest.NewRecorder()
		s.operatorHandler().ServeHTTP(res, httptest.NewRequest("POST", "http://updater.local/v1/actions", strings.NewReader(body)))
		if res.Code != 200 || !strings.Contains(res.Body.String(), job.ID) {
			t.Fatalf("retry failed: %d %s", res.Code, res.Body.String())
		}
	}
	if len(s.Store.List()) != 1 {
		t.Fatal("retry duplicated a job")
	}
	// A service-scoped caller still needs its own credential on the same operation.
	res := httptest.NewRecorder()
	s.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "http://updater.local/v1/lifecycle/neptune-installation", strings.NewReader(`{"head_id":"kernel"}`)))
	if res.Code != 401 {
		t.Fatal("helper installation bypassed service authorization")
	}
}
