package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"updater/internal/config"
	"updater/internal/kernel"
	"updater/internal/model"
	"updater/internal/release"
	"updater/internal/state"
)

type Runner interface {
	Run(context.Context, string, []string, []string, string) ([]byte, error)
}

type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, name string, args, environment []string, directory string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = directory
	command.Env = append(os.Environ(), environment...)
	return command.CombinedOutput()
}

type Engine struct {
	runtime         config.Runtime
	store           *state.Store
	runner          Runner
	loadRegister    func(string, string, string, time.Duration) (kernel.Snapshot, error)
	resolveRelease  func(context.Context, string, string, string, string, bool) (release.Resolved, error)
	checkHealthFn   func(context.Context, string) error
	restoreBackupFn func(context.Context, config.HeadConfig, string) error
	mu              sync.Mutex
	busy            bool
}

func New(runtime config.Runtime, store *state.Store, runner Runner) *Engine {
	if runner == nil {
		runner = OSRunner{}
	}
	return &Engine{
		runtime:         runtime,
		store:           store,
		runner:          runner,
		loadRegister:    kernel.Load,
		resolveRelease:  release.Resolve,
		checkHealthFn:   checkHealth,
		restoreBackupFn: restoreBackup,
	}
}

func (e *Engine) SetTestHostOperations(
	check func(context.Context, string) error,
	restore func(context.Context, config.HeadConfig, string) error,
) {
	if check != nil {
		e.checkHealthFn = check
	}
	if restore != nil {
		e.restoreBackupFn = restore
	}
}

func (e *Engine) SetTestDependencies(
	loadRegister func(string, string, string, time.Duration) (kernel.Snapshot, error),
	resolveRelease func(context.Context, string, string, string, string, bool) (release.Resolved, error),
) {
	if loadRegister != nil {
		e.loadRegister = loadRegister
	}
	if resolveRelease != nil {
		e.resolveRelease = resolveRelease
	}
}

