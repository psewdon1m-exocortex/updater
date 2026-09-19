package component

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"
	"updater/internal/config"
	"updater/internal/kernel"
	"updater/internal/release"
	"updater/internal/releaseauth"
)

// Wyvern is a shared runtime whose authoritative state is in Kernel/Volt.
// Its typed deployment transaction preserves only runtime and unit identity;
// it never accepts a caller-supplied backup exemption or restores Register.
func wyvernRepository(runtime config.Runtime, headID string) (string, error) {
	head, err := config.LoadHead(runtime, headID)
	if err != nil {
		return "", err
	}
	if !ConsumesHelper(head.Service, "wyvern") {
		return "", errors.New("Head does not consume Wyvern")
	}
	snapshot, err := kernel.Load(head.KernelURL, head.KernelServiceToken, head.KernelCachePath, 5*time.Second)
	if err != nil {
		return "", err
	}
	return kernel.String(snapshot, "repositories.wyvern.url")
}

func UpdateWyvern(runtime config.Runtime, headID, version string) error {
	if !release.Stable(version) {
		return errors.New("An exact stable Wyvern version is required")
	}
	repository, err := wyvernRepository(runtime, headID)
	if err != nil {
		return err
	}
	owner, repo, err := githubRepository(repository)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}
	selected, err := fetchRelease(ctx, client, owner, repo, "wyvern-v"+version)
	if err != nil {
		return err
	}
	if selected.Draft || selected.Prerelease || selected.TagName != "wyvern-v"+version {
		return errors.New("Wyvern release is not qualified")
	}
	staging, err := os.MkdirTemp("", "updater-wyvern-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	path := filepath.Join(staging, "wyvern-release.json")
	if err := download(ctx, client, releaseAsset(selected, "wyvern-release.json"), path, 65536); err != nil {
		return err
	}
	if err := releaseauth.VerifyDownloaded(ctx, client, path, releaseAsset(selected, "wyvern-release.json.sig.json"), "wyvern"); err != nil {
		return err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	manifest, err := ParseWyvernManifest(body)
	if err != nil {
		return err
	}
	if manifest.Version != version {
		return errors.New("Signed Wyvern manifest version differs from selected release")
	}
	if runtime.DryRun {
		return nil
	}
	return (WyvernDeployment{}).Apply(ctx, manifest)
}

// Bootstrap inputs are authenticated by the pre-pinned host trust key, never
// by a public key delivered next to the candidate manifest.
func InstallWyvernManifest(ctx context.Context, path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	signature, err := os.ReadFile(path + ".sig.json")
	if err != nil {
		return err
	}
	key, err := os.ReadFile("/etc/exocortex/release-trust/wyvern.pem")
	if err != nil {
		return errors.New("Wyvern release trust is not provisioned")
	}
	if err := releaseauth.VerifyBytes(body, signature, key); err != nil {
		return err
	}
	manifest, err := ParseWyvernManifest(body)
	if err != nil {
		return err
	}
	// A consumer installation reuses the shared runtime, regardless of the
	// version carried in its bootstrap. Updates are explicit host operations.
	if current, err := (WyvernDeployment{}).installed(); err != nil {
		return err
	} else if current != nil {
		running, err := InstalledVersion("wyvern")
		if err != nil || running != current.Version {
			return errors.New("Installed Wyvern requires repair")
		}
		return nil
	}
	return (WyvernDeployment{}).Apply(ctx, manifest)
}

func EnsureWyvern(runtime config.Runtime, headID, requestID string) (string, error) {
	head, err := config.LoadHead(runtime, headID)
	if err != nil {
		return "", err
	}
	if !ConsumesHelper(head.Service, "wyvern") {
		return "", errors.New("Head does not consume Wyvern")
	}
	// An explicitly configured remote link must never fall back to a local host.
	if link, err := ReadWyvernLink(headID); err == nil && link.Mode == "remote" {
		return "remote", nil
	}
	manifest, err := (WyvernDeployment{}).installed()
	if err != nil {
		return "", err
	}
	version := ""
	if manifest != nil {
		version, err = InstalledVersion("wyvern")
		if err != nil || version != manifest.Version {
			return "", errors.New("Installed Wyvern requires repair")
		}
	} else {
		repository, err := wyvernRepository(runtime, headID)
		if err != nil {
			return "", err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		candidate, err := release.Discover(ctx, repository, "wyvern", "0.0.0")
		cancel()
		if err != nil {
			return "", err
		}
		version = candidate.AvailableVersion
		if version == "" {
			return "", errors.New("No qualified Wyvern release is available")
		}
		if err := UpdateWyvern(runtime, headID, version); err != nil {
			return "", err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := (WyvernManager{}).EnsureClient(ctx, headID, head.Service, requestID); err != nil {
		return version, err
	}
	return version, nil
}

func ReadWyvernLink(headID string) (WyvernLink, error) { return (WyvernManager{}).ReadLink(headID) }

// Consumer bootstrap runs under the same host lock as all deployment jobs.
// A remote link is explicit and must be reachable; it never installs a fallback.
func BootstrapWyvern(ctx context.Context, runtime config.Runtime, headID, manifest string) error {
	head, err := config.LoadHead(runtime, headID)
	if err != nil {
		return err
	}
	if !ConsumesHelper(head.Service, "wyvern") {
		return errors.New("Head does not consume Wyvern")
	}
	m := WyvernManager{}
	link, linkErr := m.ReadLink(headID)
	if linkErr == nil && link.Mode == "remote" {
		return m.ImportLink(ctx, headID, link)
	}
	if linkErr != nil && !os.IsNotExist(linkErr) {
		return linkErr
	}
	if err := os.MkdirAll(filepath.Dir(WyvernLinkPath(headID)), 0750); err != nil {
		return err
	}
	if err := os.Chown(filepath.Dir(WyvernLinkPath(headID)), 10001, 10001); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(WyvernLinkPath(headID)), 0750); err != nil {
		return err
	}
	if err := InstallWyvernManifest(ctx, manifest); err != nil {
		return err
	}
	if _, err := m.identity(); err != nil {
		// Cold installation is complete; the operator connects Kernel in TUI.
		if _, statErr := os.Stat(m.identityPath()); os.IsNotExist(statErr) {
			return nil
		}
		return err
	}
	id, err := newWyvernToken()
	if err != nil {
		return err
	}
	_, err = m.EnsureClient(ctx, headID, head.Service, "bootstrap-"+id[:32])
	return err
}
