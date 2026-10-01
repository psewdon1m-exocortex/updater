package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"updater/internal/component"
	"updater/internal/console"
)

var windowAdminSocket = "/run/window-admin/admin.sock"

// PairWindowPublicKey is the local root CLI's first-time pairing path. The
// password is handled by SSH and sudo; this method receives only a public key.
func PairWindowPublicKey(ctx context.Context, publicKey string) (string, error) {
	status, err := windowRequest(ctx, "POST", "/v1/pair", map[string]string{"key": publicKey})
	if err != nil {
		return "", err
	}
	return status.Fingerprint, nil
}

type windowStatus struct {
	Paired      bool      `json:"paired"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	Open        bool      `json:"open"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
	Live        bool      `json:"live"`
	LeaseID     string    `json:"lease_id,omitempty"`
}

func windowRequest(ctx context.Context, method, route string, input any) (windowStatus, error) {
	var body bytes.Buffer
	if input != nil {
		if err := json.NewEncoder(&body).Encode(input); err != nil {
			return windowStatus{}, err
		}
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", windowAdminSocket)
	}}
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, "http://window.local"+route, &body)
	if err != nil {
		return windowStatus{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return windowStatus{}, errors.New("Window is unavailable")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8192))
	if err != nil {
		return windowStatus{}, err
	}
	if response.StatusCode != 200 {
		var failure struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &failure) == nil && failure.Error != "" {
			return windowStatus{}, errors.New(console.Text(failure.Error))
		}
		return windowStatus{}, fmt.Errorf("Window returned HTTP %d", response.StatusCode)
	}
	var status windowStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return windowStatus{}, err
	}
	return status, nil
}

func (s Server) operatorWindowAction(w http.ResponseWriter, r *http.Request, action console.Action) {
	if !operatorRequestID.MatchString(action.RequestID) {
		writeError(w, 400, errors.New("A valid request ID is required"))
		return
	}
	if err := console.ValidateAction(action); err != nil {
		writeError(w, 400, err)
		return
	}
	var route string
	var input any
	if action.Kind == "repair" {
		version, err := component.RepairWindow(s.Runtime, s.Version)
		if err != nil {
			writeError(w, 503, err)
			return
		}
		now := time.Now().UTC()
		writeJSON(w, 200, console.Job{ID: action.RequestID, RequestID: action.RequestID, Component: "window", Version: version, State: "COMPLETED", UpdatedAt: now, Finished: true, Summary: "Window repaired and health verified; any prior grant is closed"})
		return
	}
	switch action.Kind {
	case "pair":
		route, input = "/v1/pair", map[string]string{"key": action.PublicKey}
	case "open":
		route, input = "/v1/open", map[string]int{"minutes": action.Minutes}
	case "revoke":
		route = "/v1/revoke"
	default:
		writeError(w, 400, errors.New("Unsupported Window action"))
		return
	}
	status, err := windowRequest(r.Context(), "POST", route, input)
	if err != nil {
		writeError(w, 503, err)
		return
	}
	now := time.Now().UTC()
	job := console.Job{ID: action.RequestID, RequestID: action.RequestID, Component: "window", State: "COMPLETED", UpdatedAt: now, Finished: true, Summary: "Window grant closed"}
	if action.Kind == "pair" {
		job.Summary = "Development PC paired; previous grant closed"
	}
	if action.Kind == "open" {
		job.Summary = fmt.Sprintf("Window grant open for %d minutes while this TUI stays connected", action.Minutes)
		job.WindowLeaseID = status.LeaseID
	}
	writeJSON(w, 200, job)
}

func (s Server) operatorWindowHeartbeat(w http.ResponseWriter, r *http.Request) {
	var input struct {
		LeaseID string `json:"lease_id"`
	}
	if !operatorDecode(w, r, &input) {
		return
	}
	if len(input.LeaseID) != 48 {
		writeError(w, 400, errors.New("Invalid Window grant"))
		return
	}
	status, err := windowRequest(r.Context(), "POST", "/v1/heartbeat", input)
	if err != nil {
		writeError(w, 403, err)
		return
	}
	writeJSON(w, 200, status)
}

func (s Server) operatorWindowClose(w http.ResponseWriter, r *http.Request) {
	var input struct {
		LeaseID string `json:"lease_id"`
	}
	if !operatorDecode(w, r, &input) {
		return
	}
	if len(input.LeaseID) != 48 {
		writeError(w, 400, errors.New("Invalid Window grant"))
		return
	}
	status, err := windowRequest(r.Context(), "POST", "/v1/close", input)
	if err != nil {
		writeError(w, 503, err)
		return
	}
	writeJSON(w, 200, status)
}
