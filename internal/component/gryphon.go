package component

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"updater/internal/config"
	"updater/internal/kernel"
)

const gryphonApp = "/usr/local/lib/gryphon/app"
const gryphonSocket = "/run/gryphon/client.sock"
const gryphonUnit = "/etc/systemd/system/gryphon.service"
const gryphonControl = "/usr/local/sbin/gryphon"

var gryphonUpdateLock sync.Mutex

type gryphonManifest struct {
	Schema   string `json:"schema"`
	Product  string `json:"product"`
	Version  string `json:"version"`
	Runtime  string `json:"runtime"`
	Artifact string `json:"artifact"`
	SHA256   string `json:"sha256"`
}

type GryphonReleaseCheck struct {
	InstalledVersion string `json:"installed_version"`
	AvailableVersion string `json:"available_version,omitempty"`
	UpdateAvailable  bool   `json:"update_available"`
}

func CheckGryphon(runtimeConfig config.Runtime, headID, currentVersion string) (GryphonReleaseCheck, error) {
	if !neptuneVersion.MatchString(strings.TrimSuffix(currentVersion, "-dev")) && !neptuneVersion.MatchString(currentVersion) {
		return GryphonReleaseCheck{}, errors.New("invalid installed Gryphon version")
	}
	head, err := config.LoadHead(runtimeConfig, headID)
	if err != nil {
		return GryphonReleaseCheck{}, err
	}
	snapshot, err := kernel.Load(head.KernelURL, head.KernelServiceToken, head.KernelCachePath, 5*time.Second)
	if err != nil {
		return GryphonReleaseCheck{}, err
	}
	repositoryURL, err := kernel.String(snapshot, "repositories.gryphon.url")
	if err != nil {
		return GryphonReleaseCheck{}, err
	}
	owner, repository, err := gryphonRepository(repositoryURL)
	if err != nil {
		return GryphonReleaseCheck{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=100", owner, repository), nil)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "exocortex-updater")
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return GryphonReleaseCheck{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return GryphonReleaseCheck{}, fmt.Errorf("GitHub Gryphon releases returned HTTP %d", response.StatusCode)
	}
	var releases []githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 4*1024*1024)).Decode(&releases); err != nil {
		return GryphonReleaseCheck{}, err
	}
	available := ""
	for _, release := range releases {
		if release.Draft || release.Prerelease || !strings.HasPrefix(release.TagName, "gryphon-linux-v") {
			continue
		}
		candidate := strings.TrimPrefix(release.TagName, "gryphon-linux-v")
		if neptuneVersion.MatchString(candidate) && (available == "" || compareVersion(candidate, available) > 0) {
			available = candidate
		}
	}
	result := GryphonReleaseCheck{InstalledVersion: currentVersion, AvailableVersion: available}
	result.UpdateAvailable = available != "" && compareVersion(available, currentVersion) > 0
	return result, nil
}

