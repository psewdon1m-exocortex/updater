package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"updater/internal/component"
	"updater/internal/console"
	"updater/internal/model"
)

func (s Server) operatorGryphonBot(w http.ResponseWriter, action console.Action) {
	if !operatorRequestID.MatchString(action.RequestID) || action.HeadID != "" {
		writeError(w, 400, errors.New("host Gryphon bot operation requires a request ID and no service"))
		return
	}
	if err := console.ValidateAction(action); err != nil {
		writeError(w, 400, err)
		return
	}
	lifecycleStart.Lock()
	defer lifecycleStart.Unlock()
	if previous, ok := s.Store.ByRequestID(action.RequestID); ok {
		if previous.Service != "gryphon-bot" || previous.HeadID != "" {
			writeError(w, 409, errors.New("request id is already in use"))
			return
		}
		writeJSON(w, 200, operatorJob(previous))
		return
	}
	for _, listed := range s.Store.List() {
		if listed.Service == "gryphon-bot" && listed.FinishedAt == nil {
			writeError(w, 409, errors.New("a bot registration is already running"))
			return
		}
	}
	unlock, err := s.Store.BeginOperation("")
	if err != nil {
		writeError(w, 409, err)
		return
	}
	defer unlock()
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		writeError(w, 500, err)
		return
	}
	now := time.Now().UTC()
	job := model.Job{ID: "component-" + hex.EncodeToString(bytes), RequestID: action.RequestID, Service: "gryphon-bot", State: "REQUESTED", CreatedAt: now, UpdatedAt: now}
	if err := s.Store.Save(job); err != nil {
		writeError(w, 500, err)
		return
	}
	job.State = "INSTALLING"
	job.UpdatedAt = time.Now().UTC()
	_ = s.Store.Save(job)
	pairing, err := component.ConnectGryphonBot(action.Alias, action.BotToken)
	action.BotToken = ""
	job.UpdatedAt = time.Now().UTC()
	job.FinishedAt = &job.UpdatedAt
	if err != nil {
		job.State, job.Message = "FAILED", err.Error()
	} else {
		job.State, job.Message = "COMPLETED", "Bot registered; send the pairing command in Telegram"
		if pairing.Command == "" {
			job.Message = "Bot already paired with the shared Gryphon gateway"
		}
	}
	_ = s.Store.Save(job)
	result := operatorJob(job)
	result.PairCommand = pairing.Command
	result.PairExpiresAt = pairing.ExpiresAt
	result.PairBotUsername = pairing.BotUsername
	writeJSON(w, http.StatusAccepted, result)
}
