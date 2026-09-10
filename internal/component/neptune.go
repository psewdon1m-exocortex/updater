package component

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"updater/internal/config"
	"updater/internal/kernel"
)

const neptuneBinary = "/usr/local/lib/neptune/neptuned"
const neptuneSocket = "/run/neptune/neptuned.sock"
const neptuneUnit = "/etc/systemd/system/neptune.service"
const neptuneControl = "/usr/local/sbin/neptunectl"

var neptuneVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)
var neptuneUpdateLock sync.Mutex

type neptuneManifest struct {
	Schema   string `json:"schema"`
	Product  string `json:"product"`
	Version  string `json:"version"`
	Runtime  string `json:"runtime"`
	Artifact string `json:"artifact"`
	SHA256   string `json:"sha256"`
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

type NeptuneReleaseCheck struct {
	InstalledVersion string `json:"installed_version"`
	AvailableVersion string `json:"available_version,omitempty"`
	UpdateAvailable  bool   `json:"update_available"`
}

func CheckNeptune(runtimeConfig config.Runtime, headID, currentVersion string) (NeptuneReleaseCheck, error) {
	if !neptuneVersion.MatchString(strings.TrimSuffix(currentVersion, "-dev")) && !neptuneVersion.MatchString(currentVersion) {
		return NeptuneReleaseCheck{}, errors.New("invalid installed Neptune version")
	}
	head, err := config.LoadHead(runtimeConfig, headID)
	if err != nil {
		return NeptuneReleaseCheck{}, err
	}
	snapshot, err := kernel.Load(head.KernelURL, head.KernelServiceToken, head.KernelCachePath, 5*time.Second)
	if err != nil {
		return NeptuneReleaseCheck{}, err
	}
	repositoryURL, err := kernel.String(snapshot, "repositories.neptune.url")
	if err != nil {
		return NeptuneReleaseCheck{}, err
	}
	owner, repository, err := githubRepository(repositoryURL)
	if err != nil {
		return NeptuneReleaseCheck{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=100", owner, repository), nil)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "exocortex-updater")
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return NeptuneReleaseCheck{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return NeptuneReleaseCheck{}, fmt.Errorf("GitHub Neptune releases returned HTTP %d", response.StatusCode)
	}
	var releases []githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 4*1024*1024)).Decode(&releases); err != nil {
		return NeptuneReleaseCheck{}, err
	}
	available := ""
	for _, release := range releases {
		if release.Draft || release.Prerelease || !strings.HasPrefix(release.TagName, "neptune-linux-v") {
			continue
		}
		candidate := strings.TrimPrefix(release.TagName, "neptune-linux-v")
		if neptuneVersion.MatchString(candidate) && (available == "" || compareVersion(candidate, available) > 0) {
			available = candidate
		}
	}
	result := NeptuneReleaseCheck{InstalledVersion: currentVersion, AvailableVersion: available}
	result.UpdateAvailable = available != "" && compareVersion(available, currentVersion) > 0
	return result, nil
}

func InstallLatestNeptune(runtimeConfig config.Runtime, headID string) (string, error) {
	current := "0.0.0"
	versionContext, cancelVersion := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelVersion()
	if neptuneInstallationComplete() {
		output, err := exec.CommandContext(versionContext, neptuneBinary, "version").Output()
		if err == nil {
			candidate := strings.TrimSpace(string(output))
			if neptuneVersion.MatchString(strings.TrimSuffix(candidate, "-dev")) || neptuneVersion.MatchString(candidate) {
				current = candidate
			}
		}
	}
	check, err := CheckNeptune(runtimeConfig, headID, current)
	if err != nil {
		return "", err
	}
	if check.AvailableVersion == "" {
		return "", errors.New("no Neptune Linux release is available")
	}
	if current != "0.0.0" && !check.UpdateAvailable {
		return current, nil
	}
	if err := UpdateNeptune(runtimeConfig, headID, check.AvailableVersion); err != nil {
		return "", err
	}
	return check.AvailableVersion, nil
}

func compareVersion(left, right string) int {
	type versionParts struct {
		numbers [3]int
		suffix  string
	}
	parse := func(value string) versionParts {
		var parsed [3]int
		pieces := strings.SplitN(value, "-", 2)
		_, _ = fmt.Sscanf(pieces[0], "%d.%d.%d", &parsed[0], &parsed[1], &parsed[2])
		suffix := ""
		if len(pieces) == 2 {
			suffix = pieces[1]
		}
		return versionParts{numbers: parsed, suffix: suffix}
	}
	a, b := parse(left), parse(right)
	for index := 0; index < 3; index++ {
		if a.numbers[index] < b.numbers[index] {
			return -1
		}
		if a.numbers[index] > b.numbers[index] {
			return 1
		}
	}
	if a.suffix == b.suffix {
		return 0
	}
	if a.suffix == "" {
		return 1
	}
	if b.suffix == "" {
		return -1
	}
	return strings.Compare(a.suffix, b.suffix)
}

func UpdateNeptune(runtimeConfig config.Runtime, headID, version string) error {
	if !neptuneVersion.MatchString(version) {
		return errors.New("invalid Neptune version")
	}
	if !neptuneUpdateLock.TryLock() {
		return errors.New("a Neptune update is already running")
	}
	defer neptuneUpdateLock.Unlock()
	lock, err := os.OpenFile("/run/lock/neptune-update.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("a Neptune update is already running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	head, err := config.LoadHead(runtimeConfig, headID)
	if err != nil {
		return err
	}
	snapshot, err := kernel.Load(head.KernelURL, head.KernelServiceToken, head.KernelCachePath, 5*time.Second)
	if err != nil {
		return err
	}
	repositoryURL, err := kernel.String(snapshot, "repositories.neptune.url")
	if err != nil {
		return err
	}
	owner, repository, err := githubRepository(repositoryURL)
	if err != nil {
		return err
	}
	platform := "linux-" + runtime.GOARCH
	if platform != "linux-amd64" && platform != "linux-arm64" {
		return fmt.Errorf("unsupported Neptune runtime %s", platform)
	}
	releaseRuntime := strings.Replace(platform, "amd64", "x64", 1)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(runtimeConfig.CommandTimeoutSec)*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}
	release, err := fetchRelease(ctx, client, owner, repository, "neptune-linux-v"+version)
	if err != nil {
		return err
	}
	manifestName := "neptune-linux-release-" + releaseRuntime + ".json"
	manifestURL := releaseAsset(release, manifestName)
	if manifestURL == "" {
		return fmt.Errorf("Neptune release is missing %s", manifestName)
	}
	staging, err := os.MkdirTemp(filepath.Join(runtimeConfig.StateDir), "neptune-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	manifestPath := filepath.Join(staging, manifestName)
	if err := download(ctx, client, manifestURL, manifestPath, 2*1024*1024); err != nil {
		return err
	}
	var manifest neptuneManifest
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return fmt.Errorf("invalid Neptune release manifest: %w", err)
	}
	if manifest.Schema != "exocortex.neptune.release.v1" || manifest.Product != "neptune-linux" || manifest.Version != version || manifest.Runtime != releaseRuntime {
		return errors.New("Neptune release manifest identity mismatch")
	}
	artifactURL := releaseAsset(release, manifest.Artifact)
	if artifactURL == "" {
		return errors.New("Neptune release artifact is missing")
	}
	archivePath := filepath.Join(staging, "neptune.tar.gz")
	if err := download(ctx, client, artifactURL, archivePath, 256*1024*1024); err != nil {
		return err
	}
	if err := verifySHA256(archivePath, manifest.SHA256); err != nil {
		return err
	}
	newBinary := filepath.Join(staging, "neptuned.new")
	if err := extractNeptuneBinary(archivePath, newBinary); err != nil {
		return err
	}
	if runtimeConfig.DryRun {
		return nil
	}
	if !neptuneInstallationComplete() {
		return installFreshNeptune(ctx, archivePath, staging, head)
	}
	installCandidate := neptuneBinary + ".new"
	if err := copyFile(newBinary, installCandidate, 0o755); err != nil {
		return err
	}
	defer os.Remove(installCandidate)
	previous := neptuneBinary + ".previous"
	if err := copyFile(neptuneBinary, previous, 0o755); err != nil {
		return err
	}
	if err := os.Rename(installCandidate, neptuneBinary); err != nil {
		return err
	}
	if err := restartNeptune(ctx); err == nil {
		_ = os.Remove(previous)
		return nil
	} else {
		_ = os.Rename(previous, neptuneBinary)
		_, _ = exec.Command("systemctl", "restart", "neptune.service").CombinedOutput()
		return err
	}
}

func neptuneInstallationComplete() bool {
	for _, path := range []string{neptuneBinary, neptuneUnit, neptuneControl} {
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

func installFreshNeptune(ctx context.Context, archivePath, staging string, head config.HeadConfig) error {
	target := filepath.Join(staging, "install")
	if err := os.Mkdir(target, 0o700); err != nil {
		return err
	}
	allowed := map[string]int64{
		"neptuned": 192 * 1024 * 1024, "install.sh": 1024 * 1024, "neptunectl": 1024 * 1024,
		"neptune.service": 1024 * 1024, "appsettings.json": 1024 * 1024,
	}
	if err := extractNeptuneFiles(archivePath, target, allowed); err != nil {
		return err
	}
	if err := os.MkdirAll("/etc/neptune", 0o750); err != nil {
		return err
	}
	if err := os.WriteFile("/etc/neptune/kernel.token", []byte(head.KernelServiceToken+"\n"), 0o600); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "/bin/sh", filepath.Join(target, "install.sh"))
	command.Dir = target
	command.Env = append(os.Environ(), "NEPTUNE_KERNEL_URL="+head.KernelURL, "NEPTUNE_KERNEL_TOKEN_FILE=/etc/neptune/kernel.token")
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("Neptune installation failed: %s", strings.TrimSpace(string(output)))
	}
	return restartNeptune(ctx)
}

func extractNeptuneFiles(archivePath, target string, allowed map[string]int64) error {
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
	found := map[string]bool{}
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		name := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(header.Name)), "./")
		maximum, ok := allowed[name]
		if !ok {
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > maximum {
			return fmt.Errorf("Neptune release entry %s is invalid", name)
		}
		mode := os.FileMode(0o600)
		if name == "neptuned" || name == "install.sh" || name == "neptunectl" {
			mode = 0o700
		}
		output, openErr := os.OpenFile(filepath.Join(target, name), os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode)
		if openErr != nil {
			return openErr
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
			return fmt.Errorf("Neptune release entry %s is truncated", name)
		}
		found[name] = true
	}
	for name := range allowed {
		if !found[name] {
			return fmt.Errorf("Neptune release does not contain %s", name)
		}
	}
	return nil
}

