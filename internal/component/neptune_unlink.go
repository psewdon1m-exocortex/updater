package component

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"updater/internal/config"
	"updater/internal/model"
	"updater/internal/state"
)

type NeptuneUnlinkRequest struct {
	RequestID string `json:"request_id"`
	HeadID    string `json:"head_id"`
	ProjectID string `json:"project_id"`
}

// Unlink is service-scoped: the shared daemon and other project registrations
// are left in place. The durable job can be retried after an outage.
func StartNeptuneUnlink(runtimeConfig config.Runtime, store *state.Store, request NeptuneUnlinkRequest) (model.Job, error) {
	if request.RequestID == "" || request.HeadID == "" || !safeNeptuneID.MatchString(request.ProjectID) {
		return model.Job{}, errors.New("request_id, head_id and valid project_id are required")
	}
	if previous, ok := store.ByRequestID(request.RequestID); ok {
		if previous.HeadID != request.HeadID || previous.Service != "neptune-unlink" {
			return model.Job{}, errors.New("request id is already in use")
		}
		return previous, nil
	}
	head, err := config.LoadHead(runtimeConfig, request.HeadID)
	if err != nil {
		return model.Job{}, err
	}
	if head.Service != request.ProjectID {
		return model.Job{}, errors.New("Neptune project must match the registered service")
	}
	releaseOperation, err := store.BeginOperation("")
	if err != nil {
		return model.Job{}, err
	}
	if !neptuneInitializationLock.TryLock() {
		releaseOperation()
		return model.Job{}, errors.New("another Neptune lifecycle operation is already running")
	}
	now := time.Now().UTC()
	digest := sha256.Sum256([]byte(request.RequestID))
	job := model.Job{ID: fmt.Sprintf("neptune-unlink-%d-%x", now.Unix(), digest[:8]),
		RequestID: request.RequestID, HeadID: request.HeadID, Service: "neptune-unlink",
		State: "REQUESTED", Message: "Neptune unlink accepted", CreatedAt: now, UpdatedAt: now}
	if err := store.Save(job); err != nil {
		neptuneInitializationLock.Unlock()
		releaseOperation()
		return model.Job{}, err
	}
	go func(job model.Job) {
		defer releaseOperation()
		defer neptuneInitializationLock.Unlock()
		update := func(stateName, message string, finished bool) {
			job.State, job.Message, job.UpdatedAt = stateName, message, time.Now().UTC()
			if finished {
				value := job.UpdatedAt
				job.FinishedAt = &value
			}
			_ = store.Save(job)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		update("DRAINING", "Stopping new backups and waiting for accepted transfers", false)
		if err := unlinkNeptuneProject(ctx, request.ProjectID, head.ProjectDir, func(message string) { update("REVOKING", message, false) }); err != nil {
			update("FAILED", err.Error(), true)
			return
		}
		update("COMPLETED", "Service unlinked; Neptune remains installed for other services", true)
	}(job)
	return job, nil
}

var errNeptuneProjectMissing = errors.New("Neptune project is not registered")

func unlinkNeptuneProject(ctx context.Context, projectID, projectDir string, progress func(string)) error {
	controlPath := filepath.Join("/etc/neptune/clients", projectID+".control.token")
	controlBytes, err := os.ReadFile(controlPath)
	if err != nil {
		return fmt.Errorf("Neptune project control credential is unavailable: %w", err)
	}
	token := strings.TrimSpace(string(controlBytes))
	if token == "" {
		return errors.New("Neptune project control credential is empty")
	}
	path := "/v1/projects/" + url.PathEscape(projectID)
	if _, err := neptuneUnlinkRequest(ctx, http.MethodPost, path+"/unlink/prepare", token); err != nil {
		if errors.Is(err, errNeptuneProjectMissing) {
			return invalidateNeptuneLocalCredentials(projectID, projectDir)
		}
		return err
	}
	for {
		body, err := neptuneUnlinkRequest(ctx, http.MethodGet, path+"/status", token)
		if err != nil {
			return err
		}
		var status struct {
			Active       bool `json:"active"`
			MirrorActive bool `json:"mirror_active"`
		}
		if err := json.Unmarshal(body, &status); err != nil {
			return err
		}
		if !status.Active && !status.MirrorActive {
			break
		}
		select {
		case <-ctx.Done():
			return errors.New("An accepted backup or mirror is still running; the project remains paused. Retry unlink after it finishes")
		case <-time.After(2 * time.Second):
		}
	}
	progress("Revoking scoped Saturn credentials and removing the Neptune registration")
	if _, err := neptuneUnlinkRequest(ctx, http.MethodPost, path+"/unlink/finish", token); err != nil && !errors.Is(err, errNeptuneProjectMissing) {
		return err
	}
	return invalidateNeptuneLocalCredentials(projectID, projectDir)
}

func neptuneUnlinkRequest(ctx context.Context, method, path, token string) ([]byte, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", neptuneSocket)
	}}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, method, "http://neptune.local"+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("X-Neptune-Token", token)
	response, err := (&http.Client{Transport: transport, Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return nil, errors.New("Neptune unlink request failed; retry when the agent is reachable")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8192))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusNotFound {
			return nil, errNeptuneProjectMissing
		}
		return nil, fmt.Errorf("Neptune unlink returned HTTP %d; project remains paused for retry", response.StatusCode)
	}
	return body, nil
}

func invalidateNeptuneLocalCredentials(projectID, projectDir string) error {
	group, err := user.LookupGroup("neptune-clients")
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return err
	}
	clientsDir := "/etc/neptune/clients"
	for _, role := range []string{"control", "export", "saturn", "mirror", "reader"} {
		path := filepath.Join(clientsDir, projectID+"."+role+".token")
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		invalid, err := randomToken()
		if err != nil {
			return err
		}
		if err := writeSecret(path, invalid, gid); err != nil {
			return err
		}
	}
	if projectID == "mastermind" {
		// Mastermind mounts copies under its own component directory.
		for _, role := range []string{"control", "export"} {
			path := filepath.Join(projectDir, "secrets", "core", "neptune_"+role+"_token")
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return err
			}
			invalid, err := randomToken()
			if err != nil {
				return err
			}
			if err := writeSecret(path, invalid, 10001); err != nil {
				return err
			}
		}
	}
	err = os.Remove(filepath.Join("/etc/neptune/projects", projectID+".env"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
