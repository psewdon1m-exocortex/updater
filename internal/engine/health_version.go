package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"updater/internal/config"
	"updater/internal/release"
)

var errMissingRunningVersion = errors.New("running service does not report a version")

func (e *Engine) checkHeadVersion(ctx context.Context, head config.HeadConfig, version, image string) error {
	err := e.checkVersionFn(ctx, head.LocalHealthURL, version)
	if !errors.Is(err, errMissingRunningVersion) || (head.Service != "volt" && head.Service != "saturn") || !release.Stable(version) || release.SupportsMinimum(version, "0.2.0") {
		return err
	}
	// These published legacy images have no version field in health. Verify the
	// live container against the exact immutable artifact, never against .env.
	if !regexp.MustCompile(`^[A-Za-z0-9._:/-]+@sha256:[a-f0-9]{64}$`).MatchString(image) {
		return errors.New("legacy running image is not pinned to a digest")
	}
	output, err := e.runner.Run(ctx, "docker", []string{"inspect", "--format", "{{.State.Running}} {{.Image}} {{.Config.Image}}", head.ContainerName}, nil, head.ProjectDir)
	if err != nil {
		return errors.New("cannot inspect legacy running container")
	}
	fields := strings.Fields(string(output))
	if len(fields) != 3 || fields[0] != "true" || fields[2] != image {
		return errors.New("legacy running container does not match the selected image")
	}
	actual, err := e.runner.Run(ctx, "docker", []string{"image", "inspect", "--format", "{{.Id}}", image}, nil, head.ProjectDir)
	if err != nil || strings.TrimSpace(string(actual)) != fields[1] {
		return errors.New("legacy running container image identity differs from the signed release")
	}
	return nil
}

func checkRunningVersion(ctx context.Context, endpoint, expected string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	for attempt := 0; attempt < 12; attempt++ {
		request, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		if err != nil {
			return err
		}
		response, err := client.Do(request)
		if err == nil {
			var health struct {
				Version string `json:"version"`
			}
			err = json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&health)
			response.Body.Close()
			if err == nil && response.StatusCode == 200 && strings.TrimSuffix(health.Version, "-dev") == expected {
				return nil
			}
			if err == nil && response.StatusCode == 200 && health.Version == "" {
				return errMissingRunningVersion
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("running service version does not match the selected release")
}
