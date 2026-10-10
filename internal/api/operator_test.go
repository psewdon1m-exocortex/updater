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
	"updater/internal/console"
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

func TestSharedGryphonUpdateDoesNotExposeAnArbitraryReleaseService(t *testing.T) {
	now := time.Now().UTC()
	job := operatorJob(model.Job{ID: "gryphon-update-1", HeadID: "chronos", Service: "gryphon-update", State: "COMPLETED", CreatedAt: now, UpdatedAt: now})
	if job.HeadID != "" || job.Component != "gryphon" {
		t.Fatalf("shared Gryphon update exposed an arbitrary service: %+v", job)
	}
}

func TestSharedWyvernUpdateDoesNotExposeAnArbitraryReleaseService(t *testing.T) {
	now := time.Now().UTC()
	job := operatorJob(model.Job{ID: "wyvern-update-1", HeadID: "laboratory", Service: "wyvern-update", State: "COMPLETED", CreatedAt: now, UpdatedAt: now})
	if job.HeadID != "" || job.Component != "wyvern" {
		t.Fatalf("shared Wyvern update exposed a release service: %+v", job)
	}
}

func TestSharedUpdaterUpdateDoesNotExposeAnArbitraryReleaseService(t *testing.T) {
	now := time.Now().UTC()
	job := operatorJob(model.Job{ID: "updater-update-1", HeadID: "kernel", Service: "updater-self-update", State: "COMPLETED", CreatedAt: now, UpdatedAt: now})
	if job.HeadID != "" || job.Component != "updater" {
		t.Fatalf("shared Updater update exposed an arbitrary service: %+v", job)
	}
}

func TestSharedGatewayReleaseRoutesRejectServiceSelection(t *testing.T) {
	s := operatorFixture(t)
	for _, item := range []struct{ path, body string }{
		{"/v1/check", `{"component":"gryphon","head_id":"saturn"}`},
		{"/v1/actions", `{"component":"gryphon","kind":"update","head_id":"saturn","version":"1.2.3","request_id":"tui-request-123456"}`},
		{"/v1/check", `{"component":"wyvern","head_id":"laboratory"}`},
		{"/v1/actions", `{"component":"wyvern","kind":"update","head_id":"laboratory","version":"1.2.3","request_id":"tui-request-123456"}`},
		{"/v1/check", `{"component":"updater","head_id":"kernel"}`},
		{"/v1/actions", `{"component":"updater","kind":"update","head_id":"kernel","version":"1.2.3","request_id":"tui-request-123456"}`},
	} {
		res := httptest.NewRecorder()
		s.operatorHandler().ServeHTTP(res, httptest.NewRequest("POST", "http://updater.local"+item.path, strings.NewReader(item.body)))
		if res.Code != 400 {
			t.Fatalf("%s selected a service for a shared gateway: %d", item.path, res.Code)
		}
	}
}

func TestOperatorReleaseCheckReportsSourceFailure(t *testing.T) {
	s := operatorFixture(t)
	request := httptest.NewRequest(http.MethodPost, "http://updater.local/v1/check", strings.NewReader(`{"component":"updater"}`))
	response := httptest.NewRecorder()
	s.operatorHandler().ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "updater release source is unavailable") {
		t.Fatalf("operator lost the actionable release-source error: %d %s", response.Code, response.Body.String())
	}
}

func TestOperatorUpdaterUpdateRetainsHostReleasePath(t *testing.T) {
	s := operatorFixture(t)
	request := httptest.NewRequest(http.MethodPost, "http://updater.local/v1/actions", strings.NewReader(`{"component":"updater","kind":"update","version":"0.5.1","request_id":"tui-request-123456"}`))
	response := httptest.NewRecorder()
	s.operatorHandler().ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("host update did not reach release discovery: %d %s", response.Code, response.Body.String())
	}
}

