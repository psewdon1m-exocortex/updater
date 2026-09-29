package engine

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"updater/internal/config"
)

func TestSaturnReadinessUsesComposeWindow(t *testing.T) {
	engine := &Engine{
		runtime: config.Runtime{CommandTimeoutSec: 300},
		checkHealthFn: func(ctx context.Context, _ string) error {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) < 190*time.Second {
				t.Fatal("Saturn readiness window is shorter than Compose health checks")
			}
			return nil
		},
	}
	if engine.operationTimeout("saturn") < 10*time.Minute {
		t.Fatal("Saturn operation timeout cannot cover migration and readiness")
	}
	if err := engine.checkHeadHealth(context.Background(), config.HeadConfig{Service: "saturn"}, "http://127.0.0.1:3000/health/ready"); err != nil {
		t.Fatal(err)
	}
	engine.checkHealthFn = func(ctx context.Context, _ string) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 35*time.Second {
			t.Fatal("ordinary service readiness window unexpectedly grew")
		}
		return nil
	}
	if err := engine.checkHeadHealth(context.Background(), config.HeadConfig{Service: "kernel"}, "http://127.0.0.1:18180/api/health"); err != nil {
		t.Fatal(err)
	}
}

func TestHealthProbeWaitsForDelayedReadiness(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) < 4 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pollHealth(ctx, server.URL, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 4 {
		t.Fatalf("expected four readiness probes, got %d", requests.Load())
	}
}

func TestHealthFailureReportsOnlySafeDependencyCodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"checks":{"database":{"state":"pass"},"storage":{"state":"fail","detail":"storage_unavailable"},"worker":{"state":"fail","detail":"secret=private-value"}}}`))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := pollHealth(ctx, server.URL, time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "HTTP 503; database=pass, storage=fail:storage_unavailable, worker=fail") || strings.Contains(err.Error(), "private-value") {
		t.Fatalf("unexpected safe health diagnostic: %v", err)
	}
}