func UpdateGryphon(runtimeConfig config.Runtime, headID, version string) error {
	if !neptuneVersion.MatchString(version) {
		return errors.New("invalid Gryphon version")
	}
	if !gryphonUpdateLock.TryLock() {
		return errors.New("a Gryphon update is already running")
	}
	defer gryphonUpdateLock.Unlock()
	lock, err := os.OpenFile("/run/lock/gryphon-update.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("a Gryphon update is already running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if !gryphonInstallationComplete() {
		return errors.New("Gryphon Linux must be installed before it can be updated")
	}
	installedVersion, err := installedGryphonVersion()
	if err != nil {
		return err
	}
	check, err := CheckGryphon(runtimeConfig, headID, installedVersion)
	if err != nil {
		return err
	}
	if !check.UpdateAvailable || check.AvailableVersion != version {
		return errors.New("requested Gryphon version is not the current upgrade candidate")
	}

	head, err := config.LoadHead(runtimeConfig, headID)
	if err != nil {
		return err
	}
	snapshot, err := kernel.Load(head.KernelURL, head.KernelServiceToken, head.KernelCachePath, 5*time.Second)
	if err != nil {
		return err
	}
	repositoryURL, err := kernel.String(snapshot, "repositories.gryphon.url")
	if err != nil {
		return err
	}
	owner, repository, err := gryphonRepository(repositoryURL)
	if err != nil {
		return err
	}
	platform := "linux-" + runtime.GOARCH
	if platform != "linux-amd64" && platform != "linux-arm64" {
		return fmt.Errorf("unsupported Gryphon runtime %s", platform)
	}
	releaseRuntime := strings.Replace(platform, "amd64", "x64", 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(runtimeConfig.CommandTimeoutSec)*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}
	release, err := fetchRelease(ctx, client, owner, repository, "gryphon-linux-v"+version)
	if err != nil {
		return err
	}
	manifestName := "gryphon-linux-release-" + releaseRuntime + ".json"
	manifestURL := releaseAsset(release, manifestName)
	if manifestURL == "" {
		return fmt.Errorf("Gryphon release is missing %s", manifestName)
	}
	staging, err := os.MkdirTemp(runtimeConfig.StateDir, "gryphon-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	manifestPath := filepath.Join(staging, manifestName)
	if err := download(ctx, client, manifestURL, manifestPath, 2*1024*1024); err != nil {
		return err
	}
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var manifest gryphonManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return fmt.Errorf("invalid Gryphon release manifest: %w", err)
	}
	if manifest.Schema != "exocortex.gryphon.release.v1" || manifest.Product != "gryphon-linux" || manifest.Version != version || manifest.Runtime != releaseRuntime {
		return errors.New("Gryphon release manifest identity mismatch")
	}
	artifactURL := releaseAsset(release, manifest.Artifact)
	if artifactURL == "" {
		return errors.New("Gryphon release artifact is missing")
	}
	archivePath := filepath.Join(staging, "gryphon.tar.gz")
	if err := download(ctx, client, artifactURL, archivePath, 64*1024*1024); err != nil {
		return err
	}
	if err := verifySHA256(archivePath, manifest.SHA256); err != nil {
		return err
	}
	extracted := filepath.Join(staging, "app")
	if err := extractGryphonApp(archivePath, extracted, version); err != nil {
		return err
	}
	if runtimeConfig.DryRun {
		return nil
	}
	return replaceGryphon(ctx, extracted)
}

func gryphonRepository(raw string) (string, string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || strings.ToLower(parsed.Hostname()) != "github.com" {
		return "", "", errors.New("repositories.gryphon.url must identify an HTTPS GitHub repository")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 {
		return "", "", errors.New("repositories.gryphon.url must identify owner and repository")
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
}

func gryphonInstallationComplete() bool {
	for _, target := range []string{filepath.Join(gryphonApp, "dist", "main.js"), filepath.Join(gryphonApp, "package.json"), gryphonUnit, gryphonControl} {
		if info, err := os.Stat(target); err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

func installedGryphonVersion() (string, error) {
	body, err := os.ReadFile(filepath.Join(gryphonApp, "package.json"))
	if err != nil {
		return "", err
	}
	var identity struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &identity); err != nil || !neptuneVersion.MatchString(identity.Version) {
		return "", errors.New("installed Gryphon version is invalid")
	}
	return identity.Version, nil
}

func extractGryphonApp(archivePath, target, version string) error {
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	entries, total := 0, int64(0)
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		entries++
		if entries > 512 {
			return errors.New("Gryphon release contains too many entries")
		}
		name := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(header.Name)), "./")
		allowed := name == "package.json" || strings.HasPrefix(name, "dist/")
		if !allowed {
			continue
		}
		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(filepath.Join(target, filepath.FromSlash(name)), 0o700); err != nil {
				return err
			}
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > 8*1024*1024 {
			return fmt.Errorf("Gryphon release entry %s is invalid", name)
		}
		total += header.Size
		if total > 32*1024*1024 {
			return errors.New("Gryphon release expands beyond 32 MB")
		}
		destination := filepath.Join(target, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		written, copyErr := io.Copy(output, io.LimitReader(reader, header.Size+1))
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if written != header.Size {
			return fmt.Errorf("Gryphon release entry %s is truncated", name)
		}
	}
	for _, required := range []string{"package.json", "dist/main.js", "dist/cli.js"} {
		if info, err := os.Stat(filepath.Join(target, filepath.FromSlash(required))); err != nil || info.IsDir() {
			return fmt.Errorf("Gryphon release does not contain %s", required)
		}
	}
	packageBody, err := os.ReadFile(filepath.Join(target, "package.json"))
	if err != nil {
		return err
	}
	var identity struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(packageBody, &identity); err != nil || identity.Version != version {
		return errors.New("Gryphon package version does not match the release")
	}
	return nil
}

func copyTree(source, target string) error {
	return filepath.WalkDir(source, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("Gryphon candidate contains a symbolic link")
		}
		return copyFile(current, destination, 0o644)
	})
}

func replaceGryphon(ctx context.Context, extracted string) error {
	candidate, previous, failed := gryphonApp+".new", gryphonApp+".previous", gryphonApp+".failed"
	for _, target := range []string{candidate, previous, failed} {
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	}
	if err := copyTree(extracted, candidate); err != nil {
		return err
	}
	defer os.RemoveAll(candidate)
	if err := os.Rename(gryphonApp, previous); err != nil {
		return err
	}
	if err := os.Rename(candidate, gryphonApp); err != nil {
		_ = os.Rename(previous, gryphonApp)
		return err
	}
	if err := restartGryphon(ctx); err == nil {
		return os.RemoveAll(previous)
	} else {
		_ = os.Rename(gryphonApp, failed)
		_ = os.Rename(previous, gryphonApp)
		_, _ = exec.Command("systemctl", "restart", "gryphon.service").CombinedOutput()
		_ = os.RemoveAll(failed)
		return err
	}
}

func restartGryphon(ctx context.Context) error {
	if output, err := exec.CommandContext(ctx, "systemctl", "restart", "gryphon.service").CombinedOutput(); err != nil {
		return fmt.Errorf("Gryphon restart failed: %s", strings.TrimSpace(string(output)))
	}
	for attempt := 0; attempt < 30; attempt++ {
		dialer := net.Dialer{Timeout: time.Second}
		connection, err := dialer.DialContext(ctx, "unix", gryphonSocket)
		if err == nil {
			_, writeErr := io.WriteString(connection, "GET /v1/health HTTP/1.1\r\nHost: gryphon.local\r\nConnection: close\r\n\r\n")
			buffer := make([]byte, 64)
			count, readErr := connection.Read(buffer)
			_ = connection.Close()
			if writeErr == nil && readErr == nil && strings.Contains(string(buffer[:count]), " 200 ") {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("updated Gryphon did not become healthy")
}
