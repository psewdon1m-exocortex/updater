package component

import (
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRunningVersionUsesReleasedGryphonAdminContract(t *testing.T) {
	socket, endpoint, schema, err := runningVersionEndpoint("gryphon")
	if err != nil || socket != "/run/gryphon-admin/admin.sock" || endpoint != "/v1/status" || schema != "exocortex.gryphon.status.v1" {
		t.Fatalf("incompatible Gryphon version contract: %q %q %q %v", socket, endpoint, schema, err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket integration")
	}
	for _, tc := range []struct{ name, body, want string }{
		{"released-status", `{"schema":"exocortex.gryphon.status.v1","version":"0.1.2","bots":[],"connections":[]}`, "0.1.2"},
		{"health-is-not-a-version", `{"status":"ok"}`, ""},
		{"wrong-schema", `{"schema":"unrelated","version":"0.1.4"}`, ""},
		{"invalid-version", `{"schema":"exocortex.gryphon.status.v1","version":"unknown"}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := filepath.Join(t.TempDir(), "admin.sock")
			listener, err := net.Listen("unix", local)
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != endpoint {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			})}
			go server.Serve(listener)
			t.Cleanup(func() { _ = server.Close() })
			got, err := readRunningVersion(local, endpoint, schema)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("accepted incompatible response: %s", tc.body)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
