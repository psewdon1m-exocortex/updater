package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"time"
	"updater/internal/config"
	"updater/internal/hostrecovery"
	"updater/internal/model"
	"updater/internal/selfupdate"
	"updater/internal/state"
)

func activeSupervisor(job model.Job) bool {
	unit := ""
	switch job.Service {
	case "host-recovery":
		unit = "exocortex-host-recovery.service"
	case "updater-self-update":
		unit = "exocortex-updater-self-update.service"
	default:
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit).Run() == nil
}

func monitorSupervisors(store *state.Store) {
	for range time.NewTicker(30 * time.Second).C {
		_ = store.ReconcileInterrupted(func(job model.Job) bool {
			return (job.Service != "host-recovery" && job.Service != "updater-self-update") || time.Since(job.CreatedAt) < 30*time.Second || activeSupervisor(job)
		})
	}
}

func acquireHostOperation(runtime config.Runtime, ownJob string) func() {
	store, err := state.New(runtime.StateDir)
	exitIf(err)
	release, err := store.BeginOperation(ownJob)
	exitIf(err)
	return release
}

func runSupervised(runtime config.Runtime, id, kind string) (result error) {
	store, err := state.New(runtime.StateDir)
	if err != nil {
		return err
	}
	job, ok := store.Get(id)
	if !ok || job.Service != kind || job.FinishedAt != nil {
		return errors.New("invalid supervised job")
	}
	var release func()
	for deadline := time.Now().Add(5 * time.Second); ; {
		release, err = store.BeginOperation(job.ID)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer release()
	directory := filepath.Join(runtime.StateDir, "recovery-jobs", job.ID)
	defer func() {
		if kind == "host-recovery" {
			_ = os.RemoveAll(directory)
		}
		now := time.Now().UTC()
		job.UpdatedAt, job.FinishedAt = now, &now
		if result != nil {
			job.State = "FAILED"
			job.Message = result.Error()
		} else {
			job.State = "COMPLETED"
			job.Message = "Host operation completed and health verified"
		}
		if err := store.Save(job); result == nil {
			result = err
		}
	}()
	job.State = "INSTALLING"
	if kind == "host-recovery" {
		job.State = "RESTORING"
	}
	job.UpdatedAt = time.Now().UTC()
	if err = store.Save(job); err != nil {
		return err
	}
	if kind == "updater-self-update" {
		return selfupdate.Run(runtime, job.HeadID)
	}
	info, err := os.Stat(filepath.Join(directory, "archive"))
	if err != nil {
		return err
	}
	if info.Size() > 130*1024*1024 {
		return errors.New("recovery archive exceeds the staging limit")
	}
	archive, err := os.ReadFile(filepath.Join(directory, "archive"))
	if err != nil {
		return err
	}
	defer clear(archive)
	key, err := os.ReadFile(filepath.Join(directory, "key"))
	if err != nil {
		return err
	}
	defer clear(key)
	if err = os.RemoveAll(directory); err != nil {
		return err
	}
	return hostrecovery.Restore(archive, string(key))
}
