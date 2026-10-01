package component

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"updater/internal/config"
	"updater/internal/hostrelease"
	"updater/internal/release"
	"updater/internal/releaseauth"
)

const windowBinary = "/usr/local/lib/window/window"
const windowUnit = "/etc/exocortex/units/window.service"
const windowMinimumUpdaterVersion = "0.6.7"

var windowUpdateLock sync.Mutex

type WindowDeploymentError struct {
	Cause          error
	RolledBack     bool
	RollbackFailed bool
}

func (e WindowDeploymentError) Error() string { return e.Cause.Error() }
func (e WindowDeploymentError) Unwrap() error { return e.Cause }

type windowManifest struct {
	Schema                 string `json:"schema"`
	Product                string `json:"product"`
	Version                string `json:"version"`
	Runtime                string `json:"runtime"`
	MinimumUpdater         string `json:"minimum_updater"`
	BinarySHA256           string `json:"binary_sha256"`
	UnitSHA256             string `json:"unit_sha256"`
	UpdaterBootstrapSHA256 string `json:"updater_bootstrap_sha256"`
}

func parseWindowManifest(body []byte, version, platform, updaterVersion string) (windowManifest, error) {
	var value windowManifest
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return value, errors.New("Window manifest has trailing data")
	}
	if value.Schema != "exocortex.window.release.v1" || value.Product != "window-linux" || value.Version != version || value.Runtime != platform ||
		!release.Stable(value.MinimumUpdater) || release.Upgrade(windowMinimumUpdaterVersion, value.MinimumUpdater) || !release.Stable(strings.TrimSuffix(updaterVersion, "-dev")) || release.Upgrade(value.MinimumUpdater, updaterVersion) || len(value.BinarySHA256) != 64 || len(value.UnitSHA256) != 64 || len(value.UpdaterBootstrapSHA256) != 64 {
		return value, errors.New("Window release identity, compatibility or digest is invalid")
	}
	return value, nil
}

func InstallLatestWindow(cfg config.Runtime, updaterVersion string) (string, error) {
	if installed, err := InstalledVersion("window"); err == nil {
		return installed, nil
	}
	if _, err := os.Stat(windowBinary); err == nil {
		return "", errors.New("Installed Window is unhealthy; repair it before installing a new release")
	}
	source, err := hostrelease.Resolve(cfg, "window")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	candidate, err := release.Discover(ctx, source.Repository, "window", "0.0.0")
	if err != nil {
		return "", err
	}
	if candidate.AvailableVersion == "" {
		return "", errors.New("No qualified Window release is available")
	}
	if err := UpdateWindow(cfg, candidate.AvailableVersion, updaterVersion); err != nil {
		return "", err
	}
	return candidate.AvailableVersion, nil
}

func UpdateWindow(cfg config.Runtime, version, updaterVersion string) error {
	if !release.Stable(version) {
		return errors.New("An exact stable Window version is required")
	}
	if current, err := WindowDiskVersion(); err != nil {
		return err
	} else if current != "0.0.0" && !release.Upgrade(version, current) {
		if _, statErr := os.Lstat(windowUnit); version != strings.TrimSuffix(current, "-dev") || !errors.Is(statErr, os.ErrNotExist) {
			return errors.New("Window release must be newer than the installed version")
		}
	}
	if !windowUpdateLock.TryLock() {
		return errors.New("Window update is already running")
	}
	defer windowUpdateLock.Unlock()
	source, err := hostrelease.Resolve(cfg, "window")
	if err != nil {
		return err
	}
	owner, repo, err := githubRepository(source.Repository)
	if err != nil {
		return err
	}
	platform := "linux-" + runtime.GOARCH
	if platform != "linux-amd64" && platform != "linux-arm64" {
		return errors.New("Unsupported Window platform")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}
	selected, err := fetchRelease(ctx, client, owner, repo, "window-v"+version)
	if err != nil {
		return err
	}
	if selected.Draft || selected.Prerelease || selected.TagName != "window-v"+version {
		return errors.New("Window release is not qualified")
	}
	stage, err := os.MkdirTemp(cfg.StateDir, "window-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	manifestName := "window-release-" + platform + ".json"
	manifestPath := filepath.Join(stage, manifestName)
	if err := download(ctx, client, releaseAsset(selected, manifestName), manifestPath, 65536); err != nil {
		return err
	}
	if err := releaseauth.VerifyDownloaded(ctx, client, manifestPath, releaseAsset(selected, manifestName+".sig.json"), "window"); err != nil {
		return err
	}
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	manifest, err := parseWindowManifest(body, version, platform, updaterVersion)
	if err != nil {
		return err
	}
	binaryPath, unitPath := filepath.Join(stage, "window"), filepath.Join(stage, "window.service")
	if err := download(ctx, client, releaseAsset(selected, "window-"+platform), binaryPath, 64*1024*1024); err != nil {
		return err
	}
	if err := download(ctx, client, releaseAsset(selected, "window.service"), unitPath, 65536); err != nil {
		return err
	}
	if err := verifySHA256(binaryPath, manifest.BinarySHA256); err != nil {
		return err
	}
	if err := verifySHA256(unitPath, manifest.UnitSHA256); err != nil {
		return err
	}
	if err := validateWindowUnit(unitPath); err != nil {
		return err
	}
	if cfg.DryRun {
		return nil
	}
	return replaceWindow(ctx, binaryPath, unitPath, version)
}

func WindowDiskVersion() (string, error) {
	if _, err := os.Stat(windowBinary); errors.Is(err, os.ErrNotExist) {
		return "0.0.0", nil
	} else if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, windowBinary, "version").Output()
	if err != nil {
		return "", errors.New("Installed Window binary does not report its version")
	}
	version := strings.TrimSpace(string(output))
	if !release.Stable(strings.TrimSuffix(version, "-dev")) {
		return "", errors.New("Installed Window version is invalid")
	}
	return version, nil
}

