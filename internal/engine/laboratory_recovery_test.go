package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"updater/internal/config"
	"updater/internal/model"
)

type laboratoryRecoveryRunner struct {
	calls       []string
	environment []string
	fail        string
}

func (r *laboratoryRecoveryRunner) Run(_ context.Context, name string, args, env []string, _ string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	if len(env) > 0 {
		r.environment = env
	}
	if r.fail != "" && strings.Contains(call, r.fail) {
		return []byte("injected failure"), errors.New("failure")
	}
	return nil, nil
}
func TestLaboratoryOfflineRecoveryStopsWriterAndUsesVerifiedImage(t *testing.T) {
	for _, failure := range []string{"", " stop ", " run "} {
		t.Run(failure, func(t *testing.T) {
			runner := &laboratoryRecoveryRunner{fail: failure}
			owned := false
			engine := &Engine{runner: runner, chownBackupFn: func(p string, u, g int) error {
				owned = p == "/dev/shm/recovery/backup.zip" && u == 1000 && g == 1000
				return nil
			}}
			head := config.HeadConfig{EnvFile: "/opt/laboratory/.env", ProjectDir: "/opt/laboratory", ComposeFile: "compose.production.yaml", ComposeService: "laboratory", ImageVariable: "LABORATORY_IMAGE"}
			job := model.Job{BackupPath: "/dev/shm/recovery/backup.zip", RecoveryImage: "ghcr.io/example/laboratory@sha256:" + strings.Repeat("a", 64)}
			err := engine.restoreLaboratoryOffline(context.Background(), head, job)
			if (err == nil) != (failure == "") || !owned {
				t.Fatalf("outcome/ownership: %v", err)
			}
			if !strings.HasSuffix(runner.calls[0], " stop laboratory") {
				t.Fatal("writer was not stopped first")
			}
			if failure == " stop " {
				if len(runner.calls) != 1 {
					t.Fatal("restore continued after stop failure")
				}
				return
			}
			if len(runner.calls) != 2 || !strings.Contains(runner.calls[1], "--no-deps -v /dev/shm/recovery/backup.zip:/recovery/backup.zip:ro --entrypoint node laboratory /app/services/api/src/restore-cli.js /recovery/backup.zip --confirm-replace") {
				t.Fatal("restore invocation is unsafe")
			}
			if len(runner.environment) != 1 || runner.environment[0] != "LABORATORY_IMAGE="+job.RecoveryImage {
				t.Fatal("recovery did not pin the verified candidate image")
			}
		})
	}
}
