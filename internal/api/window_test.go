package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"updater/internal/console"
)

func TestWindowOperatorTUILeaseAndConditionalClose(t *testing.T) {
	dir := t.TempDir()
	gatePath, operatorPath := filepath.Join(dir, "gate.sock"), filepath.Join(dir, "operator.sock")
	gateListener, err := net.Listen("unix", gatePath)
	if err != nil {
		t.Fatal(err)
	}
	defer gateListener.Close()
	prior := windowAdminSocket
	windowAdminSocket = gatePath
	defer func() { windowAdminSocket = prior }()
	var mu sync.Mutex
	var calls []string
	lease := "0123456789abcdef0123456789abcdef0123456789abcdef"
	gate := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/open":
			_ = json.NewEncoder(w).Encode(map[string]any{"open": true, "lease_id": lease})
		case "/v1/heartbeat":
			_ = json.NewEncoder(w).Encode(map[string]any{"open": true})
		case "/v1/close":
			_ = json.NewEncoder(w).Encode(map[string]any{"open": false})
		default:
			w.WriteHeader(404)
		}
	})}
	go gate.Serve(gateListener)
	defer gate.Close()
	operatorListener, err := net.Listen("unix", operatorPath)
	if err != nil {
		t.Fatal(err)
	}
	defer operatorListener.Close()
	operator := &http.Server{Handler: (Server{}).operatorHandler()}
	go operator.Serve(operatorListener)
	defer operator.Close()
	client := console.NewClient(operatorPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	job, err := client.Act(ctx, console.Action{Component: "window", Kind: "open", Minutes: 20, RequestID: "tui-window-open-123456"})
	if err != nil || job.WindowLeaseID != lease {
		t.Fatalf("grant failed: %+v %v", job, err)
	}
	client.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[0] != "/v1/open" || calls[1] != "/v1/close" {
		t.Fatalf("unexpected Window calls: %v", calls)
	}
	if _, err := os.Stat(gatePath); err != nil {
		t.Fatal(err)
	}
}

func TestWindowFirstPairingProxySendsPublicKeyOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "window-admin.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	prior := windowAdminSocket
	windowAdminSocket = path
	defer func() { windowAdminSocket = prior }()
	var received string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/pair" || r.Method != "POST" {
			w.WriteHeader(404)
			return
		}
		var body struct {
			Key string `json:"key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(400)
			return
		}
		received = body.Key
		_ = json.NewEncoder(w).Encode(map[string]string{"fingerprint": "SHA256:paired"})
	})}
	go server.Serve(listener)
	defer server.Close()
	fingerprint, err := PairWindowPublicKey(context.Background(), "ssh-ed25519 AAAAexample")
	if err != nil || fingerprint != "SHA256:paired" || received != "ssh-ed25519 AAAAexample" {
		t.Fatalf("pair proxy failed: fingerprint=%q key=%q error=%v", fingerprint, received, err)
	}
}