func RepairWindow(cfg config.Runtime, updaterVersion string) (string, error) {
	if _, err := os.Lstat(windowBinary + ".previous"); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Lstat(windowUnit); errors.Is(err, os.ErrNotExist) {
			if version, err := WindowDiskVersion(); err == nil && version != "0.0.0" {
				stable := strings.TrimSuffix(version, "-dev")
				if err := UpdateWindow(cfg, stable, updaterVersion); err != nil {
					return "", err
				}
				return stable, nil
			}
		}
	}
	if !windowUpdateLock.TryLock() {
		return "", errors.New("Window update or repair is already running")
	}
	defer windowUpdateLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if version, recovered, err := recoverWindowPrevious(ctx); err != nil {
		return "", err
	} else if recovered {
		return version, nil
	}
	version, err := WindowDiskVersion()
	if err != nil || version == "0.0.0" {
		return "", errors.New("Window is not installed")
	}
	if err := daemonReload(ctx); err != nil {
		return "", err
	}
	if output, err := exec.CommandContext(ctx, "systemctl", "restart", "window.service").CombinedOutput(); err != nil {
		return "", fmt.Errorf("Window repair failed: %s", strings.TrimSpace(string(output)))
	}
	if err := verifyRunningComponent("window", strings.TrimSuffix(version, "-dev")); err != nil {
		return "", err
	}
	return version, nil
}

// A stopped update leaves the prior binary (and sometimes the prior unit) in
// .previous. Repair deliberately restores that known version. The backups are
// retained until it has passed the same health check as a normal deployment.
func recoverWindowPrevious(ctx context.Context) (string, bool, error) {
	oldBinary, oldUnit := windowBinary+".previous", windowUnit+".previous"
	binaryInfo, binaryErr := os.Lstat(oldBinary)
	unitInfo, unitErr := os.Lstat(oldUnit)
	if errors.Is(binaryErr, os.ErrNotExist) && errors.Is(unitErr, os.ErrNotExist) {
		return "", false, nil
	}
	if binaryErr != nil || binaryInfo == nil || !binaryInfo.Mode().IsRegular() || unitErr != nil && !errors.Is(unitErr, os.ErrNotExist) {
		return "", true, errors.New("Window backup is incomplete; inspect its protected deployment directory")
	}
	if unitErr == nil && !unitInfo.Mode().IsRegular() {
		return "", true, errors.New("Window previous unit is not a regular file")
	}
	unitSource := windowUnit
	if unitErr == nil {
		unitSource = oldUnit
	}
	if err := validateWindowUnit(unitSource); err != nil {
		return "", true, fmt.Errorf("Window previous unit is invalid: %w", err)
	}
	output, err := exec.CommandContext(ctx, oldBinary, "version").Output()
	version := strings.TrimSpace(string(output))
	if err != nil || !release.Stable(strings.TrimSuffix(version, "-dev")) {
		return "", true, errors.New("Window previous binary has no valid version")
	}
	if output, err := exec.CommandContext(ctx, "systemctl", "stop", "window.service").CombinedOutput(); err != nil {
		return "", true, fmt.Errorf("Window stop before recovery failed: %s", strings.TrimSpace(string(output)))
	}
	if err := copyFile(oldBinary, windowBinary+".repair", 0o755); err != nil {
		return "", true, err
	}
	defer os.Remove(windowBinary + ".repair")
	if err := os.Rename(windowBinary+".repair", windowBinary); err != nil {
		return "", true, err
	}
	if unitErr == nil {
		if err := copyFile(oldUnit, windowUnit+".repair", 0o644); err != nil {
			return "", true, err
		}
		defer os.Remove(windowUnit + ".repair")
		if err := os.Rename(windowUnit+".repair", windowUnit); err != nil {
			return "", true, err
		}
	}
	if err := daemonReload(ctx); err != nil {
		return "", true, err
	}
	if output, err := exec.CommandContext(ctx, "systemctl", "restart", "window.service").CombinedOutput(); err != nil {
		return "", true, fmt.Errorf("Window recovery restart failed: %s", strings.TrimSpace(string(output)))
	}
	if err := verifyRunningComponent("window", strings.TrimSuffix(version, "-dev")); err != nil {
		return "", true, err
	}
	if err := os.Remove(oldBinary); err != nil {
		return "", true, err
	}
	if unitErr == nil {
		if err := os.Remove(oldUnit); err != nil {
			return "", true, err
		}
	}
	return version, true, nil
}

