package engine

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
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
)

// Signature verification is exercised by releaseauth tests. This regression
// starts at the verified-bundle boundary and exercises the actual job engine,
// persistent rollback metadata, env migration, and the head restore contract.
// Docker/systemd execution is represented by the runner fixture.
func TestPublicationHeadsUpdateAndRollback(t *testing.T) {
	for _, service := range []string{"chronos", "laboratory"} {
		for _, failHealth := range []bool{false, true} {
			name := service + "/success"
			if failHealth {
				name = service + "/failed-health"
			}
			t.Run(name, func(t *testing.T) {
				instance, runtime, store := testEngine(t, false)
				instance.runtime.UpdaterVersion = "0.4.6"
				oldHead, err := config.LoadHead(runtime, "kernel")
				if err != nil {
					t.Fatal(err)
				}
				prefix := strings.ToUpper(service)
				content, _ := os.ReadFile(oldHead.EnvFile)
				text := strings.ReplaceAll(string(content), "=kernel", "="+service)
				text = strings.ReplaceAll(text, "exocortex-kernel", "exocortex-"+service)
				text = strings.ReplaceAll(text, "KERNEL_IMAGE", prefix+"_IMAGE")
				text = strings.ReplaceAll(text, "KERNEL_VERSION", prefix+"_VERSION")
				text += "OPERATOR_VALUE=synthetic-preserved\nUPDATER_RESTORE_URL=http://127.0.0.1:1/restore\n"
				if err = os.WriteFile(oldHead.EnvFile, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
				if err = config.RegisterHead(runtime.RegistryPath, service, oldHead.EnvFile); err != nil {
					t.Fatal(err)
				}
				head, err := config.LoadHead(runtime, service)
				if err != nil {
					t.Fatal(err)
				}
				bundle := filepath.Join(t.TempDir(), "head.tar.gz")
				f, _ := os.Create(bundle)
				g := gzip.NewWriter(f)
				w := tar.NewWriter(g)
				for name, data := range map[string]string{"compose.production.yaml": "services: {" + service + ": {image: updated}}\n", ".env.example": "OPERATOR_VALUE=template-value\nNEW_SAFE_DEFAULT=42\nNEW_SECRET=CHANGE_ME\n"} {
					if err = w.WriteHeader(&tar.Header{Name: "./" + name, Mode: 0600, Size: int64(len(data))}); err != nil {
						t.Fatal(err)
					}
					if _, err = w.Write([]byte(data)); err != nil {
						t.Fatal(err)
					}
				}
				w.Close()
				g.Close()
				f.Close()
				instance.runner = successfulRunner{}
				instance.SetTestDependencies(func(string, string, string, time.Duration) (kernel.Snapshot, error) {
					return kernel.Snapshot{Values: map[string]interface{}{"repositories": map[string]interface{}{service: map[string]interface{}{"url": "https://github.com/example/" + service}}}}, nil
				}, func(context.Context, string, string, string, string) (release.Resolved, error) {
					var r release.Resolved
					r.ComposePath = bundle
					r.Manifest.SchemaVersion = 1
					r.Manifest.Service = service
					r.Manifest.Version = "1.2.0"
					r.Manifest.MinimumUpdaterVersion = "0.4.6"
					r.Manifest.Image.Reference = "ghcr.io/example/" + service
					r.Manifest.Image.Digest = "sha256:" + strings.Repeat("a", 64)
					return r, nil
				})
				var health, restores atomic.Int32
				instance.SetTestHostOperations(func(context.Context, string) error {
					if health.Add(1) == 1 && failHealth {
						return errors.New("injected new image failure")
					}
					return nil
				}, func(_ context.Context, h config.HeadConfig, p string) error {
					if h.Service != service || h.ControlToken != "control-token-long-enough" {
						return errors.New("restore scope/token mismatch")
					}
					bytes, err := os.ReadFile(p)
					if err != nil || string(bytes) != "valid-backup" {
						return errors.New("rollback recovery bytes changed")
					}
					restores.Add(1)
					return nil
				})
				job, err := instance.Start(model.UpdateRequest{RequestID: service + "-candidate", HeadID: service, Service: service, Version: "1.2.0", Backup: backup()})
				if err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					current, _ := store.Get(job.ID)
					if current.FinishedAt != nil {
						expected := "COMPLETED"
						if failHealth {
							expected = "ROLLED_BACK"
						}
						if current.State != expected {
							t.Fatalf("%s: %s", current.State, current.Message)
						}
						values, _ := config.ParseEnvFile(head.EnvFile)
						if values["OPERATOR_VALUE"] != "synthetic-preserved" || values["NEW_SECRET"] != "" {
							t.Fatal("operator ownership changed")
						}
						if failHealth {
							if values[prefix+"_VERSION"] != "1.1.0" || values["NEW_SAFE_DEFAULT"] != "" || restores.Load() != 1 {
								t.Fatal("incomplete rollback")
							}
						} else {
							if values[prefix+"_VERSION"] != "1.2.0" || values["NEW_SAFE_DEFAULT"] != "42" || restores.Load() != 0 {
								t.Fatal("incomplete update")
							}
						}
						if !current.RollbackAvailable || current.DeploymentSnapshot == "" || current.BackupPath != "" || current.RecoveryMode != "operator-copy" {
							t.Fatal("rollback evidence missing")
						}
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatal("head job failed to finish")
			})
		}
	}
}