func githubRepository(raw string) (string, string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || strings.ToLower(parsed.Hostname()) != "github.com" {
		return "", "", errors.New("repositories.neptune.url must identify an HTTPS GitHub repository")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 {
		return "", "", errors.New("repositories.neptune.url must identify owner and repository")
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
}

func fetchRelease(ctx context.Context, client *http.Client, owner, repository, tag string) (githubRelease, error) {
	raw := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/tags/%s", owner, repository, url.PathEscape(tag))
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "exocortex-updater")
	response, err := client.Do(request)
	if err != nil {
		return githubRelease{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return githubRelease{}, fmt.Errorf("GitHub release returned HTTP %d", response.StatusCode)
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 4*1024*1024)).Decode(&release); err != nil {
		return githubRelease{}, err
	}
	if release.Draft || release.TagName != tag {
		return githubRelease{}, errors.New("release identity mismatch")
	}
	return release, nil
}

func releaseAsset(release githubRelease, name string) string {
	for _, asset := range release.Assets {
		if asset.Name == name {
			return asset.URL
		}
	}
	return ""
}

func download(ctx context.Context, client *http.Client, rawURL, target string, maximum int64) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return errors.New("release asset URL must use HTTPS")
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	request.Header.Set("User-Agent", "exocortex-updater")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("release asset returned HTTP %d", response.StatusCode)
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	written, err := io.Copy(file, io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return err
	}
	if written > maximum {
		return errors.New("release asset exceeds size limit")
	}
	return file.Sync()
}

