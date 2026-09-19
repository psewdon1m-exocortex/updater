package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"updater/internal/component"
	"updater/internal/config"
	"updater/internal/console"
	"updater/internal/model"
)

var operatorID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var operatorRequestID = regexp.MustCompile(`^[a-zA-Z0-9-]{16,128}$`)

// This handler is installed ONLY on the separate root-only listener. The
// service-mounted listener never exposes any of these operator routes.
func (s Server) operatorHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/overview", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		writeJSON(w, 200, s.operatorSnapshot(ctx))
	})
	mux.HandleFunc("GET /v1/bots", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		var result struct {
			Schema string        `json:"schema"`
			Bots   []console.Bot `json:"bots"`
		}
		if err := console.LocalJSON(ctx, "/run/gryphon-admin/admin.sock", "/v1/bots", &result); err != nil || result.Schema != "exocortex.gryphon.bots.v1" {
			writeError(w, 503, errors.New("Gryphon is unavailable. Check its status and configuration"))
			return
		}
		if len(result.Bots) > 100 {
			result.Bots = result.Bots[:100]
		}
		for i := range result.Bots {
			b := &result.Bots[i]
			b.Alias = console.Text(b.Alias)
			b.Username = console.Text(b.Username)
			b.State = console.Text(b.State)
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("POST /v1/check", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Component string `json:"component"`
			HeadID    string `json:"head_id"`
		}
		if !operatorDecode(w, r, &input) {
			return
		}
		if _, err := s.operatorHead(input.HeadID, input.Component); err != nil {
			writeError(w, 400, err)
			return
		}
		candidate, err := s.candidate(input.HeadID, input.Component)
		if err != nil {
			writeError(w, 502, errors.New("Release check failed. Verify the running component, Kernel, Volt and release configuration"))
			return
		}
		writeJSON(w, 200, console.Candidate{Component: input.Component, HeadID: input.HeadID, Installed: candidate.InstalledVersion, Available: candidate.AvailableVersion, UpdateAvailable: candidate.UpdateAvailable})
	})
	mux.HandleFunc("POST /v1/actions", s.operatorAction)
	return withLocalHeaders(mux)
}

func operatorDecode(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, 400, errors.New("Invalid operator request"))
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, 400, errors.New("Expected one JSON request"))
		return false
	}
	return true
}

func (s Server) operatorHead(id, kind string) (config.HeadConfig, error) {
	if !operatorID.MatchString(id) || (kind != "updater" && kind != "neptune" && kind != "gryphon") {
		return config.HeadConfig{}, errors.New("Unknown component or registered service")
	}
	head, err := config.LoadHead(s.Runtime, id)
	if err != nil {
		return head, errors.New("Registered service configuration is incomplete; inspect the host configuration")
	}
	if kind != "updater" && !component.ConsumesHelper(head.Service, kind) {
		return head, errors.New("The selected service does not consume this helper")
	}
	return head, nil
}

