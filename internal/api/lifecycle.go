package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"sync"
	"time"
	"updater/internal/component"
	"updater/internal/config"
	"updater/internal/model"
)

type lifecycleRequest struct {
	HeadID   string `json:"head_id"`
	Alias    string `json:"alias,omitempty"`
	BotToken string `json:"bot_token,omitempty"`
}

var lifecycleStart sync.Mutex

func (s Server) lifecycle(mux *http.ServeMux) {
	for _, kind := range []string{"gryphon-initialization", "gryphon-bot", "updater-self-update"} {
		mux.HandleFunc("POST /v1/lifecycle/"+kind, func(w http.ResponseWriter, r *http.Request) {
			lifecycleStart.Lock()
			defer lifecycleStart.Unlock()
			r.Body = http.MaxBytesReader(w, r.Body, 16384)
			var input lifecycleRequest
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				writeError(w, 400, errors.New("invalid lifecycle request"))
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
			if kind != "updater-self-update" && head.Service != "saturn" {
				writeError(w, 403, errors.New("head does not consume Gryphon"))
				return
			}
			if kind != "gryphon-bot" && (input.Alias != "" || input.BotToken != "") {
				writeError(w, 400, errors.New("unexpected bot credentials"))
				return
			}
			for _, listed := range s.Store.List() {
				job, _ := s.Store.Get(listed.ID)
				if job.Service == kind && job.FinishedAt == nil {
					writeError(w, 409, errors.New("component operation is already running"))
					return
				}
			}
			bytes := make([]byte, 16)
			releaseOperation, err := s.Store.BeginOperation("")
			if err != nil {
				writeError(w, 409, err)
				return
			}
			handedOff := false
			defer func() {
				if !handedOff {
					releaseOperation()
				}
			}()
			if _, err := rand.Read(bytes); err != nil {
				writeError(w, 500, err)
				return
			}
			now := time.Now().UTC()
			job := model.Job{ID: "component-" + hex.EncodeToString(bytes), HeadID: input.HeadID, Service: kind, State: "REQUESTED", CreatedAt: now, UpdatedAt: now}
			if err := s.Store.Save(job); err != nil {
				writeError(w, 500, err)
				return
			}
			if kind == "updater-self-update" {
				// The supervisor survives replacement/restart of updater.service.
				command := exec.Command("systemd-run", "--unit=exocortex-updater-self-update", "--collect", "--property=Type=exec", "--property=RuntimeMaxSec=600", "/usr/bin/updater", "self-update-job", job.ID)
				if err := command.Run(); err != nil {
					job.State = "FAILED"
					job.Message = "Cannot start the self-update supervisor"
					job.FinishedAt = &now
					_ = s.Store.Save(job)
					writeError(w, 503, errors.New(job.Message))
					return
				}
			} else {
				handedOff = true
				go func(job model.Job, input lifecycleRequest) {
					defer releaseOperation()
					job.State = "INSTALLING"
					job.UpdatedAt = time.Now().UTC()
					_ = s.Store.Save(job)
					var err error
					if kind == "gryphon-initialization" {
						job.Version, err = component.InitializeGryphon(s.Runtime, input.HeadID)
					} else {
						err = component.ConnectGryphonBot(s.Runtime, input.HeadID, input.Alias, input.BotToken)
						input.BotToken = ""
					}
					job.UpdatedAt = time.Now().UTC()
					job.FinishedAt = &job.UpdatedAt
					if err != nil {
						job.State = "FAILED"
						job.Message = err.Error()
					} else {
						job.State = "COMPLETED"
						job.Message = "Component operation completed and verified"
					}
					_ = s.Store.Save(job)
				}(job, input)
			}
			writeJSON(w, http.StatusAccepted, job)
		})
	}
}
