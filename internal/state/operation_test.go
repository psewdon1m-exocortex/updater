package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	"updater/internal/model"
)

func TestHostOperationsRemainExclusiveAcrossStoresAndHandoff(t *testing.T) {
	dir := t.TempDir()
	a, _ := New(dir)
	b, _ := New(dir)
	release, err := a.BeginOperation("")
	if err != nil {
		t.Fatal(err)
	}
	if other, err := b.BeginOperation(""); err == nil {
		other()
		t.Fatal("another store acquired the live host lock")
	}
	now := time.Now().UTC()
	job := model.Job{ID: "component-test", Service: "host-recovery", State: "REQUESTED", CreatedAt: now, UpdatedAt: now}
	if err := a.Save(job); err != nil {
		t.Fatal(err)
	}
	release()
	b, _ = New(dir)
	if other, err := b.BeginOperation(""); err == nil {
		other()
		t.Fatal("unfinished supervised handoff lost its reservation")
	}
	own, err := b.BeginOperation(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	job.State = "COMPLETED"
	job.FinishedAt = &now
	if err := b.Save(job); err != nil {
		t.Fatal(err)
	}
	own()
	next, err := a.BeginOperation("")
	if err != nil {
		t.Fatal(err)
	}
	next()
}

func TestInterruptedJobsKeepRollbackAndEraseStagedKeys(t *testing.T) {
	store, _ := New(t.TempDir())
	now := time.Now().UTC()
	for _, job := range []model.Job{{ID: "interrupted", State: "INSTALLING", Service: "kernel", RollbackAvailable: true}, {ID: "supervised", State: "RESTORING", Service: "host-recovery"}} {
		job.CreatedAt = now
		job.UpdatedAt = now
		if err := store.Save(job); err != nil {
			t.Fatal(err)
		}
	}
	orphan := filepath.Join(store.dir, "recovery-jobs", "orphan")
	if err := os.MkdirAll(orphan, 0700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(orphan, "key"), []byte("synthetic passphrase"), 0600)
	if err := store.ReconcileInterrupted(func(j model.Job) bool { return j.ID == "supervised" }); err != nil {
		t.Fatal(err)
	}
	failed, _ := store.Get("interrupted")
	preserved, _ := store.Get("supervised")
	if failed.State != "FAILED" || failed.FinishedAt == nil || !failed.RollbackAvailable || preserved.FinishedAt != nil {
		t.Fatal("interrupted/supervised state was conflated")
	}
	if err := store.CleanupRecoveryStaging(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("orphan recovery credential was retained")
	}
	if err := store.Save(model.Job{ID: "../escape"}); err == nil {
		t.Fatal("unsafe persisted job ID accepted")
	}
}
