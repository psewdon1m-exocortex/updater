package engine

import (
	"context"
	"os"
	"time"
	"updater/internal/config"
	"updater/internal/kernel"
	"updater/internal/release"
)

func (e *Engine) Check(headID string) (map[string]any, error) {
	head, err := config.LoadHead(e.runtime, headID)
	if err != nil {
		return nil, err
	}
	snapshot, err := e.loadRegister(head.KernelURL, head.KernelServiceToken, head.KernelCachePath, 5*time.Second)
	if err != nil {
		return nil, err
	}
	repository, err := kernel.String(snapshot, "repositories."+head.Service+".url")
	if err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(e.runtime.StateDir, "release-check-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	resolved, err := e.resolveRelease(ctx, repository, head.Service, "", staging)
	if err != nil {
		return nil, err
	}
	candidate := resolved.Manifest.Version
	result := map[string]any{"installed_version": head.CurrentVersion, "available_version": candidate, "update_available": candidate != head.CurrentVersion && release.SupportsMinimum(candidate, head.CurrentVersion)}
	if head.Service == "mastermind" {
		result["profile"] = "mastermind"
		result["components"] = []string{"core", "runtime", "worker"}
		compatible := validateMastermindHead(head) == nil && release.ValidateMastermind(resolved.Manifest) == nil &&
			release.SupportsMinimum(e.runtime.UpdaterVersion, resolved.Manifest.MinimumUpdaterVersion)
		if compatible {
			_, deploymentErr := readDeployment(resolved.ComposePath, "mastermind")
			compatible = deploymentErr == nil
		}
		result["compatible"] = compatible
	}
	return result, nil
}