func (e *Engine) Start(request model.UpdateRequest) (model.Job, error) {
	if request.RequestID == "" || request.HeadID == "" || request.Service == "" {
		return model.Job{}, errors.New("request_id, head_id and service are required")
	}
	if previous, ok := e.store.ByRequestID(request.RequestID); ok {
		return previous, nil
	}
	head, err := config.LoadHead(e.runtime, request.HeadID)
	if err != nil {
		return model.Job{}, err
	}
	if head.Service != request.Service {
		return model.Job{}, fmt.Errorf("head %q is registered for service %q", request.HeadID, head.Service)
	}
	backupBytes, err := decodeBackup(request.Backup)
	if err != nil {
		return model.Job{}, err
	}
	e.mu.Lock()
	if e.busy {
		e.mu.Unlock()
		return model.Job{}, errors.New("another update is already running on this VPS")
	}
	e.busy = true
	e.mu.Unlock()

	now := time.Now().UTC()
	job := model.Job{
		ID:        fmt.Sprintf("%d-%x", now.Unix(), sha256.Sum256([]byte(request.RequestID))),
		RequestID: request.RequestID,
		HeadID:    request.HeadID,
		Service:   request.Service,
		Version:   request.Version,
		State:     "REQUESTED",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if len(job.ID) > 48 {
		job.ID = job.ID[:48]
	}
	backupPath, err := e.store.BackupPath(job.ID, request.Backup.Filename)
	if err != nil {
		e.releaseLock()
		return model.Job{}, err
	}
	if err := os.WriteFile(backupPath, backupBytes, 0o600); err != nil {
		e.releaseLock()
		return model.Job{}, err
	}
	job.BackupPath = backupPath
	job.RollbackAvailable = true
	if err := e.store.Save(job); err != nil {
		e.releaseLock()
		return model.Job{}, err
	}
	go e.run(job, head)
	return job, nil
}

func decodeBackup(backup model.Backup) ([]byte, error) {
	if backup.Filename == "" || backup.SHA256 == "" || backup.DataBase64 == "" {
		return nil, errors.New("a downloaded, checksummed backup is required")
	}
	body, err := base64.StdEncoding.DecodeString(backup.DataBase64)
	if err != nil || len(body) == 0 || len(body) > 128*1024*1024 {
		return nil, errors.New("backup data is invalid or exceeds 128 MB")
	}
	sum := sha256.Sum256(body)
	expected := strings.ToLower(strings.TrimPrefix(backup.SHA256, "sha256:"))
	if hex.EncodeToString(sum[:]) != expected {
		return nil, errors.New("backup checksum mismatch")
	}
	return body, nil
}

func (e *Engine) run(job model.Job, head config.HeadConfig) {
	defer e.releaseLock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(e.runtime.CommandTimeoutSec)*time.Second)
	defer cancel()
	update := func(stateName, message string) {
		job.State = stateName
		job.Message = message
		job.UpdatedAt = time.Now().UTC()
		_ = e.store.Save(job)
	}
	fail := func(err error) {
		update("FAILED", err.Error())
		if job.PreviousImage != "" {
			update("ROLLING_BACK", err.Error())
			if rollbackErr := e.rollback(ctx, &job, head); rollbackErr != nil {
				update("ROLLBACK_FAILED", rollbackErr.Error())
			} else {
				update("ROLLED_BACK", err.Error())
			}
		}
		finished := time.Now().UTC()
		job.FinishedAt = &finished
		_ = e.store.Save(job)
		e.prune()
	}
	update("BACKUP_VERIFIED", "backup stored on the target VPS")
	snapshot, err := e.loadRegister(head.KernelURL, head.KernelServiceToken, head.KernelCachePath, 5*time.Second)
	if err != nil {
		fail(err)
		return
	}
	repositoryURL, err := kernel.String(snapshot, "repositories."+job.Service+".url")
	if err != nil {
		fail(err)
		return
	}
	stagingDir := filepath.Join(e.runtime.StateDir, "staging", job.ID)
	resolved, err := e.resolveRelease(ctx, repositoryURL, job.Service, job.Version, stagingDir, e.runtime.AllowUnsigned)
	if err != nil {
		fail(err)
		return
	}
	if !release.SupportsMinimum(e.runtime.UpdaterVersion, resolved.Manifest.MinimumUpdaterVersion) {
		fail(fmt.Errorf(
			"release requires updater %s or newer; installed version is %s",
			resolved.Manifest.MinimumUpdaterVersion,
			e.runtime.UpdaterVersion,
		))
		return
	}
	job.Version = resolved.Manifest.Version
	update("ARTIFACT_VERIFIED", "release manifest, signature and checksums verified")
	if e.runtime.DryRun {
		update("COMPLETED", "dry-run completed without changing the host")
		finished := time.Now().UTC()
		job.FinishedAt = &finished
		_ = e.store.Save(job)
		e.prune()
		return
	}
	if output, err := e.runner.Run(ctx, "docker", []string{"inspect", "--format", "{{.Config.Image}}", head.ContainerName}, nil, head.ProjectDir); err != nil {
		fail(fmt.Errorf("cannot inspect current image: %s", strings.TrimSpace(string(output))))
		return
	} else {
		job.PreviousImage = strings.TrimSpace(string(output))
		job.PreviousVersion = head.CurrentVersion
		_ = e.store.Save(job)
	}
	nextImage := resolved.Manifest.Image.Reference + "@" + resolved.Manifest.Image.Digest
	update("PULLING", "pulling immutable image")
	if output, err := e.runner.Run(ctx, "docker", []string{"pull", nextImage}, nil, head.ProjectDir); err != nil {
		fail(fmt.Errorf("image pull failed: %s", strings.TrimSpace(string(output))))
		return
	}
	if err := setEnvValues(head.EnvFile, map[string]string{
		head.ImageVariable:   nextImage,
		head.VersionVariable: resolved.Manifest.Version,
	}); err != nil {
		fail(err)
		return
	}
	update("APPLYING", "replacing the target container without deleting volumes")
	if output, err := e.composeUp(ctx, head); err != nil {
		fail(fmt.Errorf("container replacement failed: %s", strings.TrimSpace(string(output))))
		return
	}
	update("HEALTH_CHECK", "checking local and public endpoints")
	if err := e.checkHealthFn(ctx, head.LocalHealthURL); err != nil {
		fail(err)
		return
	}
	if head.PublicHealthURL != "" {
		if err := e.checkHealthFn(ctx, head.PublicHealthURL); err != nil {
			fail(err)
			return
		}
	}
	job.InstalledImage = nextImage
	job.InstalledVersion = resolved.Manifest.Version
	update("COMPLETED", "update completed")
	finished := time.Now().UTC()
	job.FinishedAt = &finished
	_ = e.store.Save(job)
	e.prune()
}

