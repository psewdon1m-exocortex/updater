package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
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
	SourceOrigin     string `json:"source_origin,omitempty"`
	SourceReason     string `json:"source_reason,omitempty"`
}

// Discover is metadata-only. Installation separately verifies signatures and
// digests for the exact candidate; provider order never determines selection.
func Discover(ctx context.Context, repository, service, current string) (Candidate, error) {
	result := Candidate{Component: service, InstalledVersion: current, Registry: "checked", Protocol: 2}
	owner, repo, err := repositoryCoordinates(repository)
	if err != nil {
		return result, err
	}
	body, err := releaseMetadata(ctx, fmt.Sprintf("https://api.github.com/repos/%s/%s/releases?per_page=100", owner, repo))
	if err != nil {
		return result, err
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

// GitHub release discovery is a read-only request. Retry a short provider or
// transport interruption, but never retry rate limits or invalid responses.
func releaseMetadata(ctx context.Context, endpoint string) ([]byte, error) {
	for attempt := 0; attempt < 3; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("User-Agent", "exocortex-updater")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			if attempt < 2 && ctx.Err() == nil {
				if err := waitDiscoveryRetry(ctx, attempt); err != nil {
					return nil, err
				}
				continue
			}
			return nil, fmt.Errorf("GitHub release request failed: %w", err)
		}
		if response.StatusCode != http.StatusOK {
			status := response.StatusCode
			rateLimited := status == http.StatusTooManyRequests || status == http.StatusForbidden && (response.Header.Get("X-RateLimit-Remaining") == "0" || response.Header.Get("Retry-After") != "")
			response.Body.Close()
			if rateLimited {
				return nil, fmt.Errorf("GitHub release API rate limit exhausted (HTTP %d)", status)
			}
			if attempt < 2 && (status == http.StatusInternalServerError || status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout) {
				if err := waitDiscoveryRetry(ctx, attempt); err != nil {
					return nil, err
				}
				continue
			}
			return nil, fmt.Errorf("GitHub release discovery returned HTTP %d", status)
		}
		body, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
		response.Body.Close()
		if err != nil || len(body) > 4*1024*1024 {
			return nil, fmt.Errorf("invalid or oversized release response")
		}
		return body, nil
	}
	return nil, fmt.Errorf("GitHub release discovery retry budget exhausted")
}

func waitDiscoveryRetry(ctx context.Context, attempt int) error {
	timer := time.NewTimer(time.Duration(attempt+1) * 200 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
