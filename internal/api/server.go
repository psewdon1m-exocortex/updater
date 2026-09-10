package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"updater/internal/component"
	"updater/internal/config"
	"updater/internal/engine"
	"updater/internal/model"
	"updater/internal/state"
)

type Server struct {
	Version string
	Runtime config.Runtime
	Store   *state.Store
	Engine  *engine.Engine
	OnReady func()
}

func (s Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status": "ok", "service": "updater", "version": s.Version, "busy": s.Engine.Busy(),
		})
	})
	mux.HandleFunc("GET /v1/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"service": "updater", "version": s.Version})
	})
	mux.HandleFunc("GET /v1/services", func(w http.ResponseWriter, _ *http.Request) {
		registry, err := config.LoadRegistry(s.Runtime.RegistryPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		items := make([]map[string]string, 0, len(registry.Heads))
		for id, head := range registry.Heads {
			items = append(items, map[string]string{"id": id, "env_file": head.EnvFile})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"services": items})
	})
	mux.HandleFunc("GET /v1/jobs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]interface{}{"jobs": s.Store.List()})
	})
	mux.HandleFunc("GET /v1/jobs/{id}", func(w http.ResponseWriter, request *http.Request) {
		job, ok := s.Store.Get(request.PathValue("id"))
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("job not found"))
			return
		}
		writeJSON(w, http.StatusOK, job)
	})
	mux.HandleFunc("POST /v1/updates", func(w http.ResponseWriter, request *http.Request) {
		request.Body = http.MaxBytesReader(w, request.Body, 180*1024*1024)
		var payload model.UpdateRequest
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid update request: %w", err))
			return
		}
		if err := s.authorize(request, payload.HeadID); err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		job, err := s.Engine.Start(payload)
		if err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "already running") {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusAccepted, job)
	})
	mux.HandleFunc("POST /v1/components/neptune-linux/update", func(w http.ResponseWriter, request *http.Request) {
		request.Body = http.MaxBytesReader(w, request.Body, 64*1024)
		var payload struct {
			HeadID  string `json:"head_id"`
			Version string `json:"version"`
		}
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid Neptune update request: %w", err))
			return
		}
		if err := s.authorize(request, payload.HeadID); err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		if err := component.UpdateNeptune(s.Runtime, payload.HeadID, payload.Version); err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "already running") {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"updated": true, "version": payload.Version})
	})
	mux.HandleFunc("POST /v1/components/neptune-linux/initialize", func(w http.ResponseWriter, request *http.Request) {
		request.Body = http.MaxBytesReader(w, request.Body, 64*1024)
		var payload model.NeptuneInitializationRequest
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid Neptune initialization request: %w", err))
			return
		}
		if err := s.authorize(request, payload.HeadID); err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		job, err := component.StartNeptuneInitialization(s.Runtime, s.Store, payload)
		if err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "already running") {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusAccepted, job)
	})
	mux.HandleFunc("GET /v1/components/neptune-linux/initializations/{id}", func(w http.ResponseWriter, request *http.Request) {
		headID := request.URL.Query().Get("head_id")
		if err := s.authorize(request, headID); err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		job, ok := s.Store.Get(request.PathValue("id"))
		if !ok || job.HeadID != headID || job.Service != "neptune-initialization" {
			writeError(w, http.StatusNotFound, errors.New("Neptune initialization job not found"))
			return
		}
		writeJSON(w, http.StatusOK, job)
	})
	mux.HandleFunc("POST /v1/agent/neptune-linux/update", func(w http.ResponseWriter, request *http.Request) {
		request.Body = http.MaxBytesReader(w, request.Body, 64*1024)
		var payload struct {
			HeadID  string `json:"head_id"`
			Version string `json:"version"`
		}
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid Neptune agent update request: %w", err))
			return
		}
		if err := authorizeNeptuneAgent(request); err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		if err := component.UpdateNeptune(s.Runtime, payload.HeadID, payload.Version); err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "already running") {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"updated": true, "version": payload.Version})
	})
	mux.HandleFunc("POST /v1/components/neptune-linux/check", func(w http.ResponseWriter, request *http.Request) {
		request.Body = http.MaxBytesReader(w, request.Body, 64*1024)
		var payload struct {
			HeadID         string `json:"head_id"`
			CurrentVersion string `json:"current_version"`
		}
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid Neptune check request: %w", err))
			return
		}
		if err := s.authorize(request, payload.HeadID); err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		result, err := component.CheckNeptune(s.Runtime, payload.HeadID, payload.CurrentVersion)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /v1/components/gryphon-linux/update", func(w http.ResponseWriter, request *http.Request) {
		request.Body = http.MaxBytesReader(w, request.Body, 64*1024)
		var payload struct {
			HeadID  string `json:"head_id"`
			Version string `json:"version"`
		}
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid Gryphon update request: %w", err))
			return
		}
		if err := s.authorize(request, payload.HeadID); err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		if err := component.UpdateGryphon(s.Runtime, payload.HeadID, payload.Version); err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "already running") {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"updated": true, "version": payload.Version})
	})
	mux.HandleFunc("POST /v1/components/gryphon-linux/check", func(w http.ResponseWriter, request *http.Request) {
		request.Body = http.MaxBytesReader(w, request.Body, 64*1024)
		var payload struct {
			HeadID         string `json:"head_id"`
			CurrentVersion string `json:"current_version"`
		}
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid Gryphon check request: %w", err))
			return
		}
		if err := s.authorize(request, payload.HeadID); err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		result, err := component.CheckGryphon(s.Runtime, payload.HeadID, payload.CurrentVersion)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /v1/jobs/{id}/rollback", func(w http.ResponseWriter, request *http.Request) {
		existing, ok := s.Store.Get(request.PathValue("id"))
		if !ok {
			writeError(w, http.StatusNotFound, errors.New("job not found"))
			return
		}
		if err := s.authorize(request, existing.HeadID); err != nil {
			writeError(w, http.StatusUnauthorized, err)
			return
		}
		job, err := s.Engine.Rollback(request.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusAccepted, job)
	})
	return withLocalHeaders(mux)
}

