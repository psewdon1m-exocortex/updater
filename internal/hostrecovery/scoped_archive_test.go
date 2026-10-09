package hostrecovery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestScopedArchiveIsolationAuthenticationAndCollection(t *testing.T) {
	password := "synthetic scoped recovery passphrase"
	entry := Entry{Name: "var/lib/gryphon/state.json", Data: []byte(`{"state":"ready"}`)}
	archive, err := SealScope([]Entry{entry}, password, "gryphon")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(archive, entry.Data) {
		t.Fatal("scoped archive exposed plaintext")
	}
	if scope, err := InspectScope(archive); err != nil || scope != "gryphon" {
		t.Fatal("scope header is unavailable", scope, err)
	}
	if _, err := OpenScope(archive, password, "neptune"); err == nil {
		t.Fatal("cross-service restore was accepted")
	}
	if _, err := OpenScope(archive, "wrong scoped recovery passphrase", "gryphon"); err == nil {
		t.Fatal("wrong passphrase was accepted")
	}
	tampered := bytes.Clone(archive)
	tampered[len(tampered)-1] ^= 1
	if _, err := OpenScope(tampered, password, "gryphon"); err == nil {
		t.Fatal("tampered scoped archive was accepted")
	}
	if _, err := SealScope([]Entry{{Name: "var/lib/neptune/projects.json", Data: []byte(`{}`)}}, password, "gryphon"); err == nil {
		t.Fatal("cross-scope member was accepted")
	}

	root := t.TempDir()
	for name, body := range map[string]string{
		"var/lib/updater/jobs/complete.json": `{"id":"complete","head_id":"saturn","service":"saturn"}`,
		"var/lib/neptune/projects.json":      `{"projects":[]}`,
		"var/lib/gryphon/state.json":         `{"state":"ready"}`,
		"var/lib/wyvern/state.json":          `{"state":"ready"}`,
	} {
		filename := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := CollectScope(root, "neptune")
	if err != nil || len(entries) != 1 || entries[0].Name != "var/lib/neptune/projects.json" {
		t.Fatal("scope collection crossed a service boundary", entries, err)
	}
	empty, err := SealScope(nil, password, "wyvern")
	if err != nil {
		t.Fatal("empty scoped state was not representable", err)
	}
	opened, err := OpenScope(empty, password, "wyvern")
	if err != nil || len(opened) != 0 {
		t.Fatal("empty scoped archive did not round-trip", opened, err)
	}
}

func TestUpdaterScopeExcludesActiveOperationsButRetainsFinishedHistory(t *testing.T) {
	root := t.TempDir()
	jobs := map[string]string{
		"active.json":   `{"id":"active","service":"host-recovery-export","state":"INSTALLING"}`,
		"finished.json": `{"id":"finished","service":"host-recovery-export","state":"COMPLETED","finished_at":"2026-10-02T20:30:00Z"}`,
		"pending.json":  `{"id":"pending","head_id":"saturn","service":"saturn","state":"ROLLBACK_FAILED","finished_at":"2026-10-02T20:30:00Z","recovery_pending":true}`,
	}
	for name, body := range jobs {
		filename := filepath.Join(root, "var/lib/updater/jobs", name)
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := CollectScope(root, "updater")
	if err != nil || len(entries) != 1 || entries[0].Name != "var/lib/updater/jobs/finished.json" {
		t.Fatal("Updater recovery included an unfinished operation", entries, err)
	}
	if _, err := SealScope(entries, "synthetic scoped recovery passphrase", "updater"); err != nil {
		t.Fatal("finished host-owned history was rejected", err)
	}
}

func TestApplyScopedReplacesOnlySelectedService(t *testing.T) {
	root := t.TempDir()
	initial := map[string]string{
		"var/lib/updater/jobs/complete.json": `{"id":"complete","head_id":"saturn","service":"saturn"}`,
		"var/lib/neptune/projects.json":      `{"projects":["old"]}`,
		"var/lib/gryphon/state.json":         `{"state":"must-survive"}`,
		"var/lib/wyvern/state.json":          `{"state":"must-survive"}`,
	}
	for name, body := range initial {
		filename := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	entries := []Entry{{Name: "var/lib/neptune/projects.json", Data: []byte(`{"projects":["restored"]}`)}}
	if err := ApplyScoped(root, entries, "neptune", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"var/lib/updater/jobs/complete.json", "var/lib/gryphon/state.json", "var/lib/wyvern/state.json"} {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil || string(body) != initial[name] {
			t.Fatalf("scoped Neptune restore changed %s: %q, %v", name, body, err)
		}
	}
	restored, err := os.ReadFile(filepath.Join(root, "var/lib/neptune/projects.json"))
	if err != nil || string(restored) != `{"projects":["restored"]}` {
		t.Fatal("selected service was not restored", string(restored), err)
	}
	if err := ApplyScoped(root, []Entry{{Name: "var/lib/gryphon/state.json", Data: []byte(`{}`)}}, "neptune", func() error { return nil }); err == nil {
		t.Fatal("cross-service scoped restore was accepted")
	}
}

func TestGatewayPublisherCreatesOneNamespacePerService(t *testing.T) {
	tokens := map[string]string{}
	archives := map[string][]byte{}
	var lock sync.Mutex
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		lock.Lock()
		defer lock.Unlock()
		if request.Method == http.MethodGet && request.URL.Path == "/api/v1/backups/capabilities" {
			_ = json.NewEncoder(response).Encode(map[string]any{"schema": "saturn.backup-ingest.capabilities.v1", "maxChunkBytes": 5})
			return
		}
		parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
		if len(parts) < 5 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "backups" {
			http.NotFound(response, request)
			return
		}
		scope := parts[3]
		if request.Header.Get("Authorization") != "Bearer "+tokens[scope] {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		if request.Method == http.MethodPost && len(parts) == 5 && parts[4] == "runs" {
			_ = json.NewEncoder(response).Encode(map[string]any{"id": scope + "-run"})
			return
		}
		if len(parts) != 7 || parts[4] != "runs" || parts[5] != scope+"-run" {
			http.NotFound(response, request)
			return
		}
		if request.Method == http.MethodHead && parts[6] == "upload" {
			response.Header().Set("Upload-Offset", fmt.Sprint(len(archives[scope])))
			response.WriteHeader(http.StatusNoContent)
			return
		}
		if request.Method == http.MethodPatch && parts[6] == "upload" {
			body, _ := io.ReadAll(request.Body)
			archives[scope] = append(archives[scope], body...)
			response.WriteHeader(http.StatusNoContent)
			return
		}
		if request.Method == http.MethodPost && parts[6] == "complete" {
			digest := sha256.Sum256(archives[scope])
			_ = json.NewEncoder(response).Encode(map[string]any{"id": scope + "-run", "receipt": map[string]any{
				"logicalPath": "/backups/" + scope + "/2026/10/02/fixture.exorecovery", "sha256": hex.EncodeToString(digest[:]), "sizeBytes": len(archives[scope]),
			}})
			return
		}
		http.NotFound(response, request)
	}))
	defer server.Close()
	publisher := &gatewayPublisher{base: server.URL, client: server.Client(), chunk: 5}
	for _, scope := range RecoveryScopes {
		tokens[scope] = strings.Repeat(scope[:1], 43)
		payload := []byte("encrypted-" + scope + "-archive")
		if scope == "updater" {
			archives[scope] = bytes.Clone(payload[:5])
		}
		published, err := publisher.publish(t.Context(), ScopedArchive{Scope: scope, Bytes: payload}, scope, tokens[scope], "component-0123456789abcdef", "0.6.12", time.Date(2026, 10, 2, 20, 30, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(scope, err)
		}
		if published.LogicalPath != "/backups/"+scope+"/2026/10/02/fixture.exorecovery" || !bytes.Equal(archives[scope], payload) {
			t.Fatal("archive was not isolated in its service folder", published)
		}
	}
}

func TestRecoveryEnrollmentBindsEachCodeToItsExpectedNamespace(t *testing.T) {
	codes := map[string]string{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/backup-enrollments/redeem" {
			http.NotFound(response, request)
			return
		}
		var input map[string]string
		_ = json.NewDecoder(request.Body).Decode(&input)
		for _, scope := range RecoveryScopes {
			if input["code"] == codes[scope] {
				_ = json.NewEncoder(response).Encode(map[string]string{"slug": scope + "-server-a", "namespaceSlug": scope, "token": strings.Repeat(scope[:1], 43)})
				return
			}
		}
		http.Error(response, "invalid code", http.StatusUnauthorized)
	}))
	defer server.Close()
	for _, scope := range RecoveryScopes {
		codes[scope] = strings.Repeat(scope[:1], 32)
	}
	identities, err := enrollScopes(t.Context(), server.URL, server.Client(), codes)
	if err != nil {
		t.Fatal(err)
	}
	for _, scope := range RecoveryScopes {
		if identities[scope].Slug != scope+"-server-a" || len(identities[scope].Token) != 43 {
			t.Fatalf("invalid enrolled %s identity: %#v", scope, identities[scope])
		}
	}
	wrongCodes := maps.Clone(codes)
	wrongCodes["wyvern"] = codes["gryphon"]
	if _, err := enrollScopes(t.Context(), server.URL, server.Client(), wrongCodes); err == nil {
		t.Fatal("cross-namespace setup code was accepted")
	}
}

func TestRecoveryEnrollmentAcceptsOneIndependentService(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var input map[string]string
		_ = json.NewDecoder(request.Body).Decode(&input)
		if input["code"] != strings.Repeat("n", 32) {
			http.Error(response, "invalid code", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]string{"slug": "neptune-edge-1", "namespaceSlug": "neptune", "token": strings.Repeat("n", 43)})
	}))
	defer server.Close()
	identities, err := enrollScopes(t.Context(), server.URL, server.Client(), map[string]string{"neptune": strings.Repeat("n", 32)})
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 1 || identities["neptune"].Slug != "neptune-edge-1" {
		t.Fatalf("single-service recovery enrollment was not isolated: %#v", identities)
	}
}
