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
	mu           sync.Mutex
	calls        []string
	environments [][]string
}

func (r *saturnRunner) Run(_ context.Context, name string, args, environment []string, _ string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	r.environments = append(r.environments, append([]string(nil), environment...))
	if len(args) > 1 && args[0] == "image" && args[1] == "inspect" {
		return []byte("sha256:" + strings.Repeat("a", 64)), nil
	}
	if len(args) > 0 && args[0] == "inspect" {
		return []byte("ghcr.io/example/app@sha256:old"), nil
	}
	return []byte("ok"), nil
}
func TestSaturnRollsBackBothImagesAndDatabaseBeforeReadiness(t *testing.T) {
	t.Run("legacy", func(t *testing.T) { testSaturnRollback(t, false) })
	t.Run("candidate-recovery-image", func(t *testing.T) { testSaturnRollback(t, true) })
}

func testSaturnRollback(t *testing.T, offline bool) {
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
		if offline {
			result.Manifest.RollbackRestore = "saturn-offline-v1"
		}
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
			var recoveryEnvironment []string
			for index, call := range runner.calls {
				if strings.Contains(call, "recovery-cli.mjs restore-rollback") {
					recoveryEnvironment = runner.environments[index]
				}
			}
			runner.mu.Unlock()
			restoreCommand := "restore-replace"
			if offline {
				restoreCommand = "restore-rollback"
				if current.RecoveryImage != "ghcr.io/example/app@sha256:new" || !strings.Contains(strings.Join(recoveryEnvironment, "\n"), head.ImageVariable+"="+current.RecoveryImage) {
					t.Fatal("rollback must use the candidate recovery image to restore the exact previous schema")
				}
			}
			restore := strings.Index(calls, "recovery-cli.mjs "+restoreCommand)
			if !strings.Contains(calls, "--user 1000:1000") || !strings.Contains(calls, "api node /app/scripts/recovery-cli.mjs") {
				t.Fatal("recovery requires the application uid, secrets and storage runtime volume")
			}
			if !strings.Contains(calls, "RECOVERY_ARCHIVE_DIR=/recovery-work/archives") || !strings.Contains(calls, "RECOVERY_SPOOL_DIR=/recovery-work/spool") || !strings.Contains(calls, "/dev/shm/exocortex-recovery-") {
				t.Fatal("rollback may retain archives on persistent storage")
			}
			finalStart := strings.LastIndex(calls, "up -d --no-deps api worker web")
			if restore < 0 || restore > finalStart || !strings.Contains(calls, "stop api worker web") || strings.Contains(calls, " api worker edge") || !strings.Contains(calls, "pull ghcr.io/example/web@sha256:new") {
				t.Fatal("paired replacement/restore order was not executed")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("Saturn rollback did not finish")
}
