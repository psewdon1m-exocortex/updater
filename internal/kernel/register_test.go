package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadResolvesOnlyUpdaterMetadataWithLargeRegister(t *testing.T) {
	ref := "volt://3518462b-bb66-459a-a1ab-c38a837740ab/4"
	private := map[string]interface{}{}
	for i := 0; i < 100; i++ {
		private[fmt.Sprintf("secret%d", i)] = ref
	}
	values := map[string]interface{}{"credentials": private, "repositories": map[string]interface{}{"laboratory": map[string]interface{}{"url": ref}}}
	raw, _ := json.Marshal(map[string]interface{}{"values": values})
	sum := sha256.Sum256(raw)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/register/snapshot" {
			json.NewEncoder(w).Encode(Snapshot{Schema: "exocortex.register.snapshot.v1", Revision: "scope-1", Checksum: "sha256:" + hex.EncodeToString(sum[:]), Values: values})
			return
		}
		calls++
		var payload struct {
			Keys []string `json:"keys"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if len(payload.Keys) != 1 || payload.Keys[0] != "repositories.laboratory.url" {
			t.Errorf("unexpected secret resolution: %v", payload.Keys)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"schema": "exocortex.register.resolution.v1", "values": map[string]interface{}{"repositories.laboratory.url": map[string]string{"value": "https://github.com/synthetic/laboratory"}}})
	}))
	defer server.Close()
	cache := filepath.Join(t.TempDir(), "register.json")
	for i := 0; i < 2; i++ {
		snapshot, err := Load(server.URL, "audit-token", cache, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := snapshot.Values["credentials"]; ok {
			t.Fatal("unrequested credentials returned")
		}
		if _, err := String(snapshot, "repositories.laboratory.url"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("wanted one resolve per load, got %d", calls)
	}
	bytes, _ := os.ReadFile(cache)
	if strings.Contains(string(bytes), "https://github.com/synthetic") {
		t.Fatal("resolved value persisted")
	}
}

func TestLoadNeverForwardsCredentialsOnRedirect(t *testing.T) {
	for _, endpoint := range []string{"snapshot", "resolve"} {
		t.Run(endpoint, func(t *testing.T) {
			leaks := 0
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaks++; w.WriteHeader(200) }))
			defer target.Close()
			ref := "volt://3518462b-bb66-459a-a1ab-c38a837740ab/4"
			values := map[string]interface{}{"services": map[string]interface{}{"saturn": map[string]interface{}{"sni": ref}}}
			raw, _ := json.Marshal(map[string]interface{}{"values": values})
			sum := sha256.Sum256(raw)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/"+endpoint) {
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
					return
				}
				json.NewEncoder(w).Encode(Snapshot{Schema: "exocortex.register.snapshot.v1", Revision: "redirect-1", Checksum: "sha256:" + hex.EncodeToString(sum[:]), Values: values})
			}))
			defer server.Close()
			if _, err := Load(server.URL, "audit-token", filepath.Join(t.TempDir(), "register.json"), time.Second); err == nil {
				t.Fatal("redirect accepted")
			}
			if leaks != 0 {
				t.Fatalf("redirect destination contacted %d times", leaks)
			}
		})
	}
}

func TestNumericReferencesResolveThroughKernelAndOnlyReferencesAreCached(t *testing.T) {
	ref := "volt://3518462b-bb66-459a-a1ab-c38a837740ab/4"
	values := map[string]interface{}{"services": map[string]interface{}{"saturn": map[string]interface{}{"sni": ref}}}
	raw, _ := json.Marshal(map[string]interface{}{"values": values})
	sum := sha256.Sum256(raw)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer audit-only-token" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/api/v1/register/snapshot":
			json.NewEncoder(w).Encode(Snapshot{Schema: "exocortex.register.snapshot.v1", Revision: "audit-1", Checksum: "sha256:" + hex.EncodeToString(sum[:]), Values: values})
		case "/api/v1/register/resolve":
			calls++
			json.NewEncoder(w).Encode(map[string]interface{}{"schema": "exocortex.register.resolution.v1", "values": map[string]interface{}{"services.saturn.sni": map[string]string{"value": "saturn-audit-canary.invalid"}}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cache := filepath.Join(t.TempDir(), "register.json")
	snapshot, err := Load(server.URL, "audit-only-token", cache, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	value, err := String(snapshot, "services.saturn.sni")
	if err != nil || value != "saturn-audit-canary.invalid" || calls != 1 {
		t.Fatalf("resolution failed: %v, calls %d", err, calls)
	}
	bytes, err := os.ReadFile(cache)
	if err != nil || strings.Contains(string(bytes), "saturn-audit-canary") || !strings.Contains(string(bytes), ref) {
		t.Fatal("cache must contain references only")
	}
}

func TestReferenceContractRejectsInvalidPositions(t *testing.T) {
	for _, suffix := range []string{"0", "6", "04", "-1", "3518462b-bb66-459a-a1ab-c38a837740ab"} {
		if voltReference.MatchString("volt://3518462b-bb66-459a-a1ab-c38a837740ab/" + suffix) {
			t.Fatalf("accepted invalid position %s", suffix)
		}
	}
	if !voltReference.MatchString("volt://019926ad-1034-7000-8000-c38a837740ab/5") {
		t.Fatal("UUID v7 position contract rejected")
	}
}
