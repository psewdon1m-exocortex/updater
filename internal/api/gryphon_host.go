package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"time"

	"updater/internal/component"
	"updater/internal/config"
	"updater/internal/console"
	"updater/internal/kernel"
	"updater/internal/model"
)

// Gryphon and Wyvern have one runtime each per host. Registered consumers
// provide release metadata, but they must agree before a host-wide update.
func (s Server) gryphonReleaseHead() (string, error) { return s.sharedReleaseHead("gryphon") }
func (s Server) wyvernReleaseHead() (string, error)  { return s.sharedReleaseHead("wyvern") }

func (s Server) sharedReleaseHead(kind string) (string, error) {
	registry, err := config.LoadRegistry(s.Runtime.RegistryPath)
	if err != nil {
		return "", err
	}
	ids := make([]string, 0, len(registry.Heads))
	for id := range registry.Heads {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	selected, repository := "", ""
	for _, id := range ids {
		head, err := config.LoadHead(s.Runtime, id)
		if err != nil || !component.ConsumesHelper(head.Service, kind) {
			continue
		}
		snapshot, err := kernel.Load(head.KernelURL, head.KernelServiceToken, head.KernelCachePath, 5*time.Second)
		if err != nil {
			return "", err
		}
		value, err := kernel.String(snapshot, "repositories."+kind+".url")
		if err != nil {
			return "", err
		}
		if selected == "" {
			selected, repository = id, value
		} else if value != repository {
			return "", errors.New("registered services disagree on the " + kind + " release repository")
		}
	}
	if selected == "" {
		return "", errors.New("no registered " + kind + " consumer provides release configuration")
	}
	return selected, nil
}

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
