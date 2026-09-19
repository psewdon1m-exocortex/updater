package component

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestWyvernEnrollmentAndClientReuseKeepSecretsScoped(t *testing.T) {
	var managerHash, runtimeHash string
	var configuration WyvernConfiguration
	configuration.Schema = "exocortex.kernel.wyvern.v1"
	configuration.InstanceID = "host"
	configuration.Revision = 1
	configuration.Config.Schema = "exocortex.wyvern.config.v1"
	configuration.Config.InstanceID = "host"
	configuration.Config.Adapters = map[string]WyvernAdapter{}
	configuration.Config.Clients = map[string]WyvernClient{}
	lost := true
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			var input map[string]string
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input["access_key"] != "synthetic owner secret" {
				t.Error("wrong Access Key")
			}
			http.SetCookie(w, &http.Cookie{Name: "kernel_session", Value: "synthetic-session"})
			_ = json.NewEncoder(w).Encode(map[string]bool{"authenticated": true})
		case "/api/auth/logout":
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		case "/api/wyvern/instances/host/enroll":
			var input map[string]string
			_ = json.NewDecoder(r.Body).Decode(&input)
			if managerHash != "" && (managerHash != input["manager_token_sha256"] || runtimeHash != input["runtime_token_sha256"]) {
				t.Error("lost reply rotated the pending identity")
			}
			managerHash = input["manager_token_sha256"]
			runtimeHash = input["runtime_token_sha256"]
			if lost {
				lost = false
				connection, _, _ := w.(http.Hijacker).Hijack()
				_ = connection.Close()
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"schema": "exocortex.kernel.wyvern.enrollment.v1", "instance_id": "host", "config_key": "wyvern.instances.host.config"})
		case "/api/v1/wyvern/host", "/api/v1/wyvern/host/mutations":
			if hashToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) != managerHash {
				t.Error("wrong management identity")
			}
			if r.Method == "POST" {
				var input struct {
					ClientID string       `json:"client_id"`
					Client   WyvernClient `json:"client"`
					Revision int          `json:"expected_revision"`
				}
				_ = json.NewDecoder(r.Body).Decode(&input)
				if input.Revision != configuration.Revision {
					w.WriteHeader(409)
					return
				}
				configuration.Config.Clients[input.ClientID] = input.Client
				configuration.Revision++
				requests++
			}
			_ = json.NewEncoder(w).Encode(configuration)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	m := WyvernManager{Root: t.TempDir(), Client: server.Client()}
	ctx := context.Background()
	if err := m.Connect(ctx, server.URL, "synthetic owner secret", "host"); err == nil {
		t.Fatal("lost enrollment reply accepted")
	}
	if err := m.Connect(ctx, server.URL, "synthetic owner secret", "host"); err != nil {
		t.Fatal(err)
	}
	if err := m.Connect(ctx, server.URL, "synthetic owner secret", "host"); err != nil {
		t.Fatal("idempotent reconnect", err)
	}
	identity, err := m.identity()
	if err != nil || hashToken(identity.Token) != managerHash || hashToken(identity.RuntimeToken) != runtimeHash {
		t.Fatal("identity mismatch", err)
	}
	first, err := m.EnsureClient(ctx, "mastermind", "mastermind", "test-one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.EnsureClient(ctx, "mastermind", "mastermind", "test-two")
	if err != nil || second != first || requests != 1 {
		t.Fatal("client was not reused", err, requests)
	}
	client := configuration.Config.Clients["mastermind"]
	if client.TokenHash != hashToken(first.Token) || len(client.Requirements) != 2 {
		t.Fatal("unscoped client")
	}
	client.Enabled = false
	configuration.Config.Clients["mastermind"] = client
	if _, err := m.EnsureClient(ctx, "mastermind", "mastermind", "test-three"); err == nil {
		t.Fatal("revoked client silently re-enabled")
	}
	if err := m.RotateClient(ctx, "mastermind"); err != nil {
		t.Fatal(err)
	}
	rotated, err := m.ReadLink("mastermind")
	if err != nil || rotated.Token == first.Token || configuration.Config.Clients["mastermind"].Enabled || configuration.Config.Clients["mastermind"].TokenHash != hashToken(rotated.Token) {
		t.Fatal("rotation lost scoped identity or re-enabled revoked access", err)
	}
	_ = fs.WalkDir(os.DirFS(m.Root), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, _ := os.ReadFile(m.path("/" + name))
		if strings.Contains(string(body), "synthetic owner secret") || strings.Contains(string(body), "synthetic-session") {
			t.Error("operator credentials persisted")
		}
		return nil
	})
}

func TestWyvernRemoteImportVerifiesIdentityBeforeReplacingLink(t *testing.T) {
	identity := "other"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"schema": "exocortex.wyvern.client.v1", "client_id": identity, "instance_id": "host"})
	}))
	defer server.Close()
	m := WyvernManager{Root: t.TempDir(), Client: server.Client()}
	link := WyvernLink{Schema: "exocortex.wyvern.link.v1", ClientID: "laboratory", InstanceID: "host", Mode: "remote", URL: server.URL, Token: strings.Repeat("a", 64)}
	if err := m.ImportLink(context.Background(), "laboratory", link); err == nil {
		t.Fatal("foreign client accepted")
	}
	if _, err := m.ReadLink("laboratory"); !os.IsNotExist(err) {
		t.Fatal("rejected import wrote a link")
	}
	identity = "laboratory"
	if err := m.ImportLink(context.Background(), identity, link); err != nil {
		t.Fatal(err)
	}
	actual, err := m.ReadLink(identity)
	if err != nil || actual != link {
		t.Fatal("remote link changed", err)
	}
	link.URL = "http://127.0.0.1"
	if err := m.ImportLink(context.Background(), identity, link); err == nil {
		t.Fatal("insecure URL accepted")
	}
	actual, _ = m.ReadLink(identity)
	if actual.Mode != "remote" || actual.URL != server.URL {
		t.Fatal("failed remote import changed the previous link")
	}
}