func (s Server) operatorSnapshot(ctx context.Context) console.Snapshot {
	host, _ := os.Hostname()
	result := console.Snapshot{Protocol: console.Protocol, Host: console.Text(host), ObservedAt: time.Now().UTC(), Components: console.LocalComponents(ctx, s.Version), Heads: []console.Head{}, Jobs: []console.Job{}}
	registry, err := config.LoadRegistry(s.Runtime.RegistryPath)
	if err != nil {
		result.Notice = "Cannot read the registered service list; check host configuration"
	}
	ids := make([]string, 0, len(registry.Heads))
	for id := range registry.Heads {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > 100 {
		ids = ids[:100]
		result.Notice = "Showing the first 100 registered services"
	}
	for _, id := range ids {
		entry := console.Head{ID: console.Text(id), Helpers: []string{}}
		head, err := config.LoadHead(s.Runtime, id)
		if err != nil || !operatorID.MatchString(id) {
			entry.Problem = "Incomplete configuration"
		} else {
			entry.Service = console.Text(head.Service)
			for _, helper := range []string{"neptune", "gryphon"} {
				if component.ConsumesHelper(head.Service, helper) {
					entry.Helpers = append(entry.Helpers, helper)
				}
			}
			// The configured loopback health origin plus the service's established
			// export route is a suggestion. The operator can correct it in the form.
			if u, err := url.Parse(head.LocalHealthURL); err == nil && u.Scheme == "http" && u.User == nil {
				u.RawQuery, u.Fragment, u.RawPath = "", "", ""
				u.Path = "/api/internal/neptune/backup"
				if head.Service == "saturn" {
					u.Path = "/api/v1/internal/neptune/backup"
				}
				entry.ExportURL = u.String()
			}
		}
		result.Heads = append(result.Heads, entry)
	}
	for _, listed := range s.Store.List() {
		if len(result.Jobs) >= 100 {
			break
		}
		if kind := jobComponent(listed.Service); kind == "" {
			continue
		}
		job, ok := s.Store.Get(listed.ID)
		if ok {
			result.Jobs = append(result.Jobs, operatorJob(job))
		}
	}
	return result
}

func jobComponent(service string) string {
	for _, kind := range []string{"updater", "neptune", "gryphon"} {
		if service == kind || strings.HasPrefix(service, kind+"-") {
			return kind
		}
	}
	return ""
}

func operatorJob(job model.Job) console.Job {
	summary := "Operation in progress"
	switch job.State {
	case "REQUESTED":
		summary = "Accepted by Updater"
	case "INSTALLING":
		summary = "Verifying release and installing"
	case "ENROLLING":
		summary = "Linking the service to Saturn"
	case "HEALTH_CHECK":
		summary = "Checking the running version"
	case "COMPLETED":
		summary = "Operation completed and verified"
	case "FAILED":
		summary = "Operation failed; check component diagnostics and configuration"
	case "ROLLED_BACK":
		summary = "Previous version restored"
	case "ROLLBACK_FAILED":
		summary = "Rollback failed; operator repair required"
	}
	// Raw job.Message may contain third-party stderr. Only typed metadata and a
	// controlled summary cross this boundary, never copied credentials or paths.
	return console.Job{ID: console.Text(job.ID), RequestID: console.Text(job.RequestID), HeadID: console.Text(job.HeadID), Component: jobComponent(job.Service), State: console.Text(job.State), Version: console.Text(job.Version), Summary: summary, UpdatedAt: job.UpdatedAt, Finished: job.FinishedAt != nil}
}

func (s Server) operatorAction(w http.ResponseWriter, r *http.Request) {
	var action console.Action
	if !operatorDecode(w, r, &action) {
		return
	}
	head, err := s.operatorHead(action.HeadID, action.Component)
	if err != nil {
		writeError(w, 400, err)
		return
	}
	if !operatorRequestID.MatchString(action.RequestID) {
		writeError(w, 400, errors.New("A valid operation request ID is required"))
		return
	}
	if err := console.ValidateAction(action); err != nil {
		writeError(w, 400, err)
		return
	}
	path := ""
	var payload any
	switch action.Kind {
	case "update":
		path = "/v2/components/" + action.Component + "/updates"
		payload = map[string]string{"head_id": head.ID, "version": action.Version, "request_id": action.RequestID}
	case "install":
		path = "/v1/lifecycle/" + action.Component + "-installation"
		if action.Component == "gryphon" {
			path = "/v1/lifecycle/gryphon-initialization"
		}
		payload = lifecycleRequest{HeadID: head.ID, RequestID: action.RequestID}
	case "enroll":
		path = "/v1/components/neptune-linux/initialize"
		payload = model.NeptuneInitializationRequest{RequestID: action.RequestID, HeadID: head.ID, ProjectID: head.Service, ExportURL: action.ExportURL, EnrollmentCode: action.SetupCode}
	case "connect-bot":
		path = "/v1/lifecycle/gryphon-bot"
		payload = lifecycleRequest{HeadID: head.ID, RequestID: action.RequestID, Alias: action.Alias, BotToken: action.BotToken}
	}
	if path == "" {
		writeError(w, 400, errors.New("Unsupported action or invalid action fields"))
		return
	}
	// Re-enter only a fixed, typed existing route, using the daemon-owned head
	// credential. No client-supplied path, header or executable is forwarded.
	body, _ := json.Marshal(payload)
	request, _ := http.NewRequestWithContext(r.Context(), "POST", "http://updater.local"+path, bytes.NewReader(body))
	request.Header.Set("X-Updater-Token", head.ControlToken)
	request.Header.Set("Content-Type", "application/json")
	response := &operatorResponse{header: http.Header{}}
	s.Handler().ServeHTTP(response, request)
	clear(body)
	if response.status != 200 && response.status != 202 {
		message := "Operation rejected. Verify the input and service configuration"
		if response.status == 409 {
			message = "Operation conflicts with an existing job or changed release; refresh jobs and check again"
		}
		if response.status >= 500 {
			message = "Operation could not start. Check component diagnostics, Kernel and release trust"
		}
		writeError(w, response.status, errors.New(message))
		return
	}
	var job model.Job
	if json.Unmarshal(response.body.Bytes(), &job) != nil || job.ID == "" {
		writeError(w, 502, errors.New("Invalid operation receipt; inspect job history before retrying"))
		return
	}
	writeJSON(w, response.status, operatorJob(job))
}

type operatorResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (r *operatorResponse) Header() http.Header { return r.header }
func (r *operatorResponse) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}
func (r *operatorResponse) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = 200
	}
	if r.body.Len()+len(data) > 128*1024 {
		return 0, errors.New("response too large")
	}
	return r.body.Write(data)
}
