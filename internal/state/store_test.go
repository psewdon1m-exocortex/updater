package state

import (
	"os"
	"testing"
	"time"

	"updater/internal/model"
)

func TestPruneBoundsFinishedJobsAndBackups(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for index := 0; index < 3; index++ {
		job := model.Job{
			ID:        string(rune('a' + index)),
			RequestID: string(rune('a' + index)),
			State:     "COMPLETED",
			CreatedAt: now.Add(time.Duration(index) * time.Minute),
			UpdatedAt: now,
		}
		path, err := store.BackupPath(job.ID, "backup.bin")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
			t.Fatal(err)
		}
		job.BackupPath = path
		if err := store.Save(job); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Prune(2, now.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(store.List()) != 2 {
		t.Fatalf("expected 2 retained jobs, got %d", len(store.List()))
	}
	if _, err := os.Stat(store.dir + "/backups/a"); !os.IsNotExist(err) {
		t.Fatal("old backup directory was not removed")
	}
}
