package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"updater/internal/config"
)

type versionRunner struct {
	image, id, running string
	fail               bool
	calls              int
}

func (r *versionRunner) Run(_ context.Context, _ string, args, _ []string, _ string) ([]byte, error) {
	r.calls++
	if r.fail {
		return nil, errors.New("injected inspect failure")
	}
	if args[0] == "inspect" {
		return []byte(r.running + " " + r.id + " " + r.image), nil
	}
	return []byte("sha256:actual"), nil
}

func TestLegacyVersionRequiresExactRunningImage(t *testing.T) {
	image := "ghcr.io/example/volt@sha256:" + strings.Repeat("a", 64)
	for _, scenario := range []string{"valid", "wrong-image", "wrong-id", "stopped", "inspect-failed", "new-version", "other-service", "reported-mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			runner := &versionRunner{image: image, id: "sha256:actual", running: "true"}
			head := config.HeadConfig{Service: "volt", ContainerName: "volt"}
			version := "0.1.5"
			failure := errMissingRunningVersion
			switch scenario {
			case "wrong-image":
				runner.image += "bad"
			case "wrong-id":
				runner.id = "sha256:wrong"
			case "stopped":
				runner.running = "false"
			case "inspect-failed":
				runner.fail = true
			case "new-version":
				version = "0.2.0"
			case "other-service":
				head.Service = "kernel"
			case "reported-mismatch":
				failure = errors.New("reported wrong version")
			}
			engine := &Engine{runner: runner, checkVersionFn: func(context.Context, string, string) error { return failure }}
			err := engine.checkHeadVersion(context.Background(), head, version, image)
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("unexpected outcome: %v", err)
			}
			if (scenario == "new-version" || scenario == "other-service" || scenario == "reported-mismatch") && runner.calls != 0 {
				t.Fatal("ineligible fallback inspected Docker")
			}
		})
	}
}

func TestHealthyOldOrMissingVersionCannotCompleteAnUpdate(t *testing.T) {
	for _, version := range []string{"", "1.0.0", "2.0.0"} {
		t.Run(version, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"ok":true,"version":%q}`, version) }))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			err := checkRunningVersion(ctx, server.URL, "2.0.0")
			if (err == nil) != (version == "2.0.0") {
				t.Fatalf("version %q: %v", version, err)
			}
		})
	}
}
