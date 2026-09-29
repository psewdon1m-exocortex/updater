package component

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"updater/internal/config"
	"updater/internal/release"
	"updater/internal/releaseauth"
)

// InstallPinnedHelper consumes an exact signed helper bundle carried by an
// authenticated application or helper bootstrap. It does not consult an
// application head, Kernel Register, or any mutable release-source fallback.
func InstallPinnedHelper(runtimeConfig config.Runtime, kind, directory string) (string, error) {
	if kind != "neptune" && kind != "gryphon" {
		return "", errors.New("unsupported pinned helper")
	}
	platform := "linux-" + runtime.GOARCH
	if platform != "linux-amd64" && platform != "linux-arm64" {
		return "", errors.New("unsupported helper platform")
	}
	releaseRuntime := strings.Replace(platform, "amd64", "x64", 1)
	name := kind + "-linux-release-" + releaseRuntime + ".json"
	manifestPath := filepath.Join(directory, name)
	info, err := os.Lstat(manifestPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 2*1024*1024 {
		return "", errors.New("pinned helper manifest is unavailable or unsafe")
	}
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", err
	}
	signature, err := os.ReadFile(manifestPath + ".sig.json")
	if err != nil || len(signature) > 16384 {
		return "", errors.New("pinned helper signature is unavailable")
	}
	key, err := os.ReadFile("/etc/exocortex/release-trust/" + kind + ".pem")
	if err != nil {
		return "", errors.New("pinned helper release trust is unavailable")
	}
	if err := releaseauth.VerifyBytes(body, signature, key); err != nil {
		return "", err
	}
	var identity struct{ Schema, Product, Version, Runtime, Artifact, SHA256 string }
	if err := json.Unmarshal(body, &identity); err != nil {
		return "", err
	}
	if identity.Schema != "exocortex."+kind+".release.v1" || identity.Product != kind+"-linux" || !release.Stable(identity.Version) || identity.Runtime != releaseRuntime ||
		identity.Artifact == "" || identity.Artifact != filepath.Base(identity.Artifact) {
		return "", errors.New("pinned helper manifest identity mismatch")
	}
	archive := filepath.Join(directory, identity.Artifact)
	info, err = os.Lstat(archive)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 256*1024*1024 {
		return "", errors.New("pinned helper archive is unavailable or unsafe")
	}
	if err := verifySHA256(archive, identity.SHA256); err != nil {
		return "", err
	}
	if installed, err := InstalledVersion(kind); err == nil {
		if release.Upgrade(identity.Version, installed) {
			return "", fmt.Errorf("installed %s %s is older than the consumer's pinned %s; update the shared host helper first", kind, installed, identity.Version)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if kind == "neptune" {
			err = checkNeptuneProject(ctx, "", "")
		} else {
			err = gryphonHealth(ctx)
		}
		if err != nil {
			return "", fmt.Errorf("installed %s requires repair: %w", kind, err)
		}
		return installed, nil
	} else if kind == "neptune" && neptuneInstallationComplete() || kind == "gryphon" && gryphonInstallationComplete() {
		return "", fmt.Errorf("installed %s identity requires explicit repair: %w", kind, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	stage, err := os.MkdirTemp(runtimeConfig.StateDir, kind+"-pinned-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if kind == "neptune" {
		upgrade := filepath.Join(stage, "upgrade")
		if err := extractNeptuneUpgradeFiles(archive, upgrade); err != nil {
			return "", err
		}
		if err := validatePreservedRuntimeUnit(filepath.Join(upgrade, "neptune.service"), "neptune"); err != nil {
			return "", err
		}
		if runtimeConfig.DryRun {
			return identity.Version, nil
		}
		err = installFreshNeptune(ctx, archive, stage, config.HeadConfig{})
	} else {
		extracted := filepath.Join(stage, "app")
		if err := extractGryphonApp(archive, extracted, identity.Version); err != nil {
			return "", err
		}
		if err := validatePreservedRuntimeUnit(filepath.Join(extracted, "packaging", "linux", "gryphon.service"), "gryphon"); err != nil {
			return "", err
		}
		if runtimeConfig.DryRun {
			return identity.Version, nil
		}
		err = installFreshGryphon(ctx, extracted)
	}
	if err != nil {
		return "", err
	}
	actual, err := InstalledVersion(kind)
	if err != nil || actual != identity.Version {
		return "", fmt.Errorf("%s installed version differs from pinned manifest", kind)
	}
	return actual, nil
}