func (e *Engine) composeUp(ctx context.Context, head config.HeadConfig) ([]byte, error) {
	return e.runner.Run(ctx, "docker", []string{
		"compose", "--env-file", head.EnvFile,
		"-f", filepath.Join(head.ProjectDir, head.ComposeFile),
		"up", "-d", "--no-deps", head.ComposeService,
	}, nil, head.ProjectDir)
}

func (e *Engine) rollback(ctx context.Context, job *model.Job, head config.HeadConfig) error {
	if job.PreviousImage == "" {
		return errors.New("previous image is unavailable")
	}
	if job.PreviousVersion == "" {
		return errors.New("previous service version is unavailable")
	}
	if err := setEnvValues(head.EnvFile, map[string]string{
		head.ImageVariable:   job.PreviousImage,
		head.VersionVariable: job.PreviousVersion,
	}); err != nil {
		return err
	}
	if output, err := e.composeUp(ctx, head); err != nil {
		return fmt.Errorf("previous container failed to start: %s", strings.TrimSpace(string(output)))
	}
	if err := e.checkHealthFn(ctx, head.LocalHealthURL); err != nil {
		return err
	}
	if head.RestoreURL != "" {
		if err := e.restoreBackupFn(ctx, head, job.BackupPath); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) Rollback(jobID string) (model.Job, error) {
	job, ok := e.store.Get(jobID)
	if !ok {
		return model.Job{}, errors.New("job not found")
	}
	if job.PreviousImage == "" || !job.RollbackAvailable {
		return model.Job{}, errors.New("rollback is unavailable for this job")
	}
	head, err := config.LoadHead(e.runtime, job.HeadID)
	if err != nil {
		return model.Job{}, err
	}
	e.mu.Lock()
	if e.busy {
		e.mu.Unlock()
		return model.Job{}, errors.New("another update is already running on this VPS")
	}
	e.busy = true
	e.mu.Unlock()
	job.State = "ROLLING_BACK"
	job.UpdatedAt = time.Now().UTC()
	_ = e.store.Save(job)
	go func() {
		defer e.releaseLock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(e.runtime.CommandTimeoutSec)*time.Second)
		defer cancel()
		if err := e.rollback(ctx, &job, head); err != nil {
			job.State, job.Message = "ROLLBACK_FAILED", err.Error()
		} else {
			job.State, job.Message = "ROLLED_BACK", "manual rollback completed"
		}
		now := time.Now().UTC()
		job.UpdatedAt, job.FinishedAt = now, &now
		_ = e.store.Save(job)
		e.prune()
	}()
	return job, nil
}

func (e *Engine) prune() {
	if e.runtime.MaxRetainedJobs <= 0 || e.runtime.RetentionDays <= 0 {
		return
	}
	_ = e.store.Prune(
		e.runtime.MaxRetainedJobs,
		time.Now().UTC().Add(-time.Duration(e.runtime.RetentionDays)*24*time.Hour),
	)
}

func (e *Engine) releaseLock() {
	e.mu.Lock()
	e.busy = false
	e.mu.Unlock()
}

func (e *Engine) Busy() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.busy
}

func setEnvValues(path string, updates map[string]string) error {
	if len(updates) == 0 {
		return errors.New("environment update is empty")
	}
	for key, value := range updates {
		if key == "" || strings.ContainsAny(key, "=\r\n") || strings.ContainsAny(value, "\r\n") {
			return errors.New("environment update is invalid")
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
	replaced := map[string]bool{}
	for index, line := range lines {
		for key, value := range updates {
			if strings.HasPrefix(line, key+"=") {
				lines[index] = key + "=" + value
				replaced[key] = true
				break
			}
		}
	}
	for key, value := range updates {
		if !replaced[key] {
			lines = append(lines, key+"="+value)
		}
	}
	temporary := path + ".updater.tmp"
	if err := os.WriteFile(temporary, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func checkHealth(ctx context.Context, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return errors.New("health URL is invalid")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	for attempt := 0; attempt < 12; attempt++ {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		response, err := client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("health check failed for %s", rawURL)
}

func restoreBackup(ctx context.Context, head config.HeadConfig, backupPath string) error {
	file, err := os.Open(backupPath)
	if err != nil {
		return err
	}
	defer file.Close()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(head.RestoreField, filepath.Base(backupPath))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, head.RestoreURL, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("X-Updater-Token", head.ControlToken)
	response, err := (&http.Client{Timeout: 60 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		content, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("backup restore failed with HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(content)))
	}
	return nil
}