func validateWindowUnit(path string) error {
	if err := validatePreservedRuntimeUnit(path, "window"); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for _, required := range []string{"ExecStart=/usr/local/bin/window serve", "User=root", "ProtectSystem=strict", "NoNewPrivileges=true", "RestrictAddressFamilies=AF_UNIX"} {
		if !strings.Contains(string(data), required) {
			return fmt.Errorf("Window unit lacks %s", required)
		}
	}
	return nil
}

func replaceWindow(ctx context.Context, binaryPath, unitPath, version string) error {
	if _, err := os.Stat("/var/lib/window-ssh/.ssh"); err != nil {
		return errors.New("Window host identity is not provisioned by the Updater installer")
	}
	link, err := os.Readlink("/etc/systemd/system/window.service")
	if err != nil || link != windowUnit {
		return errors.New("Window unit is not Updater-managed; rerun the verified Updater installer")
	}
	if err := os.MkdirAll(filepath.Dir(windowBinary), 0o755); err != nil {
		return err
	}
	binaryNew, unitNew := windowBinary+".new", windowUnit+".new"
	if err := copyFile(binaryPath, binaryNew, 0o755); err != nil {
		return err
	}
	defer os.Remove(binaryNew)
	if err := copyFile(unitPath, unitNew, 0o644); err != nil {
		return err
	}
	defer os.Remove(unitNew)
	oldBinary, oldUnit := windowBinary+".previous", windowUnit+".previous"
	for _, prior := range []string{oldBinary, oldUnit} {
		if _, err := os.Lstat(prior); err == nil {
			return errors.New("Window previous deployment requires repair before another update")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	_, binaryStatErr := os.Stat(windowBinary)
	_, unitStatErr := os.Stat(windowUnit)
	if (binaryStatErr == nil) != (unitStatErr == nil) {
		if binaryStatErr != nil || !errors.Is(unitStatErr, os.ErrNotExist) {
			return errors.New("Window installation is incomplete; repair it before updating")
		}
		if output, err := exec.CommandContext(ctx, "systemctl", "stop", "window.service").CombinedOutput(); err != nil {
			return fmt.Errorf("Window stop before reinstall failed: %s", strings.TrimSpace(string(output)))
		}
		if err := os.Remove(windowBinary); err != nil {
			return err
		}
	}
	oldBinaryExists := false
	if _, err := os.Stat(windowBinary); err == nil {
		oldBinaryExists = true
		if err := os.Rename(windowBinary, oldBinary); err != nil {
			return err
		}
	}
	oldUnitExists := false
	if _, err := os.Stat(windowUnit); err == nil {
		oldUnitExists = true
		if err := os.Rename(windowUnit, oldUnit); err != nil {
			if oldBinaryExists {
				_ = os.Rename(oldBinary, windowBinary)
			}
			return err
		}
	}
	activated := false
	restore := func(failure error) error {
		rollback, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var rollbackErr error
		if activated {
			rollbackErr = errors.Join(rollbackErr, exec.CommandContext(rollback, "systemctl", "stop", "window.service").Run())
		}
		if err := os.Remove(windowBinary); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErr = errors.Join(rollbackErr, err)
		}
		if err := os.Remove(windowUnit); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErr = errors.Join(rollbackErr, err)
		}
		if oldBinaryExists {
			rollbackErr = errors.Join(rollbackErr, os.Rename(oldBinary, windowBinary))
		}
		if oldUnitExists {
			rollbackErr = errors.Join(rollbackErr, os.Rename(oldUnit, windowUnit))
		}
		rollbackErr = errors.Join(rollbackErr, daemonReload(rollback))
		if oldBinaryExists && oldUnitExists {
			rollbackErr = errors.Join(rollbackErr, exec.CommandContext(rollback, "systemctl", "restart", "window.service").Run())
		}
		return WindowDeploymentError{Cause: errors.Join(failure, rollbackErr), RolledBack: rollbackErr == nil, RollbackFailed: rollbackErr != nil}
	}
	if err := os.Rename(binaryNew, windowBinary); err != nil {
		return restore(err)
	}
	if err := os.Rename(unitNew, windowUnit); err != nil {
		return restore(err)
	}
	if err := daemonReload(ctx); err != nil {
		return restore(err)
	}
	activated = true
	if output, err := exec.CommandContext(ctx, "systemctl", "restart", "window.service").CombinedOutput(); err != nil {
		return restore(fmt.Errorf("Window restart failed: %s", strings.TrimSpace(string(output))))
	}
	var healthErr error
	for attempt := 0; attempt < 30; attempt++ {
		if healthErr = verifyRunningComponent("window", version); healthErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if healthErr != nil {
		return restore(healthErr)
	}
	_ = os.Remove(oldBinary)
	_ = os.Remove(oldUnit)
	return nil
}
