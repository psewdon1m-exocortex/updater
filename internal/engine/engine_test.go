package engine

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"updater/internal/config"
	"updater/internal/kernel"
	"updater/internal/model"
	"updater/internal/release"
	"updater/internal/state"
)

type successfulRunner struct{}

func (successfulRunner) Run(_ context.Context, name string, args, _ []string, _ string) ([]byte, error) {
	if name == "docker" && len(args) > 0 && args[0] == "inspect" {
		return []byte("ghcr.io/example/kernel@sha256:old\n"), nil
	}
	return []byte("ok"), nil
}

func testEngine(t *testing.T, dryRun bool) (*Engine, config.Runtime, *state.Store) {
	t.Helper()
	dir := t.TempDir()
	envPath := filepath.Join(dir, "kernel.env")
	env := `KERNEL_URL=http://127.0.0.1:18180
KERNEL_SERVICE_TOKEN=service-token-long-enough
UPDATER_SERVICE_ID=kernel
UPDATER_COMPOSE_PROJECT_DIR=/opt/kernel
UPDATER_COMPOSE_SERVICE=kernel
UPDATER_CONTAINER_NAME=exocortex-kernel
UPDATER_IMAGE_VARIABLE=KERNEL_IMAGE
UPDATER_VERSION_VARIABLE=KERNEL_VERSION
KERNEL_VERSION=1.1.0
UPDATER_LOCAL_HEALTH_URL=http://127.0.0.1:18180/api/health
UPDATER_CONTROL_TOKEN=control-token-long-enough
`
	if err := os.WriteFile(envPath, []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := config.Runtime{
		StateDir:          dir,
		RegistryPath:      filepath.Join(dir, "heads.json"),
		DryRun:            dryRun,
		CommandTimeoutSec: 10,
	}
	if err := config.RegisterHead(runtime.RegistryPath, "kernel", envPath); err != nil {
		t.Fatal(err)
	}
	store, err := state.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	instance := New(runtime, store, nil)
	instance.SetTestDependencies(
		func(string, string, string, time.Duration) (kernel.Snapshot, error) {
			return kernel.Snapshot{Values: map[string]interface{}{
				"repositories": map[string]interface{}{"kernel": map[string]interface{}{"url": "https://github.com/example/platform"}},
			}}, nil
		},
		func(context.Context, string, string, string, string) (release.Resolved, error) {
			var resolved release.Resolved
			resolved.Manifest.SchemaVersion = 1
			resolved.Manifest.Service = "kernel"
			resolved.Manifest.Version = "1.2.0"
			resolved.Manifest.Image.Reference = "ghcr.io/example/kernel"
			resolved.Manifest.Image.Digest = "sha256:0123456789abcdef"
			return resolved, nil
		},
	)
	return instance, runtime, store
}

func backup() model.Backup {
	data := []byte("valid-backup")
	sum := sha256.Sum256(data)
	return model.Backup{
		Filename:   "kernel-backup.json",
		SHA256:     hex.EncodeToString(sum[:]),
		DataBase64: base64.StdEncoding.EncodeToString(data),
	}
}

func TestDryRunPersistsBackupAndCompletes(t *testing.T) {
	instance, _, store := testEngine(t, true)
	job, err := instance.Start(model.UpdateRequest{
		RequestID: "request-1", HeadID: "kernel", Service: "kernel", Backup: backup(),
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := store.Get(job.ID)
		if current.State == "COMPLETED" {
			if _, err := os.Stat(current.BackupPath); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not complete")
}

func TestIdempotencyAndBackupValidation(t *testing.T) {
	instance, _, _ := testEngine(t, true)
	request := model.UpdateRequest{RequestID: "same", HeadID: "kernel", Service: "kernel", Backup: backup()}
	first, err := instance.Start(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := instance.Start(request)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatal("same request_id must return the existing job")
	}
	request.RequestID = "bad"
	request.Backup.SHA256 = "00"
	if _, err := instance.Start(request); err == nil {
		t.Fatal("invalid backup checksum must be rejected")
	}
	deadline := time.Now().Add(time.Second)
	for instance.Busy() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
}

func TestHeadCannotUpdateDifferentService(t *testing.T) {
	instance, _, _ := testEngine(t, true)
	_, err := instance.Start(model.UpdateRequest{
		RequestID: "request-2", HeadID: "kernel", Service: "perimetr", Backup: backup(),
	})
	if err == nil {
		t.Fatal("head must not mutate another local profile")
	}
}

func TestSuccessfulUpdatePersistsImageAndServiceVersionTogether(t *testing.T) {
	instance, _, store := testEngine(t, false)
	instance.runner = successfulRunner{}
	instance.SetTestHostOperations(func(context.Context, string) error { return nil }, nil)
	job, err := instance.Start(model.UpdateRequest{
		RequestID: "successful-update", HeadID: "kernel", Service: "kernel", Backup: backup(),
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := store.Get(job.ID)
		if current.State == "COMPLETED" {
			head, err := config.LoadHead(instance.runtime, "kernel")
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(head.EnvFile)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), "KERNEL_IMAGE=ghcr.io/example/kernel@sha256:0123456789abcdef") ||
				!strings.Contains(string(body), "KERNEL_VERSION=1.2.0") {
				t.Fatalf("image and version were not updated atomically:\n%s", body)
			}
			if current.InstalledVersion != "1.2.0" {
				t.Fatalf("installed version was not persisted in the job: %#v", current)
			}
			return
		}
		if current.State == "FAILED" || current.State == "ROLLBACK_FAILED" {
			t.Fatalf("unexpected update failure: %s", current.Message)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("update did not complete")
}

func TestFailedHealthCheckAutomaticallyRestoresPreviousImage(t *testing.T) {
	instance, _, store := testEngine(t, false)
	instance.runner = successfulRunner{}
	var checks atomic.Int32
	instance.SetTestHostOperations(
		func(context.Context, string) error {
			if checks.Add(1) == 1 {
				return context.DeadlineExceeded
			}
			return nil
		},
		nil,
	)
	job, err := instance.Start(model.UpdateRequest{
		RequestID: "rollback-health", HeadID: "kernel", Service: "kernel", Backup: backup(),
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := store.Get(job.ID)
		if current.State == "ROLLED_BACK" {
			head, err := config.LoadHead(instance.runtime, "kernel")
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(head.EnvFile)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), "KERNEL_IMAGE=ghcr.io/example/kernel@sha256:old") {
				t.Fatalf("previous image was not restored:\n%s", body)
			}
			if !strings.Contains(string(body), "KERNEL_VERSION=1.1.0") {
				t.Fatalf("previous version was not restored:\n%s", body)
			}
			return
		}
		if current.State == "ROLLBACK_FAILED" {
			t.Fatalf("unexpected rollback failure: %s", current.Message)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("failed update did not roll back")
}
