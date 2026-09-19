package api

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"updater/internal/console"
)

func wyvernFixture(t *testing.T) (Server, *atomic.Int32) {
	t.Helper()
	s := operatorFixture(t)
	s.WyvernSocket = filepath.Join(t.TempDir(), "w.sock")
	listener, err := net.Listen("unix", s.WyvernSocket)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/catalog" {
			_, _ = w.Write([]byte(`{"schema":"exocortex.wyvern.catalog.v1","generation":"fixture","api_key":"never-disclose","adapters":[{"adapter_id":"google","name":"Google","driver":"google","enabled":true,"credential_ref":"never-disclose","profiles":[]}],"clients":[{"client_id":"mastermind","enabled":true,"token_sha256":"never-disclose","allowed_adapters":["google"],"bindings":{}}]}`))
			return
		}
		if r.Method == "POST" {
			calls.Add(1)
		}
		drain := r.URL.Path == "/v1/drain"
		state := "ready"
		if drain {
			state = "draining"
		}
		_ = json.NewEncoder(w).Encode(console.WyvernStatus{Schema: "exocortex.wyvern.status.v1", Service: "wyvern", Version: "0.0.1", APIVersion: 1, InstanceID: "host", Generation: "fixture", State: state, ConfigurationLoaded: true, Ready: !drain, Drain: drain})
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return s, &calls
}

func TestWyvernOperatorCatalogWhitelistsAndServiceRoutesDeny(t *testing.T) {
	s, calls := wyvernFixture(t)
	response := httptest.NewRecorder()
	s.operatorHandler().ServeHTTP(response, httptest.NewRequest("GET", "http://updater.local/v1/wyvern", nil))
	if response.Code != 200 || strings.Contains(response.Body.String(), "never-disclose") || !strings.Contains(response.Body.String(), "Google") {
		t.Fatalf("invalid catalog: %d %s", response.Code, response.Body.String())
	}
	for _, pair := range [][2]string{{"GET", "/v1/wyvern"}, {"POST", "/v1/actions"}, {"POST", "/v1/lifecycle/wyvern-drain"}} {
		response := httptest.NewRecorder()
		req := httptest.NewRequest(pair[0], "http://updater.local"+pair[1], strings.NewReader(`{"component":"wyvern","kind":"drain","request_id":"drain-request-123456"}`))
		req.Header.Set("X-Updater-Token", "never-disclose-control")
		s.Handler().ServeHTTP(response, req)
		if response.Code != 404 {
			t.Fatalf("service listener exposed %s: %d", pair[1], response.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("observation or service request mutated Wyvern")
	}
}

func TestWyvernHostActionPersistsAndRetriesDoNotRepeat(t *testing.T) {
	s, calls := wyvernFixture(t)
	body := `{"component":"wyvern","kind":"drain","request_id":"drain-request-123456"}`
	response := httptest.NewRecorder()
	s.operatorHandler().ServeHTTP(response, httptest.NewRequest("POST", "http://updater.local/v1/actions", strings.NewReader(body)))
	if response.Code != 202 {
		t.Fatalf("action: %d %s", response.Code, response.Body.String())
	}
	var job console.Job
	if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		current, _ := s.Store.Get(job.ID)
		if current.FinishedAt != nil {
			if current.State != "COMPLETED" {
				t.Fatal(current.State)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for i := 0; i < 2; i++ {
		response := httptest.NewRecorder()
		s.operatorHandler().ServeHTTP(response, httptest.NewRequest("POST", "http://updater.local/v1/actions", strings.NewReader(body)))
		if response.Code != 200 || !strings.Contains(response.Body.String(), job.ID) {
			t.Fatal("request ID retry lost receipt")
		}
	}
	if calls.Load() != 1 || len(s.Store.List()) != 1 {
		t.Fatal("operation duplicated")
	}
	conflict := httptest.NewRecorder()
	s.operatorHandler().ServeHTTP(conflict, httptest.NewRequest("POST", "http://updater.local/v1/actions", strings.NewReader(strings.Replace(body, "drain\"", "reload\"", 1))))
	if conflict.Code != 409 {
		t.Fatal("request ID accepted a different operation")
	}
}

func TestWyvernUnexpectedFieldsCannotAcquireHostControl(t *testing.T) {
	s := operatorFixture(t)
	for _, extra := range []string{`,"head_id":"kernel"`, `,"version":"0.0.1"`, `,"bot_token":"secret"`, `,"socket":"/tmp/evil.sock"`} {
		response := httptest.NewRecorder()
		body := `{"component":"wyvern","kind":"drain","request_id":"drain-request-123456"` + extra + `}`
		s.operatorHandler().ServeHTTP(response, httptest.NewRequest("POST", "http://updater.local/v1/actions", strings.NewReader(body)))
		if response.Code != 400 {
			t.Fatal("unexpected fields accepted")
		}
	}
	if len(s.Store.List()) != 0 {
		t.Fatal("invalid actions persisted")
	}
}
