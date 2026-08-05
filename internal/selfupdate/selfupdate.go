package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"updater/internal/api"
	"updater/internal/config"
	"updater/internal/kernel"
)

type manifest struct {
	SchemaVersion int    `json:"schema_version"`
	Service       string `json:"service"`
	Version       string `json:"version"`
	Binary        struct {
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	} `json:"binary"`
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Draft   bool   `json:"draft"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

var versionPattern = regexp.MustCompile(`^updater-v([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?)$`)

func Run(runtime config.Runtime, headID string) error {
	if headID == "" {
		registry, err := config.LoadRegistry(runtime.RegistryPath)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(registry.Heads))
		for id := range registry.Heads {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		if len(ids) == 0 {
			return errors.New("no head service is registered")
		}
		headID = ids[0]
	}
	head, err := config.LoadHead(runtime, headID)
	if err != nil {
		return err
	}
	snapshot, err := kernel.Load(head.KernelURL, head.KernelServiceToken, head.KernelCachePath, 5*time.Second)
	if err != nil {
		return err
	}
	repositoryURL, err := kernel.String(snapshot, "repositories.updater.url")
	if err != nil {
		return err
	}
	return apply(runtime, repositoryURL)
}

func apply(runtime config.Runtime, repositoryURL string) error {
	parsed, err := url.Parse(repositoryURL)
	if err != nil || parsed.Scheme != "https" || strings.ToLower(parsed.Hostname()) != "github.com" {
		return errors.New("repositories.updater.url must identify an HTTPS GitHub repository")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 {
		return errors.New("repositories.updater.url must identify owner and repository")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(runtime.CommandTimeoutSec)*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=100", parts[0], strings.TrimSuffix(parts[1], ".git"))
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "updater-self-update")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub releases returned HTTP %d", response.StatusCode)
	}
	var releases []githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 4*1024*1024)).Decode(&releases); err != nil {
		return err
	}
	var selected *githubRelease
	for index := range releases {
		if !releases[index].Draft && versionPattern.MatchString(releases[index].TagName) {
			selected = &releases[index]
			break
		}
	}
	if selected == nil {
		return errors.New("no updater-v* release is available")
	}
	asset := func(name string) string {
		for _, item := range selected.Assets {
			if item.Name == name {
				return item.BrowserDownloadURL
			}
		}
		return ""
	}
	manifestURL := asset("updater-release.json")
	if manifestURL == "" {
		return errors.New("updater release manifest is missing")
	}
	staging := filepath.Join(runtime.StateDir, "self-update")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return err
	}
	manifestPath := filepath.Join(staging, "updater-release.json")
	if err := download(ctx, client, manifestURL, manifestPath, 2*1024*1024); err != nil {
		return err
	}
	var release manifest
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, &release); err != nil {
		return err
	}
	expectedVersion := versionPattern.FindStringSubmatch(selected.TagName)[1]
	if release.SchemaVersion != 1 || release.Service != "updater" || release.Version != expectedVersion {
		return errors.New("updater release manifest identity mismatch")
	}
	binaryPath := filepath.Join(staging, "updater.new")
	if err := download(ctx, client, release.Binary.URL, binaryPath, 64*1024*1024); err != nil {
		return err
	}
	if err := verify(binaryPath, release.Binary.SHA256); err != nil {
		return err
	}
	if runtime.DryRun {
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	previous := executable + ".previous"
	if err := copyFile(executable, previous, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(binaryPath, 0o755); err != nil {
		return err
	}
	if err := os.Rename(binaryPath, executable); err != nil {
		return err
	}
	if output, err := exec.CommandContext(ctx, "systemctl", "restart", "updater.service").CombinedOutput(); err != nil {
		_ = os.Rename(previous, executable)
		return fmt.Errorf("updater restart failed: %s", strings.TrimSpace(string(output)))
	}
	for attempt := 0; attempt < 20; attempt++ {
		var status map[string]interface{}
		if api.Request(runtime.SocketPath, http.MethodGet, "/v1/health", nil, &status) == nil {
			_ = os.Remove(previous)
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	_ = os.Rename(previous, executable)
	_, _ = exec.Command("systemctl", "restart", "updater.service").CombinedOutput()
	return errors.New("updated updater did not become healthy; previous binary restored")
}

func download(ctx context.Context, client *http.Client, rawURL, path string, maximum int64) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return errors.New("release asset URL must use HTTPS")
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	request.Header.Set("User-Agent", "updater-self-update")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("release asset returned HTTP %d", response.StatusCode)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
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

func verify(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	expected = strings.ToLower(strings.TrimPrefix(expected, "sha256:"))
	if actual != expected {
		return errors.New("updater binary checksum mismatch")
	}
	return nil
}

func copyFile(source, target string, mode os.FileMode) error {
	body, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(target, body, mode)
}
