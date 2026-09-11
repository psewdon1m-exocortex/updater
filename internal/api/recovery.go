package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
	"updater/internal/config"
	"updater/internal/hostrecovery"
	"updater/internal/model"
)

type recoveryInput struct {
	HeadID       string `json:"head_id"`
	Passphrase   string `json:"passphrase"`
	Archive      string `json:"archive_base64,omitempty"`
	Confirmation string `json:"confirmation,omitempty"`
}

func (s Server) recovery(mux *http.ServeMux) {
	for _, action := range []string{"export", "restore"} {
		mux.HandleFunc("POST /v1/host-recovery/"+action, func(w http.ResponseWriter, r *http.Request) {
			lifecycleStart.Lock()
			defer lifecycleStart.Unlock()
			r.Body = http.MaxBytesReader(w, r.Body, 180*1024*1024)
			var input recoveryInput
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				writeError(w, 400, errors.New("invalid recovery request"))
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
			values, err := config.ParseEnvFile(head.EnvFile)
			if err != nil || head.Service != "saturn" || values["UPDATER_HOST_RECOVERY_ALLOWED"] != "true" {
				writeError(w, 403, errors.New("host recovery is not delegated to this head by the host installer"))
				return
			}
			if len(input.Passphrase) < 16 || len(input.Passphrase) > 1024 {
				writeError(w, 400, errors.New("recovery passphrase must contain 16 to 1024 bytes"))
				return
			}
			if s.Engine.Busy() {
				writeError(w, 409, errors.New("an update is running"))
				return
			}
			releaseOperation, err := s.Store.BeginOperation("")
			if err != nil {
				writeError(w, 409, err)
				return
			}
			defer releaseOperation()
			for _, listed := range s.Store.List() {
				job, _ := s.Store.Get(listed.ID)
				if job.FinishedAt == nil {
					writeError(w, 409, errors.New("a component job is running"))
					return
				}
			}
			if action == "export" {
				if input.Archive != "" || input.Confirmation != "" {
					writeError(w, 400, errors.New("unexpected export fields"))
					return
				}
				archive, err := hostrecovery.Export(input.Passphrase)
				input.Passphrase = ""
				if err != nil {
					writeError(w, 500, err)
					return
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Disposition", "attachment; filename=exocortex-helpers.exorecovery")
				w.WriteHeader(200)
				_, _ = w.Write(archive)
				return
			}
			if input.Confirmation != "RESTORE HELPERS" {
				writeError(w, 400, errors.New("explicit helper replacement confirmation is required"))
				return
			}
			archive, err := base64.StdEncoding.DecodeString(input.Archive)
			input.Archive = ""
			if err != nil {
				writeError(w, 400, errors.New("invalid archive encoding"))
				return
			}
			// Authenticate and validate every member before creating the privileged job.
			entries, err := hostrecovery.Open(archive, input.Passphrase)
			if err != nil {
				writeError(w, 400, err)
				return
			}
			for _, entry := range entries {
				clear(entry.Data)
			}
			random := make([]byte, 16)
			if _, err := rand.Read(random); err != nil {
				writeError(w, 500, err)
				return
			}
			id := "component-" + hex.EncodeToString(random)
			directory := filepath.Join(s.Runtime.StateDir, "recovery-jobs", id)
			if err := os.MkdirAll(directory, 0700); err != nil {
				writeError(w, 500, err)
				return
			}
			launched := false
			defer func() {
				if !launched {
					_ = os.RemoveAll(directory)
				}
			}()
			if err := os.WriteFile(filepath.Join(directory, "archive"), archive, 0600); err != nil {
				writeError(w, 500, err)
				return
			}
			if err := os.WriteFile(filepath.Join(directory, "key"), []byte(input.Passphrase), 0600); err != nil {
				writeError(w, 500, err)
				return
			}
			input.Passphrase = ""
			now := time.Now().UTC()
			job := model.Job{ID: id, HeadID: input.HeadID, Service: "host-recovery", State: "REQUESTED", CreatedAt: now, UpdatedAt: now}
			if err := s.Store.Save(job); err != nil {
				_ = os.RemoveAll(directory)
				writeError(w, 500, err)
				return
			}
			if err := exec.Command("systemd-run", "--unit=exocortex-host-recovery", "--collect", "--property=Type=exec", "--property=RuntimeMaxSec=600", "/usr/bin/updater", "host-recovery-job", id).Run(); err != nil {
				_ = os.RemoveAll(directory)
				job.State = "FAILED"
				job.Message = "Cannot start host recovery supervisor"
				job.FinishedAt = &now
				_ = s.Store.Save(job)
				writeError(w, 503, errors.New(job.Message))
				return
			}
			launched = true
			writeJSON(w, 202, job)
		})
	}
}
