//go:build kernelintegration

package hostrecovery

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"updater/internal/config"
)

// The Kernel fixture uses its real app, persisted migration and HTTP Volt
// client. The Go client trusts only this fixture's test TLS certificate.
func TestActualKernelRecoveryDiscoveryHTTP(t *testing.T) {
	endpoint := os.Getenv("UPDATER_KERNEL_HTTP_TEST_URL")
	target, err := url.Parse(endpoint)
	if err != nil || target.Hostname() != "host.docker.internal" || target.Scheme != "http" {
		t.Fatal("disposable Kernel HTTP fixture is required")
	}
	mode := os.Getenv("UPDATER_KERNEL_HTTP_TEST_MODE")
	if mode != "denied" && mode != "discovered" {
		t.Fatal("unknown discovery fixture mode")
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
	root := t.TempDir()
	tokenFile := filepath.Join(root, "kernel.token")
	if err := os.WriteFile(tokenFile, []byte("machine-"+strings.Repeat("m", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := config.Runtime{StateDir: filepath.Join(root, "state"), HostConfigPath: filepath.Join(root, "host.json")}
	// A stale saved URL must neither hide 403 nor override valid discovery.
	if err := config.SaveHost(runtime, config.HostConfig{KernelURL: server.URL, KernelTokenFile: tokenFile,
		HostID: "host-1234567890abcdef12345678", RecoveryGatewayURL: "https://stale-saved.test"}); err != nil {
		t.Fatal(err)
	}
	for _, scope := range RecoveryScopes {
		t.Run(scope, func(t *testing.T) {
			for attempt := 0; attempt < 2; attempt++ {
				origin, err := ResolveOrigin(runtime, "")
				if mode == "denied" {
					if err == nil || !strings.Contains(err.Error(), "HTTP 403") || origin != "" {
						t.Fatal("authorization denial selected a saved origin", origin, err)
					}
				} else if err != nil || origin != "https://saturn.discovered.test:8443" {
					t.Fatal("Kernel did not supply the Saturn origin", origin, err)
				}
			}
		})
	}
}
