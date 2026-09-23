package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"updater/internal/config"
	"updater/internal/kernel"
	"updater/internal/model"
	"updater/internal/release"
)

var mastermindRequestID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func validateMastermindHead(head config.HeadConfig) error {
	if head.Service != "mastermind" || head.ComposeService != "core" || head.ImageVariable != "MASTERMIND_CORE_IMAGE" ||
		head.VersionVariable != "MASTERMIND_VERSION" || !filepath.IsAbs(head.ProjectDir) {
		return errors.New("head is not registered for the fixed Mastermind component profile")
	}
	return nil
}

func manifestHash(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 2*1024*1024 {
		return "", errors.New("verified manifest is unavailable")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// Prepare downloads and independently verifies the exact release and pre-pulls
// all components before Core acquires the long-lived snapshot/write barrier.
func (e *Engine) PrepareMastermind(ctx context.Context, headID, requestID, version string, rollbackID ...string) (model.Job, error) {
	if e.runtime.DryRun {
		return model.Job{}, errors.New("Mastermind Apply preparation is disabled in dry-run mode")
	}
	if !mastermindRequestID.MatchString(requestID) || version == "" {
		return model.Job{}, errors.New("an exact version and 32-hex request ID are required")
	}
	head, err := config.LoadHead(e.runtime, headID)
	if err != nil {
		return model.Job{}, err
	}
	if err = validateMastermindHead(head); err != nil {
		return model.Job{}, err
	}
	rollbackOf := ""
	if len(rollbackID) > 1 {
		return model.Job{}, errors.New("only one rollback target is allowed")
	}
	if len(rollbackID) == 1 {
		rollbackOf = rollbackID[0]
	}
	var rollbackTarget model.Job
	if rollbackOf != "" {
		var ok bool
		rollbackTarget, ok = e.store.Get(rollbackOf)
		if !ok || rollbackTarget.Service != "mastermind" || rollbackTarget.HeadID != headID || rollbackTarget.State != "COMPLETED" ||
			rollbackTarget.Version != head.CurrentVersion || rollbackTarget.PreviousVersion != version ||
			!rollbackTarget.RollbackAvailable || rollbackTarget.PreviousManifestSHA256 == "" || len(rollbackTarget.PreviousComponents) != 3 {
			return model.Job{}, errors.New("version rollback must name a completed own-head update of the installed version")
		}
	}
	if version == head.CurrentVersion || rollbackOf == "" && !release.SupportsMinimum(version, head.CurrentVersion) {
		return model.Job{}, errors.New("Mastermind preparation requires a newer release")
	}
	if previous, ok := e.store.ByRequestID(requestID + ":prepare"); ok {
		if previous.HeadID != headID || previous.Version != version || previous.RollbackOf != rollbackOf || previous.PreviousVersion != head.CurrentVersion {
			return model.Job{}, errors.New("preparation request is already bound to another release")
		}
		if previous.State != "COMPLETED" || time.Since(previous.UpdatedAt) > time.Hour {
			return model.Job{}, errors.New("preparation failed or expired; use a new request ID")
		}
		return previous, nil
	}
	unlock, err := e.store.BeginOperation("")
	if err != nil {
		return model.Job{}, err
	}
	defer unlock()
	sum := sha256.Sum256([]byte(headID + ":" + requestID))
	now := time.Now().UTC()
	job := model.Job{ID: "prepare-" + hex.EncodeToString(sum[:])[:40], RequestID: requestID + ":prepare", HeadID: headID,
		Service: "mastermind-preparation", Version: version, PreviousVersion: head.CurrentVersion, RollbackOf: rollbackOf, State: "PREPARING", CreatedAt: now, UpdatedAt: now}
	if err = e.store.Save(job); err != nil {
		return model.Job{}, err
	}
	finished := false
	defer func() {
		if !finished {
			end := time.Now().UTC()
			job.State = "FAILED"
			job.Message = "Release preparation did not complete"
			job.UpdatedAt = end
			job.FinishedAt = &end
			_ = e.store.Save(job)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, time.Hour)
	defer cancel()
	snapshot, err := e.loadRegister(head.KernelURL, head.KernelServiceToken, head.KernelCachePath, 5*time.Second)
	if err != nil {
		return model.Job{}, err
	}
	repository, err := kernel.String(snapshot, "repositories.mastermind.url")
	if err != nil {
		return model.Job{}, err
	}
	directory := filepath.Join(e.runtime.StateDir, "staging", job.ID)
	defer os.RemoveAll(directory)
	resolved, err := e.resolveRelease(ctx, repository, "mastermind", version, directory)
	if err != nil {
		return model.Job{}, err
	}
	if err = release.ValidateMastermind(resolved.Manifest); err != nil {
		return model.Job{}, err
	}
	if !release.SupportsMinimum(e.runtime.UpdaterVersion, resolved.Manifest.MinimumUpdaterVersion) {
		return model.Job{}, errors.New("installed Updater is older than the release requirement")
	}
	if _, err = readDeployment(resolved.ComposePath, "mastermind"); err != nil {
		return model.Job{}, err
	}
	job.ManifestSHA256, err = manifestHash(resolved.ManifestPath)
	if err != nil {
		return model.Job{}, err
	}
	job.ComponentImages = resolved.Manifest.Mastermind.Components
	if rollbackOf != "" {
		if job.ManifestSHA256 != rollbackTarget.PreviousManifestSHA256 {
			return model.Job{}, errors.New("previous immutable release metadata changed")
		}
		for _, name := range componentNames {
			if job.ComponentImages[name] != rollbackTarget.PreviousComponents[name] {
				return model.Job{}, errors.New("previous component identity changed")
			}
		}
	}
	for _, name := range []string{"core", "runtime", "worker"} {
		if _, err = e.runner.Run(ctx, "docker", []string{"pull", job.ComponentImages[name]}, nil, head.ProjectDir); err != nil {
			return model.Job{}, errors.New("Mastermind component pre-pull failed: " + name)
		}
		output, inspectErr := e.runner.Run(ctx, "docker", []string{"image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", job.ComponentImages[name]}, nil, head.ProjectDir)
		if inspectErr != nil || strings.TrimSpace(string(output)) != resolved.Manifest.Mastermind.Platform {
			return model.Job{}, errors.New("Mastermind component platform does not match: " + name)
		}
	}
	end := time.Now().UTC()
	job.State = "COMPLETED"
	job.Message = "All three immutable components verified and pre-pulled"
	job.UpdatedAt = end
	job.FinishedAt = &end
	if err = e.store.Save(job); err != nil {
		return model.Job{}, err
	}
	finished = true
	return job, nil
}

func (e *Engine) mastermindPreparation(headID, requestID, version, id string) (model.Job, error) {
	job, ok := e.store.Get(id)
	if !ok || job.Service != "mastermind-preparation" || job.HeadID != headID || job.RequestID != requestID+":prepare" ||
		job.State != "COMPLETED" || job.Version != version || time.Since(job.UpdatedAt) > time.Hour {
		return model.Job{}, errors.New("Mastermind needs a fresh verified preparation for this head, request and version")
	}
	head, err := config.LoadHead(e.runtime, headID)
	if err != nil || job.PreviousVersion != "" && job.PreviousVersion != head.CurrentVersion {
		return model.Job{}, errors.New("installed version changed after preparation")
	}
	return job, nil
}
