package engine

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"updater/internal/config"
	"updater/internal/kernel"
	"updater/internal/model"
	"updater/internal/release"
)

type saturnRunner struct {
	mu    sync.Mutex
	calls []string
}

func (r *saturnRunner) Run(_ context.Context, name string, args, _ []string, _ string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if len(args) > 0 && args[0] == "inspect" {
		return []byte("ghcr.io/example/app@sha256:old"), nil
	}
	return []byte("ok"), nil
}
func TestSaturnRollsBackBothImagesAndDatabaseBeforeReadiness(t *testing.T) {
	instance, runtime, store := testEngine(t, false)
	head, err := config.LoadHead(runtime, "kernel")
	if err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(head.EnvFile)
	content = []byte(strings.ReplaceAll(string(content), "UPDATER_SERVICE_ID=kernel", "UPDATER_SERVICE_ID=saturn") + "\nVAULT_WEB_IMAGE=ghcr.io/example/web@sha256:old\n")
	if err := os.WriteFile(head.EnvFile, content, 0600); err != nil {
		t.Fatal(err)
	}
	runner := &saturnRunner{}
	instance.runner = runner
	instance.SetTestBackupOwnership(func(string, int, int) error { return nil })
	instance.SetTestDependencies(func(string, string, string, time.Duration) (kernel.Snapshot, error) {
		return kernel.Snapshot{Values: map[string]interface{}{"repositories": map[string]interface{}{"saturn": map[string]interface{}{"url": "https://github.com/example/saturn"}}}}, nil
	}, func(context.Context, string, string, string, string) (release.Resolved, error) {
		var result release.Resolved
		result.ComposePath = deploymentFixture(t, "saturn")
		result.Manifest.SchemaVersion = 1
		result.Manifest.Service = "saturn"
		result.Manifest.Version = "1.2.0"
		result.Manifest.Image.Reference = "ghcr.io/example/app"
		result.Manifest.Image.Digest = "sha256:new"
		result.Manifest.WebImage = "ghcr.io/example/web@sha256:new"
		return result, nil
	})
	var healthCalls atomic.Int32
	instance.SetTestHostOperations(func(context.Context, string) error {
		if healthCalls.Add(1) == 1 {
			return errors.New("injected new-image health failure")
		}
		return nil
	}, nil)
	job, err := instance.Start(model.UpdateRequest{RequestID: "saturn-dual-rollback", HeadID: "kernel", Service: "saturn", Backup: backup()})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := store.Get(job.ID)
		if current.FinishedAt != nil {
			if current.State != "ROLLED_BACK" {
				t.Fatalf("unexpected terminal state %s: %s", current.State, current.Message)
			}
			values, _ := config.ParseEnvFile(head.EnvFile)
			if values["KERNEL_IMAGE"] != "ghcr.io/example/app@sha256:old" || values["VAULT_WEB_IMAGE"] != "ghcr.io/example/web@sha256:old" || values["KERNEL_VERSION"] != "1.1.0" {
				t.Fatal("dual image or version rollback incomplete")
			}
			runner.mu.Lock()
			calls := strings.Join(runner.calls, "\n")
			runner.mu.Unlock()
			restore := strings.Index(calls, "recovery-cli.mjs restore-replace")
			finalStart := strings.LastIndex(calls, "up -d --no-deps api worker edge")
			if restore < 0 || restore > finalStart || !strings.Contains(calls, "pull ghcr.io/example/web@sha256:new") {
				t.Fatal("paired replacement/restore order was not executed")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Saturn rollback did not finish")
}
