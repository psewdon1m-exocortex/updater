package component

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
	"updater/internal/console"
)

func wyvernManifest(version string) WyvernManifest {
	return WyvernManifest{Schema: "exocortex.wyvern.release.v1", Product: "wyvern", Version: version, Image: "ghcr.io/exocortex/wyvern@sha256:" + strings.Repeat("a", 64), APIVersion: 1, ConfigSchema: "exocortex.wyvern.config.v1", Capabilities: []string{"text", "streaming", "structured_output", "token_count"}}
}

type wyvernHost struct {
	d             WyvernDeployment
	version       string
	candidate     string
	active        int
	paused        bool
	configured    bool
	stops         int
	failNew       bool
	failAll       bool
	failAfterStop bool
}

func newWyvernHost(t *testing.T) *wyvernHost {
	h := &wyvernHost{candidate: "0.0.2", configured: true}
	h.d = WyvernDeployment{Root: t.TempDir(), Poll: time.Nanosecond, DrainTimeout: time.Millisecond}
	h.d.Read = func(context.Context) (console.WyvernView, error) {
		return console.WyvernView{Status: console.WyvernStatus{Version: h.version, APIVersion: 1, ConfigurationLoaded: h.configured, Ready: h.configured && !h.paused, Drain: h.paused, ActiveRequests: h.active}}, nil
	}
	h.d.Control = func(ctx context.Context, kind string) (console.WyvernStatus, error) {
		if kind == "drain" {
			h.paused = true
		}
		if kind == "resume" {
			h.paused = false
		}
		view, _ := h.d.Read(ctx)
		return view.Status, nil
	}
	h.d.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "docker" && args[0] == "run" {
			return json.Marshal(map[string]any{"service": "wyvern", "version": h.candidate, "api_version": 1})
		}
		if name == "systemctl" && args[0] == "stop" {
			h.stops++
			if h.failAfterStop {
				_ = os.WriteFile(h.d.path("/run/wyvern/client.sock"), []byte("foreign object"), 0600)
			}
		}
		if name == "systemctl" && args[0] == "restart" {
			env, _ := os.ReadFile(h.d.path("/etc/wyvern/wyvern.env"))
			h.version = strings.TrimSpace(strings.Split(string(env), "WYVERN_VERSION=")[1])
			h.paused = true
			if h.failAll || (h.failNew && h.version == h.candidate) {
				return nil, errors.New("simulated start failure")
			}
		}
		return nil, nil
	}
	return h
}
func (h *wyvernHost) installOld(t *testing.T) {
	t.Helper()
	old := wyvernManifest("0.0.1")
	h.version = old.Version
	if err := h.d.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.d.installUnit(wyvernUnit + "# old-unit\n"); err != nil {
		t.Fatal(err)
	}
	if err := atomicWyvernFile(h.d.path(wyvernCurrent), old, -1); err != nil {
		t.Fatal(err)
	}
}
func TestWyvernInstallAndUpdatePreservePausedState(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "paused-update"}[old], func(t *testing.T) {
			h := newWyvernHost(t)
			if old {
				h.installOld(t)
				h.paused = true
			}
			if err := h.d.Apply(context.Background(), wyvernManifest(h.candidate)); err != nil {
				t.Fatal(err)
			}
			current, err := h.d.installed()
			if err != nil || current.Version != h.candidate || h.paused != old {
				t.Fatalf("unexpected state %+v %v", current, err)
			}
			if _, err := os.Stat(h.d.path(wyvernMaintenance)); !os.IsNotExist(err) {
				t.Fatal("maintenance marker retained")
			}
		})
	}
}

func TestWyvernUnconfiguredInstallationCanBeUpdated(t *testing.T) {
	h := newWyvernHost(t)
	h.installOld(t)
	h.configured = false
	control := h.d.Control
	h.d.Control = func(ctx context.Context, kind string) (console.WyvernStatus, error) {
		if kind == "reload" {
			t.Fatal("cold install tried to load absent credentials")
		}
		return control(ctx, kind)
	}
	if err := h.d.Apply(context.Background(), wyvernManifest(h.candidate)); err != nil {
		t.Fatal(err)
	}
	if h.version != h.candidate || h.configured {
		t.Fatal("cold update falsely claimed LLM readiness")
	}
}
func TestWyvernFailureRestoresRuntimeAndUnit(t *testing.T) {
	for _, stage := range []string{"activation", "after-stop", "rollback"} {
		t.Run(stage, func(t *testing.T) {
			h := newWyvernHost(t)
			h.installOld(t)
			h.failNew = stage == "activation"
			h.failAfterStop = stage == "after-stop"
			h.failAll = stage == "rollback"
			err := h.d.Apply(context.Background(), wyvernManifest(h.candidate))
			var outcome WyvernDeploymentError
			if !errors.As(err, &outcome) {
				t.Fatalf("missing rollback outcome: %v", err)
			}
			if stage == "rollback" {
				if !outcome.RollbackFailed {
					t.Fatal(err)
				}
				return
			}
			if !outcome.RolledBack || h.version != "0.0.1" || h.paused {
				t.Fatalf("rollback not verified: %v", err)
			}
			unit, _ := os.ReadFile(h.d.path(wyvernUnitPath))
			if !strings.Contains(string(unit), "# old-unit") {
				t.Fatal("old unit not restored")
			}
		})
	}
}
func TestWyvernDrainTimeoutKeepsExistingRuntimeAndClearsJournal(t *testing.T) {
	h := newWyvernHost(t)
	h.installOld(t)
	h.active = 1
	if err := h.d.Apply(context.Background(), wyvernManifest(h.candidate)); err == nil {
		t.Fatal("active request interrupted")
	}
	if h.stops != 0 || h.paused || h.version != "0.0.1" {
		t.Fatal("old runtime changed")
	}
	h.active = 0
	if err := h.d.Apply(context.Background(), wyvernManifest(h.candidate)); err != nil {
		t.Fatalf("aborted transaction stranded: %v", err)
	}
}
func TestWyvernRepairResumesInterruptedFreshInstall(t *testing.T) {
	h := newWyvernHost(t)
	if err := h.d.writeJournal(wyvernTransaction{Phase: "stopping", Candidate: wyvernManifest(h.candidate)}); err != nil {
		t.Fatal(err)
	}
	if err := h.d.Repair(context.Background()); err != nil {
		t.Fatal(err)
	}
	current, err := h.d.installed()
	if err != nil || current == nil || current.Version != h.candidate {
		t.Fatal("installation not recovered")
	}
}
func TestWyvernManifestRejectsFloatingImagesAndNewerProtocol(t *testing.T) {
	for _, mutate := range []func(*WyvernManifest){func(m *WyvernManifest) { m.Image = "ghcr.io/exocortex/wyvern:latest" }, func(m *WyvernManifest) { m.APIVersion = 2 }, func(m *WyvernManifest) { m.ConfigSchema = "v2" }} {
		manifest := wyvernManifest("0.0.2")
		mutate(&manifest)
		body, _ := json.Marshal(manifest)
		if _, err := ParseWyvernManifest(body); err == nil {
			t.Fatal("incompatible release accepted")
		}
	}
}
