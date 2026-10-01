package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"updater/internal/config"
	"updater/internal/console"
	"updater/internal/model"
)

func hostComponent(kind string) bool {
	return kind == "updater" || kind == "neptune" || kind == "gryphon" || kind == "wyvern" || kind == "window"
}

func (s Server) operatorSetKernel(w http.ResponseWriter, action console.Action) {
	if !operatorRequestID.MatchString(action.RequestID) {
		writeError(w, 400, errors.New("A valid operation request ID is required"))
		return
	}
	if err := console.ValidateAction(action); err != nil {
		writeError(w, 400, err)
		return
	}
	lifecycleStart.Lock()
	defer lifecycleStart.Unlock()
	if prior, ok := s.Store.ByRequestID(action.RequestID); ok {
		if prior.Service != "updater-kernel-connection" {
			writeError(w, 409, errors.New("request ID is already in use"))
			return
		}
		writeJSON(w, 200, operatorJob(prior))
		return
	}
	unlock, err := s.Store.BeginOperation("")
	if err != nil {
		writeError(w, 409, err)
		return
	}
	defer unlock()
	host, err := config.LoadHost(s.Runtime)
	if err != nil {
		writeError(w, 503, err)
		return
	}
	if host.KernelURL != "" && (host.KernelURL != action.KernelURL || host.KernelTokenFile != action.KernelTokenFile || host.HostID != action.HostID) {
		writeError(w, 409, errors.New("Existing host Kernel binding differs; use explicit migration or repair"))
		return
	}
	host.KernelURL, host.KernelTokenFile, host.HostID = action.KernelURL, action.KernelTokenFile, action.HostID
	if _, err := config.HostKernelToken(host); err != nil {
		writeError(w, 400, err)
		return
	}
	if err := config.SaveHost(s.Runtime, host); err != nil {
		writeError(w, 500, err)
		return
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		writeError(w, 500, err)
		return
	}
	now := time.Now().UTC()
	job := model.Job{ID: "component-" + hex.EncodeToString(random), RequestID: action.RequestID, Service: "updater-kernel-connection",
		State: "COMPLETED", Message: "Host Kernel machine connection saved", CreatedAt: now, UpdatedAt: now, FinishedAt: &now}
	if err := s.Store.Save(job); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, operatorJob(job))
}

func (s Server) operatorSetSource(w http.ResponseWriter, action console.Action) {
	if !operatorRequestID.MatchString(action.RequestID) {
		writeError(w, 400, errors.New("A valid operation request ID is required"))
		return
	}
	if err := console.ValidateAction(action); err != nil {
		writeError(w, 400, err)
		return
	}
	lifecycleStart.Lock()
	defer lifecycleStart.Unlock()
	service := action.Component + "-source"
	if prior, ok := s.Store.ByRequestID(action.RequestID); ok {
		if prior.Service != service || prior.HeadID != "" {
			writeError(w, 409, errors.New("request ID is already in use"))
			return
		}
		writeJSON(w, 200, operatorJob(prior))
		return
	}
	unlock, err := s.Store.BeginOperation("")
	if err != nil {
		writeError(w, 409, err)
		return
	}
	defer unlock()
	host, err := config.LoadHost(s.Runtime)
	if err != nil {
		writeError(w, 503, err)
		return
	}
	if host.ReleaseSources == nil {
		host.ReleaseSources = map[string]string{}
	}
	host.ReleaseSources[action.Component] = action.RepositoryURL
	if err := config.SaveHost(s.Runtime, host); err != nil {
		writeError(w, 500, err)
		return
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		writeError(w, 500, err)
		return
	}
	now := time.Now().UTC()
	job := model.Job{ID: "component-" + hex.EncodeToString(random), RequestID: action.RequestID, Service: service,
		State: "COMPLETED", Message: "Host fallback release source saved", CreatedAt: now, UpdatedAt: now, FinishedAt: &now}
	if err := s.Store.Save(job); err != nil {
		writeError(w, 500, err)
		return
	}
	writeJSON(w, 200, operatorJob(job))
}

func (s Server) operatorHostRelease(w http.ResponseWriter, r *http.Request, action console.Action) {
	if action.HeadID != "" || !operatorRequestID.MatchString(action.RequestID) || !hostComponent(action.Component) {
		writeError(w, 400, errors.New("Host release operation requires a component, request ID and no service selection"))
		return
	}
	if err := console.ValidateAction(action); err != nil {
		writeError(w, 400, err)
		return
	}
	path := "/v2/components/" + action.Component + "/updates"
	var payload any = map[string]string{"version": action.Version, "request_id": action.RequestID}
	if action.Kind == "install" {
		if action.Component == "updater" {
			writeError(w, 400, errors.New("Bootstrap Updater before opening the TUI"))
			return
		}
		name := action.Component + "-installation"
		if action.Component == "gryphon" {
			name = "gryphon-initialization"
		}
		path = "/v1/lifecycle/" + name
		payload = lifecycleRequest{RequestID: action.RequestID}
	}
	body, _ := json.Marshal(payload)
	request, _ := http.NewRequestWithContext(context.WithValue(r.Context(), operatorDispatchKey{}, true), http.MethodPost, "http://updater.local"+path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := &operatorResponse{header: http.Header{}}
	s.Handler().ServeHTTP(response, request)
	if response.status != http.StatusOK && response.status != http.StatusAccepted {
		writeError(w, response.status, errors.New("Host operation rejected; inspect release source, compatibility and job history"))
		return
	}
	var job model.Job
	if json.Unmarshal(response.body.Bytes(), &job) != nil || job.ID == "" {
		writeError(w, 502, errors.New("Invalid host operation receipt; inspect job history"))
		return
	}
	writeJSON(w, response.status, operatorJob(job))
}
