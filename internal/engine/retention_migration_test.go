package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"updater/internal/config"
	"updater/internal/model"
	"updater/internal/state"
)

func TestLegacyArchiveMigrationPreservesRollbackMetadataWithoutSecrets(t *testing.T) {
	dir := t.TempDir()
	store, err := state.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := store.BackupPath("legacy-job", "backup.zip")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(archive, []byte("PKsynthetic-old-archive"), 0600); err != nil {
		t.Fatal(err)
	}
	deployment := filepath.Join(filepath.Dir(archive), "deployment.json")
	body, _ := json.Marshal([]deploymentFile{{Name: "compose.production.yaml", Existed: true, Data: []byte("services: {}")}, {Name: ".env", Existed: true, Data: []byte("ACCESS_KEY=synthetic-secret\n")}})
	if err = os.WriteFile(deployment, body, 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err = store.Save(model.Job{ID: "legacy-job", HeadID: "chronos", Service: "chronos", State: "COMPLETED", CreatedAt: now, UpdatedAt: now, BackupPath: archive, DeploymentSnapshot: deployment, RollbackAvailable: true}); err != nil {
		t.Fatal(err)
	}
	runtime := config.Runtime{StateDir: dir}
	if err = MigrateBackupRetention(runtime, store); err != nil {
		t.Fatal(err)
	}
	job, _ := store.Get("legacy-job")
	if job.BackupPath != "" || job.BackupSHA256 == "" || !job.RollbackAvailable || job.RecoveryMode != "operator-copy" {
		t.Fatalf("migration lost metadata: %+v", job)
	}
	if _, err = os.Stat(archive); !os.IsNotExist(err) {
		t.Fatal("legacy archive remains on disk")
	}
	migrated, _ := os.ReadFile(job.DeploymentSnapshot)
	if strings.Contains(string(migrated), "synthetic-secret") || strings.Contains(string(migrated), "QUNDRVNTX0tFWT0") {
		t.Fatal("copied env secret remains")
	}
	if err = MigrateBackupRetention(runtime, store); err != nil {
		t.Fatal("migration is not restart-safe", err)
	}
	// Reproduce a crash after metadata commit but before archive unlink.
	if err = os.MkdirAll(filepath.Dir(archive), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(archive, []byte("PKleft-after-crash"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = MigrateBackupRetention(runtime, store); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(archive); !os.IsNotExist(err) {
		t.Fatal("archive survived resumed cleanup")
	}
	orphan := filepath.Join(dir, "backups", "1700000000-"+strings.Repeat("a", 37))
	if err = os.Mkdir(orphan, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(orphan, "orphan.zip"), []byte("PKorphan"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = MigrateBackupRetention(runtime, store); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("managed orphan archive survived startup")
	}
}
