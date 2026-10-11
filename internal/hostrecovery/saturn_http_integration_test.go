//go:build saturnintegration

package hostrecovery

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"updater/internal/config"
)

// Saturn's disposable Nest/Fastify fixture starts this test explicitly. TLS is
// terminated here with a trusted test CA; no production TLS check is bypassed.
func TestActualSaturnHostRecoveryHTTP(t *testing.T) {
	endpoint := os.Getenv("UPDATER_SATURN_HTTP_TEST_URL")
	if endpoint == "" {
		t.Fatal("disposable Saturn HTTP fixture is required")
	}
	target, err := url.Parse(endpoint)
	if err != nil || target.Hostname() != "host.docker.internal" || target.Scheme != "http" {
		t.Fatal("unexpected Saturn test fixture endpoint")
	}
	server := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
	defer server.Close()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous; transport.CloseIdleConnections() })
	response, err := server.Client().Get(server.URL + "/__qa/setup-codes")
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]string{}
	err = json.NewDecoder(response.Body).Decode(&codes)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	identities, err := EnrollScopes(t.Context(), server.URL, codes)
	if err != nil || len(identities) != len(RecoveryScopes) {
		t.Fatal("real HTTP enrollment failed", err)
	}
	root := t.TempDir()
	fixture := hostRecoveryFixture("actual-http")
	writeHostRecoveryFixture(t, root, fixture)
	runtime := config.Runtime{StateDir: filepath.Join(root, "var/lib/updater"), UpdaterVersion: "qa-http", HostConfigPath: filepath.Join(root, "etc/exocortex/updater-host.json")}
	if err := config.SaveRecoveryStorage(runtime, server.URL, identities); err != nil {
		t.Fatal(err)
	}
	for _, scope := range RecoveryScopes {
		t.Run(scope, func(t *testing.T) {
			entries, err := CollectScope(root, scope)
			if err != nil {
				t.Fatal(err)
			}
			password := "synthetic HTTP recovery key " + scope
			sealed, err := SealScope(entries, password, scope)
			if err != nil {
				t.Fatal(err)
			}
			createdAt := time.Now().UTC()
			setID := "component-http-go-" + scope
			published, err := PublishScope(t.Context(), runtime, ScopedArchive{Scope: scope, Bytes: sealed}, setID, createdAt)
			if err != nil {
				t.Fatal("real HTTP publisher failed", err)
			}
			// Replaying the identical run must not duplicate or overwrite the object.
			repeated, err := PublishScope(t.Context(), runtime, ScopedArchive{Scope: scope, Bytes: sealed}, setID, createdAt)
			if err != nil || repeated != published {
				t.Fatal("completed HTTP replay changed the receipt", err)
			}
			response, err := server.Client().Get(server.URL + "/__qa/archive/" + scope)
			if err != nil {
				t.Fatal(err)
			}
			received, err := io.ReadAll(response.Body)
			response.Body.Close()
			digest := sha256.Sum256(received)
			if err != nil || response.StatusCode != http.StatusOK || !bytes.Equal(received, sealed) || published.SHA256 != hex.EncodeToString(digest[:]) || published.Size != len(received) || !strings.HasPrefix(published.LogicalPath, "/backups/"+scope+"/fixture-host/") {
				t.Fatal("stored HTTP archive or receipt differs", err)
			}
			decoded, err := OpenScope(received, password, scope)
			if err != nil {
				t.Fatal("downloaded archive cannot be decrypted", err)
			}
			restored := t.TempDir()
			if err := os.MkdirAll(filepath.Join(restored, "var/lib/updater"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := ApplyScoped(restored, decoded, scope, func() error { return nil }); err != nil {
				t.Fatal("downloaded archive cannot be restored", err)
			}
			assertHostRecoveryFixture(t, restored, map[string]map[string][]byte{scope: fixture[scope]})
		})
	}
}