func verifySHA256(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(digest.Sum(nil))
	if actual != strings.ToLower(strings.TrimPrefix(expected, "sha256:")) {
		return errors.New("release checksum mismatch")
	}
	return nil
}

func extractNeptuneBinary(archivePath, target string) error {
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
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		clean := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(header.Name)), "./")
		if clean != "neptuned" {
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > 192*1024*1024 {
			return errors.New("Neptune binary entry is invalid")
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o700)
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
			return errors.New("Neptune binary entry is truncated")
		}
		return nil
	}
	return errors.New("Neptune release does not contain neptuned")
}

func copyFile(source, target string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func restartNeptune(ctx context.Context) error {
	if output, err := exec.CommandContext(ctx, "systemctl", "restart", "neptune.service").CombinedOutput(); err != nil {
		return fmt.Errorf("Neptune restart failed: %s", strings.TrimSpace(string(output)))
	}
	for attempt := 0; attempt < 30; attempt++ {
		dialer := net.Dialer{Timeout: time.Second}
		connection, err := dialer.DialContext(ctx, "unix", neptuneSocket)
		if err == nil {
			request := "GET /v1/health HTTP/1.1\r\nHost: neptune.local\r\nConnection: close\r\n\r\n"
			_, writeErr := io.WriteString(connection, request)
			buffer := make([]byte, 64)
			count, readErr := connection.Read(buffer)
			_ = connection.Close()
			if writeErr == nil && readErr == nil && strings.Contains(string(buffer[:count]), " 200 ") {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New("updated Neptune did not become healthy")
}
