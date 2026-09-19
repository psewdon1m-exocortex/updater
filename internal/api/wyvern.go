package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"time"
	"updater/internal/component"
	"updater/internal/console"
	"updater/internal/model"
)

func (s Server) wyvernSocket() string {
	if s.WyvernSocket != "" {
		return s.WyvernSocket
	}
	return console.WyvernAdminSocket
}

func (s Server) wyvernManager() component.WyvernManager {
	if s.WyvernManager != nil {
		return *s.WyvernManager
	}
	return component.WyvernManager{}
}

func (s Server) manageWyvern(ctx context.Context, action console.Action) error {
	m := s.wyvernManager()
	i := action.Wyvern
	if action.Kind == "connect-kernel" {
		instance := i.InstanceID
		if instance == "" {
			id, err := os.ReadFile("/etc/machine-id")
			if err != nil {
				return errors.New("Host identity unavailable; provide an explicit instance ID")
			}
			digest := sha256.Sum256(id)
			instance = "host-" + hex.EncodeToString(digest[:12])
		}
		return m.Connect(ctx, i.KernelURL, i.AccessKey, instance)
	}
	config, err := m.Configuration(ctx)
	if err != nil {
		return err
	}
	if config.Revision != i.Revision {
		return errors.New("Wyvern configuration changed; refresh the form")
	}
	input := map[string]any{"expected_revision": i.Revision, "request_id": action.RequestID}
	switch action.Kind {
	case "adapter-put", "profile-put", "adapter-disable", "adapter-enable":
		adapter, exists := config.Config.Adapters[i.AdapterID]
		if !exists && action.Kind != "adapter-put" {
			return errors.New("Adapter does not exist")
		}
		if !exists {
			adapter = component.WyvernAdapter{Driver: "google", Enabled: true, Profiles: map[string]component.WyvernProfile{}}
		}
		adapter.CredentialRef = ""
		if action.Kind == "adapter-put" {
			adapter.Name = i.Name
			if i.APIKey != "" {
				input["credential"] = i.APIKey
			}
		}
		if action.Kind == "adapter-disable" {
			adapter.Enabled = false
		} else if action.Kind == "adapter-enable" {
			adapter.Enabled = true
		} else {
			profile := i.Profile
			if profile == "" {
				profile = "default"
			}
			previous := adapter.Profiles[profile]
			previous.Model, previous.MaxOutputTokens, previous.Capabilities = i.Model, i.MaxOutput, i.Capabilities
			adapter.Profiles[profile] = previous
		}
		input["operation"], input["adapter_id"], input["adapter"] = "adapter.put", i.AdapterID, adapter
	case "adapter-delete":
		input["operation"], input["adapter_id"] = "adapter.delete", i.AdapterID
	case "client-grant", "client-revoke":
		client, exists := config.Config.Clients[i.ClientID]
		if !exists {
			return errors.New("Client is not registered")
		}
		if action.Kind == "client-revoke" {
			client.Enabled = false
		} else {
			client.Enabled = true
			client.Allowed = i.AllowedAdapters
			if client.Allowed == nil {
				client.Allowed = []string{}
			}
			// Removing a grant deliberately unbinds only that client's affected
			// functions, never deletes the shared Adapter.
			for function, binding := range client.Bindings {
				allowed := false
				for _, id := range client.Allowed {
					if id == binding.AdapterID {
						allowed = true
					}
				}
				if !allowed {
					delete(client.Bindings, function)
				}
			}
		}
		input["operation"], input["client_id"], input["client"] = "client.put", i.ClientID, client
	default:
		return errors.New("Unsupported Wyvern management action")
	}
	_, err = m.Mutate(ctx, input)
	if err != nil {
		return err
	}
	// Publication is durable even if the runtime is temporarily offline.
	_, _ = console.ControlWyvern(ctx, s.wyvernSocket(), "reload")
	return nil
}

// Called exclusively by the root operator handler. Services cannot stop the
// shared gateway. Jobs contain only a typed action and a controlled result.
func (s Server) wyvernAction(w http.ResponseWriter, action console.Action) {
	lifecycleStart.Lock()
	defer lifecycleStart.Unlock()
	kind := "wyvern-" + action.Kind
	if previous, exists := s.Store.ByRequestID(action.RequestID); exists {
		if previous.Service != kind || previous.HeadID != "" {
			writeError(w, 409, errors.New("Request ID is already in use"))
			return
		}
		latest, _ := s.Store.Get(previous.ID)
		writeJSON(w, 200, operatorJob(latest))
		return
	}
	release, err := s.Store.BeginOperation("")
	if err != nil {
		writeError(w, 409, errors.New("A host operation is already running"))
		return
	}
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		release()
		writeError(w, 500, errors.New("Cannot allocate operation ID"))
		return
	}
	now := time.Now().UTC()
	job := model.Job{ID: "wyvern-" + hex.EncodeToString(bytes), RequestID: action.RequestID, Service: kind, State: "REQUESTED", CreatedAt: now, UpdatedAt: now}
	if err := s.Store.Save(job); err != nil {
		release()
		writeError(w, 500, errors.New("Cannot record operation"))
		return
	}
	go func(job model.Job) {
		defer release()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		var status console.WyvernStatus
		var err error
		if action.Kind == "repair" {
			err = (component.WyvernDeployment{}).Repair(ctx)
		} else if action.Wyvern != nil {
			err = s.manageWyvern(ctx, action)
		} else {
			status, err = console.ControlWyvern(ctx, s.wyvernSocket(), action.Kind)
		}
		if action.Wyvern != nil {
			action.Wyvern.AccessKey, action.Wyvern.APIKey = "", ""
		}
		job.UpdatedAt = time.Now().UTC()
		job.FinishedAt = &job.UpdatedAt
		if err != nil {
			job.State, job.Message = "FAILED", console.Text(err.Error())
		} else {
			job.State, job.Version, job.Message = "COMPLETED", status.Version, "Wyvern operation verified"
		}
		_ = s.Store.Save(job)
		if s.Runtime.MaxRetainedJobs > 0 && s.Runtime.RetentionDays > 0 {
			_ = s.Store.Prune(s.Runtime.MaxRetainedJobs, time.Now().Add(-time.Duration(s.Runtime.RetentionDays)*24*time.Hour))
		}
	}(job)
	writeJSON(w, http.StatusAccepted, operatorJob(job))
}
