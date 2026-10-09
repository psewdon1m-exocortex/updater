package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"updater/internal/config"
	"updater/internal/console"
	"updater/internal/hostrecovery"
	"updater/internal/model"
)

func recoveryJobID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "component-" + hex.EncodeToString(random), nil
}

func (s Server) recoveryReady() (func(), error) {
	if s.Engine.Busy() {
		return nil, errors.New("an update is running")
	}
	release, err := s.Store.BeginOperation("")
	if err != nil {
		return nil, err
	}
	for _, listed := range s.Store.List() {
		job, _ := s.Store.Get(listed.ID)
		if job.FinishedAt == nil {
			release()
			return nil, errors.New("a component job is running")
		}
	}
	return release, nil
}

func (s Server) completedRecoveryConfiguration(ctx context.Context, action console.Action) (model.Job, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	enroll := s.RecoveryEnroll
	if enroll == nil {
		enroll = hostrecovery.EnrollScopes
	}
	origin, err := hostrecovery.ResolveOrigin(s.Runtime, action.Recovery.GatewayURL)
	if err != nil {
		return model.Job{}, err
	}
	if _, err := config.ManagedRecoveryKey(s.Runtime, action.Recovery.Service, true); err != nil {
		return model.Job{}, err
	}
	identities, err := enroll(ctx, origin, action.Recovery.EnrollmentCodes)
	if err != nil {
		return model.Job{}, err
	}
	if err := config.SaveRecoveryStorage(s.Runtime, origin, identities); err != nil {
		return model.Job{}, err
	}
	id, err := recoveryJobID()
	if err != nil {
		return model.Job{}, err
	}
	now := time.Now().UTC()
	job := model.Job{ID: id, RequestID: action.RequestID, HeadID: action.Recovery.Service, Service: "host-recovery-configuration", State: "COMPLETED", Message: action.Recovery.Service + " recovery storage configured", CreatedAt: now, UpdatedAt: now, FinishedAt: &now}
	if err := s.Store.Save(job); err != nil {
		return model.Job{}, err
	}
	return job, nil
}

func privateRecoveryArchive(filename, scope, passphrase string) ([]byte, error) {
	info, err := os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 130*1024*1024 {
		return nil, errors.New("recovery archive must be a regular file no larger than 130 MiB")
	}
	body, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	entries, err := hostrecovery.OpenScope(body, passphrase, scope)
	for index := range entries {
		clear(entries[index].Data)
	}
	if err != nil {
		clear(body)
		return nil, err
	}
	return body, nil
}

func (s Server) stageRecoveryJob(action console.Action) (model.Job, error) {
	id, err := recoveryJobID()
	if err != nil {
		return model.Job{}, err
	}
	directory := filepath.Join(s.Runtime.StateDir, "recovery-jobs", id)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return model.Job{}, err
	}
	launched := false
	defer func() {
		if !launched {
			_ = os.RemoveAll(directory)
		}
	}()
	passphrase := action.Recovery.Passphrase
	if passphrase == "" {
		if action.Kind == "recovery-restore" && action.Recovery.KeyPath != "" {
			passphrase, err = config.ReadRecoveryKey(action.Recovery.KeyPath, action.Recovery.Service)
		} else {
			passphrase, err = config.ManagedRecoveryKey(s.Runtime, action.Recovery.Service, action.Kind == "recovery-export")
		}
		if err != nil {
			return model.Job{}, err
		}
	}
	service := "host-recovery-export"
	if err := os.WriteFile(filepath.Join(directory, "key"), []byte(passphrase), 0o600); err != nil {
		return model.Job{}, err
	}
	if err := os.WriteFile(filepath.Join(directory, "scope"), []byte(action.Recovery.Service), 0o600); err != nil {
		return model.Job{}, err
	}
	if action.Kind == "recovery-restore" {
		service = "host-recovery-restore"
		archive, err := privateRecoveryArchive(action.Recovery.ArchivePath, action.Recovery.Service, passphrase)
		if err != nil {
			return model.Job{}, err
		}
		defer clear(archive)
		if err := os.WriteFile(filepath.Join(directory, "archive"), archive, 0o600); err != nil {
			return model.Job{}, err
		}
	}
	now := time.Now().UTC()
	job := model.Job{ID: id, RequestID: action.RequestID, HeadID: action.Recovery.Service, Service: service, State: "REQUESTED", CreatedAt: now, UpdatedAt: now}
	if err := s.Store.Save(job); err != nil {
		return model.Job{}, err
	}
	if err := exec.Command("systemd-run", "--unit=exocortex-host-recovery", "--collect", "--property=Type=exec", "--property=RuntimeMaxSec=900", "/usr/bin/updater", "host-recovery-job", id).Run(); err != nil {
		job.State = "FAILED"
		job.Message = "Cannot start host recovery supervisor"
		job.FinishedAt = &now
		_ = s.Store.Save(job)
		return model.Job{}, errors.New(job.Message)
	}
	launched = true
	return job, nil
}

func (s Server) operatorRecoveryAction(w http.ResponseWriter, request *http.Request, action console.Action) {
	lifecycleStart.Lock()
	defer lifecycleStart.Unlock()
	if !operatorRequestID.MatchString(action.RequestID) {
		writeError(w, 400, errors.New("A valid operation request ID is required"))
		return
	}
	if err := console.ValidateAction(action); err != nil {
		writeError(w, 400, err)
		return
	}
	release, err := s.recoveryReady()
	if err != nil {
		writeError(w, 409, err)
		return
	}
	defer release()
	var job model.Job
	if action.Kind == "recovery-configure" {
		job, err = s.completedRecoveryConfiguration(request.Context(), action)
	} else if action.Kind == "recovery-key-export" {
		err = config.ExportRecoveryKey(s.Runtime, action.Recovery.Service, action.Recovery.KeyPath)
		if err == nil {
			var id string
			id, err = recoveryJobID()
			now := time.Now().UTC()
			job = model.Job{ID: id, RequestID: action.RequestID, HeadID: action.Recovery.Service, Service: "host-recovery-key-export", State: "COMPLETED", Message: "Recovery key exported to a private file; keep it outside this host", CreatedAt: now, UpdatedAt: now, FinishedAt: &now}
			if err == nil {
				err = s.Store.Save(job)
			}
		}
	} else {
		job, err = s.stageRecoveryJob(action)
	}
	if action.Recovery != nil {
		action.Recovery.Passphrase = ""
		for service := range action.Recovery.EnrollmentCodes {
			action.Recovery.EnrollmentCodes[service] = ""
		}
	}
	if err != nil {
		writeError(w, 500, err)
		return
	}
	status := 202
	if job.FinishedAt != nil {
		status = 200
	}
	writeJSON(w, status, operatorJob(job))
}
