package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

var stableVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func Stable(version string) bool { return stableVersion.MatchString(version) }
func Upgrade(candidate, current string) bool {
	current = strings.TrimSuffix(current, "-dev")
	return Stable(candidate) && semver.MatchString(current) && compareVersion(candidate, current) > 0
}

type Candidate struct {
	Component        string `json:"component"`
	InstalledVersion string `json:"installed_version"`
	AvailableVersion string `json:"available_version,omitempty"`
	UpdateAvailable  bool   `json:"update_available"`
	Tag              string `json:"tag,omitempty"`
	ReleaseURL       string `json:"release_url,omitempty"`
	PublishedAt      string `json:"published_at,omitempty"`
	BackupRequired   bool   `json:"backup_required"`
	UpdaterVersion   string `json:"updater_version"`
	Registry         string `json:"registry"`
	Protocol         int    `json:"protocol"`
}

// Discover is metadata-only. Installation separately verifies signatures and
// digests for the exact candidate; provider order never determines selection.
func Discover(ctx context.Context, repository, service, current string) (Candidate, error) {
	result := Candidate{Component: service, InstalledVersion: current, Registry: "checked", Protocol: 2}
	owner, repo, err := repositoryCoordinates(repository)
	if err != nil {
		return result, err
	}
	request, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=100", owner, repo), nil)
	if err != nil {
		return result, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "exocortex-updater")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return result, fmt.Errorf("release discovery returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	if err != nil || len(body) > 4*1024*1024 {
		return result, fmt.Errorf("invalid or oversized release response")
	}
	var releases []struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		URL        string `json:"html_url"`
		Published  string `json:"published_at"`
	}
	if err = json.Unmarshal(body, &releases); err != nil {
		return result, err
	}
	for _, item := range releases {
		version, ok := versionFromServiceTag(service, item.Tag)
		if !ok || !Stable(version) || item.Draft || item.Prerelease {
			continue
		}
		if result.AvailableVersion == "" || compareVersion(version, result.AvailableVersion) > 0 {
			result.AvailableVersion = version
			result.Tag = item.Tag
			result.ReleaseURL = item.URL
			result.PublishedAt = item.Published
		}
	}
	result.UpdateAvailable = Upgrade(result.AvailableVersion, current)
	return result, nil
}
