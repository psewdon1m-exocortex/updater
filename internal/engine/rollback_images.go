package engine

import (
	"context"
	"errors"
	"strings"

	"updater/internal/config"
	"updater/internal/model"
)

// ensureImageLocal completes before any live deployment or database mutation.
// A missing legacy image without a signed pull reference fails closed.
func (e *Engine) ensureImageLocal(ctx context.Context, head config.HeadConfig, target, pull string) error {
	if target == "" {
		return nil
	}
	if output, err := e.runner.Run(ctx, "docker", []string{"image", "inspect", "--format", "{{.Id}}", target}, nil, head.ProjectDir); err == nil && localImageIDPattern.MatchString(strings.TrimSpace(string(output))) {
		return nil
	}
	if !immutableImageReference(pull) {
		return errors.New("previous image is absent and has no verified registry digest; current deployment was not changed")
	}
	if _, err := e.runner.Run(ctx, "docker", []string{"pull", pull}, nil, head.ProjectDir); err != nil {
		return errors.New("previous image could not be downloaded; current deployment was not changed")
	}
	output, err := e.runner.Run(ctx, "docker", []string{"image", "inspect", "--format", "{{.Id}}", pull}, nil, head.ProjectDir)
	if err != nil || !localImageIDPattern.MatchString(strings.TrimSpace(string(output))) {
		return errors.New("downloaded rollback image could not be verified; current deployment was not changed")
	}
	// If the old job names a local image ID, a registry pull must restore that
	// exact object. Docker verifies the registry digest during the pull itself.
	if localImageIDPattern.MatchString(target) && strings.TrimSpace(string(output)) != target {
		return errors.New("downloaded rollback image differs from the previous deployment")
	}
	if target != pull {
		if _, err := e.runner.Run(ctx, "docker", []string{"image", "inspect", "--format", "{{.Id}}", target}, nil, head.ProjectDir); err != nil {
			return errors.New("previous image reference is unavailable after download")
		}
	}
	return nil
}

func (e *Engine) ensureRollbackImages(ctx context.Context, job model.Job, head config.HeadConfig) error {
	pull := job.PreviousImagePull
	if pull == "" && immutableImageReference(job.PreviousImage) {
		pull = job.PreviousImage
	}
	if err := e.ensureImageLocal(ctx, head, job.PreviousImage, pull); err != nil {
		return err
	}
	for _, ref := range []string{job.PreviousWebImage, job.RecoveryImage} {
		if ref != "" {
			if err := e.ensureImageLocal(ctx, head, ref, ref); err != nil {
				return err
			}
		}
	}
	return nil
}
