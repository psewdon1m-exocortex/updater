package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
	"updater/internal/component"
	"updater/internal/config"
	"updater/internal/kernel"
	"updater/internal/model"
	"updater/internal/release"
)

func (s Server) candidate(headID, kind string) (release.Candidate, error) {
	head, err := config.LoadHead(s.Runtime, headID)
	if err != nil {
		return release.Candidate{}, err
	}
	current := head.CurrentVersion
	if kind != head.Service {
		if kind != "updater" && !component.ConsumesHelper(head.Service, kind) {
			return release.Candidate{}, errors.New("head does not consume this component")
		}
		if kind == "updater" {
			current = s.Version
		} else {
			current, err = component.InstalledVersion(kind)
			if err != nil {
				return release.Candidate{}, err
			}
		}
	}
	snapshot, err := kernel.Load(head.KernelURL, head.KernelServiceToken, head.KernelCachePath, 5*time.Second)
	if err != nil {
		return release.Candidate{}, err
	}
	repository, err := kernel.String(snapshot, "repositories."+kind+".url")
	if err != nil {
		return release.Candidate{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := release.Discover(ctx, repository, kind, current)
	result.BackupRequired = kind == head.Service
	result.UpdaterVersion = s.Version
	return result, err
}

func (s Server) protocol(mux *http.ServeMux) {
	mux.HandleFunc("POST /v2/check", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			HeadID    string `json:"head_id"`
			Component string `json:"component"`
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
		result, err := s.candidate(input.HeadID, input.Component)
		if err != nil {
			writeError(w, 502, err)
			return
		}
		writeJSON(w, 200, result)
	})
	install := func(w http.ResponseWriter, r *http.Request) {
		var input model.UpdateRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 180*1024*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			writeError(w, 400, err)
			return
		}
		if err := s.authorize(r, input.HeadID); err != nil {
			writeError(w, 401, err)
			return
		}
		head, err := config.LoadHead(s.Runtime, input.HeadID)
		if err != nil {
			writeError(w, 400, err)
			return
		}
		if input.BackupReceipt == "" {
			writeError(w, 426, errors.New("update protocol 2 requires a saved ZIP and signed backup receipt; update the head integration first"))
			return
		}
		job, err := s.Engine.StartDownloaded(input, head.ControlToken)
		if err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, 202, job)
	}
	mux.HandleFunc("POST /v2/updates", install)
	mux.HandleFunc("POST /v1/updates", install)
	mux.HandleFunc("POST /v2/jobs/{id}/rollback", func(w http.ResponseWriter, r *http.Request) {
		job, ok := s.Store.Get(r.PathValue("id"))
		if !ok {
			writeError(w, 404, errors.New("job not found"))
			return
		}
		if err := s.authorize(r, job.HeadID); err != nil {
			writeError(w, 401, err)
			return
		}
		var backup model.Backup
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 180*1024*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&backup); err != nil {
			writeError(w, 400, err)
			return
		}
		result, err := s.Engine.RollbackDownloaded(job.ID, backup)
		if err != nil {
			writeError(w, 409, err)
			return
		}
		writeJSON(w, 202, result)
	})
}