func (s Server) authorize(request *http.Request, headID string) error {
	head, err := config.LoadHead(s.Runtime, headID)
	if err != nil {
		return err
	}
	provided := request.Header.Get("X-Updater-Token")
	if len(provided) != len(head.ControlToken) ||
		subtle.ConstantTimeCompare([]byte(provided), []byte(head.ControlToken)) != 1 {
		return errors.New("invalid updater control token")
	}
	return nil
}

func authorizeNeptuneAgent(request *http.Request) error {
	path := strings.TrimSpace(os.Getenv("NEPTUNE_UPDATER_TOKEN_FILE"))
	if path == "" {
		path = "/etc/neptune/updater-agent.token"
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return errors.New("Neptune updater bridge is not configured")
	}
	expected := strings.TrimSpace(string(body))
	provided := request.Header.Get("X-Neptune-Updater-Token")
	if len(provided) != len(expected) || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		return errors.New("invalid Neptune updater bridge token")
	}
	return nil
}

func (s Server) ListenAndServe() error {
	if err := os.MkdirAll(filepath.Dir(s.Runtime.SocketPath), 0o750); err != nil {
		return err
	}
	if _, err := os.Stat(s.Runtime.SocketPath); err == nil {
		existing, dialErr := net.DialTimeout(
			"unix",
			s.Runtime.SocketPath,
			500*time.Millisecond,
		)
		if dialErr == nil {
			_ = existing.Close()
			return fmt.Errorf(
				"another updater is already listening on %s",
				s.Runtime.SocketPath,
			)
		}
		if removeErr := os.Remove(s.Runtime.SocketPath); removeErr != nil {
			return fmt.Errorf("remove stale updater socket: %w", removeErr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", s.Runtime.SocketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(s.Runtime.SocketPath, 0o660); err != nil {
		return err
	}
	if s.OnReady != nil {
		s.OnReady()
	}
	server := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    32 * 1024,
	}
	return server.Serve(listener)
}

func withLocalHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if request.Host != "updater.local" && request.Host != "" {
			writeError(w, http.StatusBadRequest, errors.New("invalid updater host"))
			return
		}
		next.ServeHTTP(w, request)
	})
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func DecodeResponse(response *http.Response, target interface{}) error {
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload map[string]string
		if json.Unmarshal(body, &payload) == nil && payload["error"] != "" {
			return errors.New(payload["error"])
		}
		return fmt.Errorf("updater returned HTTP %d", response.StatusCode)
	}
	return json.Unmarshal(body, target)
}
