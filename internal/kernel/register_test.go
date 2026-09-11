package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