func TestRecoveryStorageConfigurationExistsOnlyOnRootOperatorAPI(t *testing.T) {
	s := operatorFixture(t)
	s.RecoveryEnroll = func(_ context.Context, _ string, codes map[string]string) (map[string]config.RecoveryIdentity, error) {
		identities := map[string]config.RecoveryIdentity{}
		if len(codes) != 1 {
			t.Fatal("operator combined independent recovery setup codes", codes)
		}
		for service, code := range codes {
			if len(code) != 32 {
				t.Fatal("operator did not pass the bounded setup code", service)
			}
			identities[service] = config.RecoveryIdentity{Slug: service + "-server", Token: strings.Repeat(service[:1], 43)}
		}
		return identities, nil
	}
	services := []string{"updater", "neptune", "gryphon", "wyvern"}
	var bodies []string
	for _, service := range services {
		body, err := json.Marshal(console.Action{
			Component: service, Kind: "recovery-configure", RequestID: "tui-recovery-" + service + "-123456",
			Recovery: &console.RecoveryInput{GatewayURL: "https://saturn.example", Service: service, EnrollmentCodes: map[string]string{service: strings.Repeat(service[:1], 32)}},
		})
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, string(body))
	}
	for _, body := range bodies {
		response := httptest.NewRecorder()
		s.operatorHandler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "http://updater.local/v1/actions", strings.NewReader(body)))
		if response.Code != http.StatusOK {
			t.Fatalf("root recovery configuration failed or disclosed a token: %d %s", response.Code, response.Body.String())
		}
		for _, service := range services {
			if strings.Contains(response.Body.String(), strings.Repeat(service[:1], 6)) {
				t.Fatal("root recovery configuration disclosed a token", service)
			}
		}
	}
	snapshot := s.operatorSnapshot(context.Background())
	if !snapshot.RecoveryConfigured || snapshot.RecoveryGatewayURL != "https://saturn.example" || len(snapshot.RecoveryServices) != len(services) {
		t.Fatalf("recovery configuration is not observable without secrets: %+v", snapshot)
	}
	for _, service := range services {
		info, err := os.Stat(filepath.Join(filepath.Dir(config.HostConfigFile(s.Runtime)), "recovery-tokens", service+".token"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private %s token file is missing: %v", service, err)
		}
	}
	serviceResponse := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "http://updater.local/v1/actions", strings.NewReader(bodies[0]))
	request.Header.Set("X-Updater-Token", "never-disclose-control")
	s.Handler().ServeHTTP(serviceResponse, request)
	if serviceResponse.Code != http.StatusNotFound {
		t.Fatalf("root recovery action leaked to service listener: %d", serviceResponse.Code)
	}
}

func TestRecoveryKeyExportNeverReturnsTheKeyAndIsPrivateToRoot(t *testing.T) {
	for _, service := range []string{"updater", "neptune", "gryphon", "wyvern"} {
		t.Run(service, func(t *testing.T) { testRecoveryKeyExport(t, service) })
	}
}

func testRecoveryKeyExport(t *testing.T, service string) {
	t.Helper()
	s := operatorFixture(t)
	destination := filepath.Join(t.TempDir(), "offline-"+service+"-key.json")
	body, _ := json.Marshal(map[string]any{"component": service, "kind": "recovery-key-export", "request_id": "tui-key-export-123456", "recovery": map[string]string{"service": service, "key_path": destination}})
	response := httptest.NewRecorder()
	s.operatorHandler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "http://updater.local/v1/actions", strings.NewReader(string(body))))
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	key, err := config.ManagedRecoveryKey(s.Runtime, service, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(response.Body.String(), key) {
		t.Fatal("API disclosed the recovery key")
	}
	exported, err := config.ReadRecoveryKey(destination, service)
	if err != nil || exported != key {
		t.Fatal("private export does not contain the service key", err)
	}
	serviceResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(serviceResponse, httptest.NewRequest(http.MethodPost, "http://updater.local/v1/actions", strings.NewReader(string(body))))
	if serviceResponse.Code == http.StatusOK || serviceResponse.Code == http.StatusAccepted {
		t.Fatal("service API exported a host key")
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
