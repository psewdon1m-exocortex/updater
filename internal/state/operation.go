package state

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"updater/internal/model"
)

// BeginOperation serializes host mutations across daemon requests, CLI and
// detached supervisors. Durable unfinished jobs reserve the host during handoff.
func (s *Store) BeginOperation(ownJobID string) (func(), error) {
	file, err := os.OpenFile(filepath.Join(s.dir, "host-operation.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("another host operation is already running")
	}
	var once sync.Once
	release := func() { once.Do(func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }) }
	for _, listed := range s.List() {
		job, ok := s.Get(listed.ID)
		if ok && job.ID != ownJobID && job.FinishedAt == nil {
			release()
			return nil, errors.New("another host operation is already running")
		}
	}
	return release, nil
}

// ReconcileInterrupted runs at daemon startup. Supervised jobs survive restart;
// in-process jobs become explicit failures and keep their rollback snapshots.
func (s *Store) ReconcileInterrupted(supervisorActive func(model.Job) bool) error {
	for _, listed := range s.List() {
		job, ok := s.Get(listed.ID)
		if !ok || job.FinishedAt != nil || supervisorActive(job) {
			continue
		}
		now := time.Now().UTC()
		job.State, job.Message = "FAILED", "Host operation was interrupted; inspect status and retry or use the retained rollback snapshot"
		job.UpdatedAt, job.FinishedAt = now, &now
		if err := s.Save(job); err != nil {
			return err
		}
		if err := os.RemoveAll(filepath.Join(s.dir, "recovery-jobs", job.ID)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) HasActiveOperation() bool {
	for _, listed := range s.List() {
		if job, ok := s.Get(listed.ID); ok && job.FinishedAt == nil {
			return true
		}
	}
	return false
}

// Startup-only cleanup: no request can be writing an as-yet unregistered job.
func (s *Store) CleanupRecoveryStaging() error {
	entries, err := os.ReadDir(filepath.Join(s.dir, "recovery-jobs"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !safeJobID.MatchString(entry.Name()) {
			continue
		}
		job, ok := s.Get(entry.Name())
		if ok && job.FinishedAt == nil {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.dir, "recovery-jobs", entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
