package component

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"updater/internal/config"
)

// InitializeGryphon reuses a healthy per-host installation and preserves bindings.
func InitializeGryphon(runtime config.Runtime, headID string) (string, error) {
	var head config.HeadConfig
	var err error
	if headID != "" {
		head, err = config.LoadHead(runtime, headID)
		if err != nil {
			return "", err
		}
		if !ConsumesHelper(head.Service, "gryphon") {
			return "", errors.New("this head does not consume Gryphon")
		}
	}
	if gryphonInstallationComplete() {
		ctx, cancel := context.WithTimeout(context.Background(), 30_000_000_000)
		defer cancel()
		// Do not restart a healthy daemon when attaching another head.
		if err := gryphonHealth(ctx); err != nil {
			if err = restartGryphon(ctx); err != nil {
				return "", err
			}
		}
		version, err := installedGryphonVersion()
		if err == nil && head.Service == "mastermind" && !runtime.DryRun {
			err = enrollMastermindGryphon()
		}
		return version, err
	}
	check, err := CheckGryphon(runtime, headID, "0.0.0")
	if err != nil {
		return "", err
	}
	if !check.UpdateAvailable {
		return "", errors.New("no trusted Gryphon release is available")
	}
	if err := UpdateGryphon(runtime, headID, check.AvailableVersion); err != nil {
		return "", err
	}
	if head.Service == "mastermind" && !runtime.DryRun {
		if err := enrollMastermindGryphon(); err != nil {
			return "", err
		}
	}
	return check.AvailableVersion, nil
}

func installFreshGryphon(ctx context.Context, extracted string) error {
	for _, name := range []string{"install.sh", "gryphon.service", "gryphonctl"} {
		if info, err := os.Stat(filepath.Join(extracted, "packaging", "linux", name)); err != nil || info.IsDir() {
			return errors.New("Gryphon release lacks installation files")
		}
	}
	if err := os.MkdirAll("/etc/gryphon", 0750); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "/bin/sh", "packaging/linux/install.sh")
	command.Dir = extracted
	if err := command.Run(); err != nil {
		return errors.New("Gryphon installation failed; inspect the local installation journal")
	}
	return restartGryphon(ctx)
}
