package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"strings"
	"time"
	"updater/internal/component"
	"updater/internal/model"
	"updater/internal/release"
)

func (s Server) componentUpdates(mux *http.ServeMux) {
	mux.HandleFunc("POST /v2/components/{component}/updates", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			HeadID    string `json:"head_id"`
			Version   string `json:"version"`
			RequestID string `json:"request_id"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, 400, err)
			return
		}
		if err := s.authorize(r, input.HeadID); err != nil {
			writeError(w, 401, err)
			return
		}
		kind := r.PathValue("component")
		if kind != "updater" && kind != "neptune" && kind != "gryphon" {
			writeError(w, 400, errors.New("unknown component"))
			return
		}
		if len(input.RequestID) < 16 || len(input.RequestID) > 128 || !release.Stable(input.Version) {
			writeError(w, 400, errors.New("request_id and an exact stable version are required"))
			return
		}
		lifecycleStart.Lock()
		defer lifecycleStart.Unlock()
		service := kind + "-update"
		if kind == "updater" {
			service = "updater-self-update"
		}
		if job, ok := s.Store.ByRequestID(input.RequestID); ok {
			if job.HeadID != input.HeadID || job.Service != service || job.Version != input.Version {
				writeError(w, 409, errors.New("request id is already in use"))
				return
			}
			writeJSON(w, 200, job)
			return
		}
		candidate, err := s.candidate(input.HeadID, kind)
		if err != nil {
			writeError(w, 502, err)
			return
		}
		if !candidate.UpdateAvailable || candidate.AvailableVersion != input.Version {
			writeError(w, 409, errors.New("selected version is no longer the upgrade candidate; check again"))
			return
		}
		unlock, err := s.Store.BeginOperation("")
		if err != nil {
			writeError(w, 409, err)
			return
		}
		handedOff := false
		defer func() {
			if !handedOff {
				unlock()
			}
		}()
		random := make([]byte, 16)
		if _, err = rand.Read(random); err != nil {
			writeError(w, 500, err)
			return
		}
		now := time.Now().UTC()
		job := model.Job{ID: "component-" + hex.EncodeToString(random), RequestID: input.RequestID, HeadID: input.HeadID, Service: service, Version: input.Version, State: "REQUESTED", CreatedAt: now, UpdatedAt: now}
		job.Progress = model.JobProgress(job)
		if err = s.Store.Save(job); err != nil {
			writeError(w, 500, err)
			return
		}
		if kind == "updater" {
			err = exec.Command("systemd-run", "--unit=exocortex-updater-self-update", "--collect", "--property=Type=exec", "--property=RuntimeMaxSec=600", "/usr/bin/updater", "self-update-job", job.ID).Run()
			if err != nil {
				job.State = "FAILED"
				job.Message = "Cannot start the self-update supervisor"
				job.FinishedAt = &now
				_ = s.Store.Save(job)
			}
		} else {
			handedOff = true
			go func() { defer unlock(); s.runComponentUpdate(job, kind) }()
		}
		writeJSON(w, 202, job)
	})
}

func (s Server) runComponentUpdate(job model.Job, kind string) {
	job.State = "INSTALLING"
	job.Message = "Verifying the signed release and replacing the component"
	job.UpdatedAt = time.Now().UTC()
	_ = s.Store.Save(job)
	var err error
	if kind == "neptune" {
		err = component.UpdateNeptune(s.Runtime, job.HeadID, job.Version)
	} else {
		err = component.UpdateGryphon(s.Runtime, job.HeadID, job.Version)
	}
	if err == nil {
		job.State = "HEALTH_CHECK"
		job.Message = "Verifying the running component version"
		job.UpdatedAt = time.Now().UTC()
		_ = s.Store.Save(job)
		var installed string
		installed, err = component.InstalledVersion(kind)
		if err == nil && strings.TrimSuffix(installed, "-dev") != job.Version {
			err = errors.New("running component version does not match the selected release")
		}
		if err == nil {
			job.InstalledVersion = installed
		}
	}
	job.UpdatedAt = time.Now().UTC()
	job.FinishedAt = &job.UpdatedAt
	if err != nil {
		job.State = "FAILED"
		job.Message = err.Error()
	} else {
		job.State = "COMPLETED"
		job.Message = "Update completed and running version verified"
	}
	_ = s.Store.Save(job)
}
