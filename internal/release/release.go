package release

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
	"strconv"
	"strings"
	"time"

	"updater/internal/model"
)

var semver = regexp.MustCompile(`^([0-9]+)\.([0-9]+)\.([0-9]+)(?:-([0-9A-Za-z.-]+))?$`)

type githubRelease struct {
	TagName string `json:"tag_name"`
	Draft   bool   `json:"draft"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

type Resolved struct {
	Manifest     model.ReleaseManifest
	ManifestPath string
	BundlePath   string
	ComposePath  string
}

func Resolve(ctx context.Context, repositoryURL, service, requestedVersion, stagingDir string, allowUnsigned bool) (Resolved, error) {
	owner, repository, err := repositoryCoordinates(repositoryURL)
	if err != nil {
		return Resolved{}, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=100", url.PathEscape(owner), url.PathEscape(repository))
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "updater")
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	response, err := client.Do(request)
	if err != nil {
		return Resolved{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Resolved{}, fmt.Errorf("GitHub releases returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if err != nil || len(body) > 4*1024*1024 {
		return Resolved{}, errors.New("GitHub releases response is invalid or too large")
	}
	var releases []githubRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return Resolved{}, err
	}
	prefix := service + "-v"
	candidates := make([]githubRelease, 0)
	for _, item := range releases {
		if item.Draft || !strings.HasPrefix(strings.ToLower(item.TagName), strings.ToLower(prefix)) {
			continue
		}
		version := item.TagName[len(prefix):]
		if !semver.MatchString(version) || (requestedVersion != "" && version != requestedVersion) {
			continue
		}
		candidates = append(candidates, item)
	}
	sort.Slice(candidates, func(i, j int) bool {
		return compareVersion(candidates[i].TagName[len(prefix):], candidates[j].TagName[len(prefix):]) > 0
	})
	if len(candidates) == 0 {
		return Resolved{}, fmt.Errorf("no %s release matches version %q", service, requestedVersion)
	}
	selected := candidates[0]
	assetURL := func(name string) string {
		for _, asset := range selected.Assets {
			if asset.Name == name {
				return asset.BrowserDownloadURL
			}
		}
		return ""
	}
	manifestName := service + "-release.json"
	manifestURL := assetURL(manifestName)
	bundleURL := assetURL(manifestName + ".sigstore.json")
	if manifestURL == "" || (!allowUnsigned && bundleURL == "") {
		return Resolved{}, errors.New("release manifest or Sigstore bundle is missing")
	}
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return Resolved{}, err
	}
	manifestPath := filepath.Join(stagingDir, manifestName)
	if err := download(ctx, client, manifestURL, manifestPath, 2*1024*1024); err != nil {
		return Resolved{}, err
	}
	bundlePath := filepath.Join(stagingDir, manifestName+".sigstore.json")
	if bundleURL != "" {
		if err := download(ctx, client, bundleURL, bundlePath, 8*1024*1024); err != nil {
			return Resolved{}, err
		}
	}
	if !allowUnsigned {
		if err := verifyCosign(ctx, manifestPath, bundlePath); err != nil {
			return Resolved{}, err
		}
	}
	manifestBody, err := os.ReadFile(manifestPath)
	if err != nil {
		return Resolved{}, err
	}
	var manifest model.ReleaseManifest
	if err := json.Unmarshal(manifestBody, &manifest); err != nil {
		return Resolved{}, fmt.Errorf("invalid release manifest: %w", err)
	}
	version := selected.TagName[len(prefix):]
	if manifest.SchemaVersion != 1 || manifest.Service != service || manifest.Version != version {
		return Resolved{}, errors.New("release manifest identity does not match the selected release")
	}
	if !strings.HasPrefix(manifest.Image.Digest, "sha256:") || manifest.Image.Reference == "" {
		return Resolved{}, errors.New("release manifest image is invalid")
	}
	if !semver.MatchString(manifest.MinimumUpdaterVersion) {
		return Resolved{}, errors.New("release manifest minimum_updater_version is invalid")
	}
	if manifest.DatabaseSchema < 1 {
		return Resolved{}, errors.New("release manifest database_schema is invalid")
	}
	composePath := filepath.Join(stagingDir, service+"-compose.tar.gz")
	if err := download(ctx, client, manifest.ComposeBundle.URL, composePath, 32*1024*1024); err != nil {
		return Resolved{}, err
	}
	if err := verifySHA256(composePath, manifest.ComposeBundle.SHA256); err != nil {
		return Resolved{}, err
	}
	return Resolved{Manifest: manifest, ManifestPath: manifestPath, BundlePath: bundlePath, ComposePath: composePath}, nil
}

func repositoryCoordinates(raw string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || strings.ToLower(parsed.Hostname()) != "github.com" {
		return "", "", errors.New("repository must be an HTTPS GitHub URL")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 {
		return "", "", errors.New("repository URL must identify owner and repository")
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), nil
}

func download(ctx context.Context, client *http.Client, rawURL, path string, maximum int64) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return errors.New("release asset URL must use HTTPS")
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	request.Header.Set("User-Agent", "updater")
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
		return errors.New("release asset exceeds the configured size limit")
	}
	return file.Sync()
}

func verifyCosign(ctx context.Context, artifact, bundle string) error {
	if _, err := exec.LookPath("cosign"); err != nil {
		return errors.New("cosign is required to verify production releases")
	}
	command := exec.CommandContext(ctx, "cosign", "verify-blob",
		"--bundle", bundle,
		"--certificate-identity-regexp", `^https://github.com/.+/.github/workflows/release.yml@refs/tags/.+$`,
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com",
		artifact,
	)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("Sigstore verification failed: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func verifySHA256(path, expected string) error {
	expected = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(expected), "sha256:"))
	if len(expected) != 64 {
		return errors.New("expected SHA-256 is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected {
		return errors.New("release asset checksum mismatch")
	}
	return nil
}

func compareVersion(left, right string) int {
	a, b := semver.FindStringSubmatch(left), semver.FindStringSubmatch(right)
	for index := 1; index <= 3; index++ {
		av, _ := strconv.Atoi(a[index])
		bv, _ := strconv.Atoi(b[index])
		if av > bv {
			return 1
		}
		if av < bv {
			return -1
		}
	}
	if a[4] == b[4] {
		return 0
	}
	if a[4] == "" {
		return 1
	}
	if b[4] == "" {
		return -1
	}
	return strings.Compare(a[4], b[4])
}

func SupportsMinimum(current, minimum string) bool {
	if minimum == "" {
		return true
	}
	if !semver.MatchString(current) || !semver.MatchString(minimum) {
		return false
	}
	return compareVersion(current, minimum) >= 0
}
