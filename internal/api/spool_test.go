//go:build linux

package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"updater/internal/config"
	"updater/internal/engine"
	"updater/internal/state"
)

func TestStreamingSpoolHTTPAuthenticatesBeforeBodyAndSeals(t *testing.T) {
	dir := t.TempDir()
	env := filepath.Join(dir, "mastermind.env")
	content := "KERNEL_URL=https://kernel.internal\nKERNEL_SERVICE_TOKEN=fixture-kernel-token\nUPDATER_SERVICE_ID=mastermind\n" +
		"UPDATER_COMPOSE_PROJECT_DIR=" + dir + "\nUPDATER_COMPOSE_SERVICE=core\nUPDATER_CONTAINER_NAME=mastermind-core\nUPDATER_IMAGE_VARIABLE=MASTERMIND_CORE_IMAGE\n" +
		"UPDATER_VERSION_VARIABLE=MASTERMIND_VERSION\nMASTERMIND_VERSION=0.0.1\nUPDATER_LOCAL_HEALTH_URL=http://127.0.0.1:18390/readyz\nUPDATER_CONTROL_TOKEN=fixture-head-token\n"
	if err := os.WriteFile(env, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	runtime := config.Runtime{StateDir: dir, RegistryPath: filepath.Join(dir, "heads.json")}
	if err := config.RegisterHead(runtime.RegistryPath, "mastermind", env); err != nil {
		t.Fatal(err)
	}
	store, err := state.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	app := Server{Version: "test-candidate", Runtime: runtime, Store: store, Engine: engine.New(runtime, store, nil)}
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	send := func(method, route string, body io.Reader, token string) *http.Response {
		t.Helper()
		request, err := http.NewRequest(method, server.URL+route, body)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("X-Updater-Token", token)
		request.Host = "updater.local"
		request.Header.Set("Content-Type", "application/octet-stream")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { response.Body.Close() })
		return response
	}
	route := "/v1/heads/mastermind/backup-spools"
	payload := bytes.Repeat([]byte{1, 3, 5, 7}, 512*1024)
	sum := sha256.Sum256(payload)
	descriptor, _ := json.Marshal(map[string]any{"request_id": "http-stream", "filename": state.SpoolFilename, "size": len(payload), "sha256": hex.EncodeToString(sum[:])})
	if response := send("POST", route, bytes.NewReader(descriptor), "wrong"); response.StatusCode != 401 {
		t.Fatal(response.StatusCode)
	}
	created := send("POST", route, bytes.NewReader(descriptor), "fixture-head-token")
	if created.StatusCode != 201 {
		t.Fatal(created.StatusCode)
	}
	var item state.Spool
	if err = json.NewDecoder(created.Body).Decode(&item); err != nil {
		t.Fatal(err)
	}
	object := route + "/" + item.ID
	if response := send("POST", object+"/seal", nil, "fixture-head-token"); response.StatusCode != 400 {
		t.Fatal("unuploaded seal accepted")
	}
	if response := send("PUT", object+"/content", bytes.NewReader(payload), "wrong"); response.StatusCode != 401 {
		t.Fatal(response.StatusCode)
	}
	if response := send("PUT", object+"/content", bytes.NewReader(payload), "fixture-head-token"); response.StatusCode != 204 {
		t.Fatal(response.StatusCode)
	}
	if response := send("POST", object+"/seal", nil, "fixture-head-token"); response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	if response := send("PUT", object+"/content", bytes.NewReader(payload), "fixture-head-token"); response.StatusCode != 400 {
		t.Fatal("sealed spool overwritten")
	}
	if response := send("POST", route, bytes.NewReader(append(descriptor, []byte("{}")...)), "fixture-head-token"); response.StatusCode != 400 {
		t.Fatal("trailing JSON accepted")
	}
	if response := send("DELETE", object, nil, "wrong"); response.StatusCode != 401 {
		t.Fatal("unauthenticated cleanup accepted")
	}
	if response := send("DELETE", object, nil, "fixture-head-token"); response.StatusCode != 204 {
		t.Fatal("unclaimed spool cleanup failed", response.StatusCode)
	}
	if _, _, err := store.ValidatedSpool("mastermind", "http-stream", item.ID); err == nil {
		t.Fatal("discarded bytes still available")
	}
}
