package component

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"updater/internal/config"
)

// InitializeGryphon reuses a healthy per-host installation and preserves bindings.
func InitializeGryphon(runtime config.Runtime, headID string) (string, error) {
	head, err := config.LoadHead(runtime, headID)
	if err != nil {
		return "", err
	}
	if head.Service != "saturn" {
		return "", errors.New("this head does not consume Gryphon")
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
		return installedGryphonVersion()
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
	return check.AvailableVersion, nil
}

func installFreshGryphon(ctx context.Context, extracted string, head config.HeadConfig) error {
	for _, name := range []string{"install.sh", "gryphon.service", "gryphonctl"} {
		if info, err := os.Stat(filepath.Join(extracted, "packaging", "linux", name)); err != nil || info.IsDir() {
			return errors.New("Gryphon release lacks installation files")
		}
	}
	if err := os.MkdirAll("/etc/gryphon", 0750); err != nil {
		return err
	}
	group, err := user.LookupGroup("gryphon-clients")
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return err
	}
	tokenPath := "/etc/gryphon/kernel.token"
	if err := os.WriteFile(tokenPath, []byte(head.KernelServiceToken+"\n"), 0640); err != nil {
		return err
	}
	if err := os.Chown(tokenPath, 0, gid); err != nil {
		return err
	}
	if strings.ContainsAny(head.KernelURL, "\r\n\"'") {
		return errors.New("invalid Kernel bootstrap URL")
	}
	environment := "GRYPHON_KERNEL_URL=" + head.KernelURL + "\nGRYPHON_KERNEL_TOKEN_FILE=" + tokenPath + "\n"
	if err := os.WriteFile("/etc/gryphon/gryphon.env", []byte(environment), 0640); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "/bin/sh", "packaging/linux/install.sh")
	command.Dir = extracted
	if err := command.Run(); err != nil {
		return errors.New("Gryphon installation failed; inspect the local installation journal")
	}
	return restartGryphon(ctx)
}
