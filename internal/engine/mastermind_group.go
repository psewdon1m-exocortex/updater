package engine

import (
	"bytes"
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
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"updater/internal/config"
	"updater/internal/model"
	"updater/internal/release"
)

var componentNames = []string{"core", "runtime", "worker"}

// An interrupted group mutation cannot be treated as an ordinary failed image
// pull. Recover its old data/components before Core can reopen owner writes.
func (e *Engine) ResumeMastermindRecovery() {
	for _, job := range e.store.List() {
		if job.Service == "mastermind" && job.RecoveryPending {
			_, _ = e.Rollback(job.ID)
			return
		}
	}
}

func componentEnvironment(images map[string]string, version string) map[string]string {
	return map[string]string{"MASTERMIND_CORE_IMAGE": images["core"], "MASTERMIND_RUNTIME_IMAGE": images["runtime"],
		"MASTERMIND_WORKER_IMAGE": images["worker"], "MASTERMIND_VERSION": version}
}

func composeArguments(head config.HeadConfig, args ...string) []string {
	return append([]string{"compose", "--env-file", head.EnvFile, "-f", filepath.Join(head.ProjectDir, head.ComposeFile)}, args...)
}

func (e *Engine) groupCommand(ctx context.Context, head config.HeadConfig, args ...string) error {
	_, err := e.runner.Run(ctx, "docker", composeArguments(head, args...), nil, head.ProjectDir)
	if err != nil {
		return errors.New("Mastermind component operation failed: " + args[0])
	}
	return nil
}

func (e *Engine) startMastermindGroup(ctx context.Context, head config.HeadConfig) error {
	if err := e.groupCommand(ctx, head, "up", "-d", "--no-deps", "--wait", "--wait-timeout", "120", "runtime", "worker"); err != nil {
		return err
	}
	return e.groupCommand(ctx, head, "up", "-d", "--no-deps", "core")
}

func mastermindControl(ctx context.Context, head config.HeadConfig, operation string, input any) (map[string]any, error) {
	parsed, err := url.Parse(head.LocalHealthURL)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.User != nil ||
		(parsed.Hostname() != "localhost" && (net.ParseIP(parsed.Hostname()) == nil || !net.ParseIP(parsed.Hostname()).IsLoopback())) {
		return nil, errors.New("Mastermind control endpoint must be registered loopback HTTP(S)")
	}
	if operation != "confirm" && operation != "functional" {
		return nil, errors.New("unsupported Mastermind control operation")
	}
	parsed.Path = "/api/internal/updater/" + operation
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	body, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+head.ControlToken)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 120 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("Mastermind control endpoint is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("Mastermind %s returned HTTP %d", operation, response.StatusCode)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(payload) > 65536 {
		return nil, errors.New("Mastermind control response exceeds its limit")
	}
	var result map[string]any
	err = json.Unmarshal(payload, &result)
	return result, err
}

func (e *Engine) validateGroupCompose(ctx context.Context, head config.HeadConfig, filename string, images map[string]string, version string) error {
	environment := []string{}
	for key, value := range componentEnvironment(images, version) {
		environment = append(environment, key+"="+value)
	}
	output, err := e.runner.Run(ctx, "docker", []string{"compose", "--env-file", head.EnvFile, "-f", filename, "--project-directory", head.ProjectDir, "config", "--format", "json"}, environment, head.ProjectDir)
	if err != nil || len(output) > 1024*1024 {
		return errors.New("Mastermind Compose profile could not be rendered within its bound")
	}
	var rendered struct {
		Name     string `json:"name"`
		Services map[string]struct {
			Image         string   `json:"image"`
			ContainerName string   `json:"container_name"`
			User          string   `json:"user"`
			Privileged    bool     `json:"privileged"`
			ReadOnly      bool     `json:"read_only"`
			NetworkMode   string   `json:"network_mode"`
			CapAdd        []string `json:"cap_add"`
			CapDrop       []string `json:"cap_drop"`
			SecurityOpt   []string `json:"security_opt"`
			Devices       []any    `json:"devices"`
			Entrypoint    any      `json:"entrypoint"`
			Command       any      `json:"command"`
			Ports         []struct {
				HostIP    string `json:"host_ip"`
				Target    int    `json:"target"`
				Published string `json:"published"`
			} `json:"ports"`
			Volumes []struct {
				Type     string `json:"type"`
				Source   string `json:"source"`
				Target   string `json:"target"`
				ReadOnly bool   `json:"read_only"`
			} `json:"volumes"`
		} `json:"services"`
	}
	if json.Unmarshal(output, &rendered) != nil || rendered.Name != head.ID || len(rendered.Services) != 3 {
		return errors.New("Mastermind Compose must contain only the registered three-component project")
	}
	for _, name := range componentNames {
		service, ok := rendered.Services[name]
		if !ok || service.Image != images[name] || service.Privileged || !service.ReadOnly || service.NetworkMode != "" ||
			service.User != "10001:10001" || len(service.CapAdd) != 0 || len(service.Devices) != 0 ||
			len(service.SecurityOpt) != 1 || service.SecurityOpt[0] != "no-new-privileges:true" ||
			len(service.CapDrop) != 1 || !strings.EqualFold(service.CapDrop[0], "ALL") || service.Command != nil || service.Entrypoint != nil ||
			service.ContainerName != "" && service.ContainerName != head.ID+"-"+name+"-1" {
			return errors.New("Mastermind component violates its fixed runtime profile: " + name)
		}
		if name == "core" && len(service.Ports) != 1 || name != "core" && len(service.Ports) != 0 {
			return errors.New("Mastermind profile does not have its single owner listener")
		}
		for _, port := range service.Ports {
			if name != "core" || port.HostIP != "127.0.0.1" || port.Target != 18390 || port.Published != "18390" {
				return errors.New("Mastermind profile exposes a private component")
			}
		}
		required := map[string]map[string]string{
			"core":    {"core-data": "/data", "vault-data": "/data/vault"},
			"runtime": {"vault-data": "/vault", "runtime-data": "/home/mastermind"},
			"worker":  {"work-data": "/work"},
		}[name]
		mounted, targets := map[string]bool{}, map[string]bool{}
		for _, volume := range service.Volumes {
			if targets[volume.Target] {
				return errors.New("Mastermind mount target is duplicated")
			}
			targets[volume.Target] = true
			if volume.Type == "volume" {
				if required[volume.Source] != volume.Target || mounted[volume.Source] || volume.ReadOnly {
					return errors.New("Mastermind profile crosses a component data boundary")
				}
				mounted[volume.Source] = true
				continue
			}
			if volume.Type != "bind" || !volume.ReadOnly {
				return errors.New("Mastermind profile contains an unsupported host mount")
			}
			allowed := filepath.Join(head.ProjectDir, "secrets", name)
			ownSecret := volume.Source == allowed && volume.Target == "/run/mastermind"
			agentSocket := name == "core" && volume.Source == volume.Target && (volume.Source == "/run/neptune" || volume.Source == "/run/wyvern" || volume.Source == filepath.Dir(e.runtime.SocketPath))
			wyvernLink := name == "core" && volume.Source == filepath.Join("/etc/exocortex/wyvern/clients", head.ID) && volume.Target == "/run/wyvern-link"
			if !ownSecret && !agentSocket && !wyvernLink || strings.Contains(volume.Source, "docker.sock") || strings.Contains(volume.Target, "docker.sock") {
				return errors.New("Mastermind profile requests an unowned host path")
			}
		}
		if len(mounted) != len(required) || !targets["/run/mastermind"] || name == "core" && (!targets["/run/neptune"] || !targets[filepath.Dir(e.runtime.SocketPath)]) {
			return errors.New("Mastermind profile is missing a required scoped mount")
		}
	}
	return nil
}

func (e *Engine) applyMastermind(ctx context.Context, job *model.Job, head config.HeadConfig, resolved release.Resolved, update func(string, string)) error {
	if err := validateMastermindHead(head); err != nil {
		return err
	}
	if err := release.ValidateMastermind(resolved.Manifest); err != nil {
		return err
	}
	preparation, err := e.mastermindPreparation(job.HeadID, job.RequestID, job.Version, job.PreparationID)
	if err != nil {
		return err
	}
	hash, err := manifestHash(resolved.ManifestPath)
	if err != nil || hash != preparation.ManifestSHA256 {
		return errors.New("Mastermind release changed after pre-pull preparation")
	}
	images := resolved.Manifest.Mastermind.Components
	values, err := config.ParseEnvFile(head.EnvFile)
	if err != nil {
		return err
	}
	previous := map[string]string{"core": values["MASTERMIND_CORE_IMAGE"], "runtime": values["MASTERMIND_RUNTIME_IMAGE"], "worker": values["MASTERMIND_WORKER_IMAGE"]}
	// Bootstrap pins this signed manifest digest in the protected environment.
	// A legacy Core cannot reconstruct a v2 journal without retained preimages.
	previousPath := filepath.Join(head.ProjectDir, "mastermind-release.json")
	previousHash, hashErr := manifestHash(previousPath)
	previousBody, readErr := os.ReadFile(previousPath)
	var previousManifest model.ReleaseManifest
	if hashErr != nil || readErr != nil || previousHash != values["MASTERMIND_RELEASE_SHA256"] ||
		json.Unmarshal(previousBody, &previousManifest) != nil || release.ValidateMastermind(previousManifest) != nil || previousManifest.Version != head.CurrentVersion {
		return errors.New("installed release lacks verified saved-copy rollback v2; a qualified compatibility migration is required")
	}
	for _, name := range componentNames {
		if previousManifest.Mastermind.Components[name] != previous[name] {
			return errors.New("installed components differ from the pinned previous release")
		}
	}
	// Validate the installed profile as well as the candidate before any stop/mutation.
	if err = e.validateGroupCompose(ctx, head, filepath.Join(head.ProjectDir, head.ComposeFile), previous, head.CurrentVersion); err != nil {
		return err
	}
	files, err := readDeployment(resolved.ComposePath, "mastermind")
	if err != nil {
		return err
	}
	for _, suffix := range []string{"", ".sig.json"} {
		metadata, readErr := os.ReadFile(resolved.ManifestPath + suffix)
		if readErr != nil || len(metadata) > 2*1024*1024 {
			return errors.New("verified Mastermind release metadata is missing")
		}
		files["mastermind-release.json"+suffix] = metadata
	}
	candidate := filepath.Join(head.ProjectDir, ".updater-candidate-"+job.RequestID+".yaml")
	if err = atomicDeploymentWrite(candidate, files["compose.production.yaml"]); err != nil {
		return err
	}
	defer os.Remove(candidate)
	if err = e.validateGroupCompose(ctx, head, candidate, images, job.Version); err != nil {
		return err
	}
	for _, name := range componentNames {
		// Inspect cannot silently pull after Core has entered its snapshot barrier.
		if _, err = e.runner.Run(ctx, "docker", []string{"image", "inspect", images[name]}, nil, head.ProjectDir); err != nil {
			return errors.New("a prepared Mastermind image is no longer present")
		}
		output, inspectErr := e.runner.Run(ctx, "docker", []string{"inspect", "--format", "{{.Config.Image}}", head.ID + "-" + name + "-1"}, nil, head.ProjectDir)
		if inspectErr != nil || strings.TrimSpace(string(output)) != previous[name] {
			return errors.New("installed component differs from its registered immutable image")
		}
	}
	spool, err := os.Open(job.BackupPath)
	if err != nil {
		return err
	}
	digest := sha256.New()
	size, hashErr := io.CopyBuffer(digest, spool, make([]byte, 1024*1024))
	spool.Close()
	if hashErr != nil {
		return hashErr
	}
	receipt, err := mastermindControl(ctx, head, "confirm", map[string]any{"request_id": job.RequestID, "version": job.Version,
		"sha256": hex.EncodeToString(digest.Sum(nil)), "size": size})
	if err != nil {
		return err
	}
	schema, ok := receipt["schema"].(float64)
	if receipt["held"] != true || receipt["saved_copy_protocol"] != float64(2) || receipt["request_id"] != job.RequestID || !ok || int(schema) < resolved.Manifest.Mastermind.MinimumSourceSchema || int(schema) > resolved.Manifest.Mastermind.MaximumSourceSchema {
		return errors.New("Core did not confirm the retained snapshot barrier and compatible source schema")
	}
	job.BackupSHA256, job.BackupFilename = hex.EncodeToString(digest.Sum(nil)), "mastermind-backup.zip"
	job.RollbackAvailable = true
	job.PreviousSchema = int(schema)
	job.PreviousComponents = previous
	job.PreviousImage = previous["core"]
	if immutableImageReference(previous["core"]) && immutableImageReference(previous["runtime"]) && immutableImageReference(previous["worker"]) {
		job.PreviousImagePull = previous["core"]
	}
	job.PreviousVersion = head.CurrentVersion
	job.ComponentImages = images
	job.ManifestSHA256 = hash
	job.RollbackOf = preparation.RollbackOf
	job.PreviousManifestSHA256 = values["MASTERMIND_RELEASE_SHA256"]
	job.DeploymentSnapshot = filepath.Join(e.runtime.StateDir, "deployments", job.ID, "deployment.json")
	if err = os.MkdirAll(filepath.Dir(job.DeploymentSnapshot), 0700); err != nil {
		return err
	}
	if err = snapshotDeployment(head, files, job.DeploymentSnapshot); err != nil {
		return err
	}
	if err = e.store.SaveImageGeneration(head.ID, []string{previous["core"], previous["runtime"], previous["worker"]}); err != nil {
		return errors.New("cannot preserve the previous image generation")
	}
	job.MutationStarted = true
	if err = e.store.Save(*job); err != nil {
		return err
	}
	update("APPLYING", "stopping only Core, Runtime and Worker under the retained write barrier")
	if err = e.groupCommand(ctx, head, "stop", "core", "runtime", "worker"); err != nil {
		return err
	}
	if err = applyDeployment(head, files); err != nil {
		return err
	}
	updatedEnvironment := componentEnvironment(images, job.Version)
	updatedEnvironment["MASTERMIND_RELEASE_SHA256"] = hash
	if err = setEnvValues(head.EnvFile, updatedEnvironment); err != nil {
		return err
	}
	if err = e.groupCommand(ctx, head, "run", "--rm", "--no-deps", "--entrypoint", "python", "core", "-m", "mastermind.cli", "update-migrate",
		"--request", job.RequestID, "--version", job.Version, "--schema", strconv.Itoa(resolved.Manifest.DatabaseSchema)); err != nil {
		return err
	}
	if err = e.startMastermindGroup(ctx, head); err != nil {
		return err
	}
	update("HEALTH_CHECK", "verifying Core schema, Vault open, official Runtime/Bridge and Worker")
	if err = e.checkHeadHealth(ctx, head, head.LocalHealthURL); err != nil {
		return err
	}
	result, err := mastermindControl(ctx, head, "functional", map[string]any{"request_id": job.RequestID})
	if err != nil {
		return err
	}
	group := resolved.Manifest.Mastermind
	if result["verified"] != true || result["version"] != job.Version || result["schema"] != float64(resolved.Manifest.DatabaseSchema) ||
		result["bridge_version"] != group.BridgeVersion || result["obsidian_version"] != group.ObsidianVersion || result["model_sha256"] != group.ModelSHA256 ||
		result["worker"] != true || result["vault"] != true {
		return errors.New("Mastermind functional acceptance did not match the signed component group")
	}
	if head.PublicHealthURL != "" {
		if err = e.checkHeadHealth(ctx, head, head.PublicHealthURL); err != nil {
			return err
		}
	}
	job.InstalledImage = images["core"]
	job.InstalledVersion = job.Version
	// Core releases its durable barrier only after it observes this job COMPLETED.
	return nil
}

func (e *Engine) rollbackMastermind(ctx context.Context, job *model.Job, head config.HeadConfig) error {
	if len(job.PreviousComponents) != 3 || job.PreviousVersion == "" || job.PreviousSchema < 1 {
		return errors.New("Mastermind rollback component snapshot is incomplete")
	}
	for _, component := range []string{"core", "runtime", "worker"} {
		ref := job.PreviousComponents[component]
		if err := e.ensureImageLocal(ctx, head, ref, ref); err != nil {
			return err
		}
	}
	if err := e.groupCommand(ctx, head, "stop", "core", "runtime", "worker"); err != nil {
		return err
	}
	if err := restoreDeployment(head, job.DeploymentSnapshot); err != nil {
		return err
	}
	previousEnvironment := componentEnvironment(job.PreviousComponents, job.PreviousVersion)
	previousEnvironment["MASTERMIND_RELEASE_SHA256"] = job.PreviousManifestSHA256
	if err := setEnvValues(head.EnvFile, previousEnvironment); err != nil {
		return err
	}
	// File remains beneath a root-only directory; this fixed UID receives only the sealed bind-mounted archive.
	if err := e.chownBackupFn(job.BackupPath, 10001, 10001); err != nil {
		return err
	}
	if err := e.groupCommand(ctx, head, "run", "--rm", "--no-deps", "-v", job.BackupPath+":/recovery/rollback.zip:ro", "--entrypoint", "python", "core",
		"-m", "mastermind.cli", "update-rollback", "--request", job.RequestID, "--archive", "/recovery/rollback.zip"); err != nil {
		return err
	}
	if err := e.startMastermindGroup(ctx, head); err != nil {
		return err
	}
	if err := e.checkHeadHealth(ctx, head, head.LocalHealthURL); err != nil {
		return err
	}
	result, err := mastermindControl(ctx, head, "functional", map[string]any{"request_id": job.RequestID})
	if err != nil {
		return err
	}
	if result["verified"] != true || result["version"] != job.PreviousVersion || result["schema"] != float64(job.PreviousSchema) || result["worker"] != true || result["vault"] != true {
		return errors.New("Mastermind rollback did not recover the previous functional data/components")
	}
	manifestPath := filepath.Join(head.ProjectDir, "mastermind-release.json")
	digest, err := manifestHash(manifestPath)
	if err != nil || digest != job.PreviousManifestSHA256 {
		return errors.New("rollback release metadata identity changed")
	}
	body, err := os.ReadFile(manifestPath)
	var previous model.ReleaseManifest
	if err != nil || json.Unmarshal(body, &previous) != nil || release.ValidateMastermind(previous) != nil || previous.Version != job.PreviousVersion {
		return errors.New("rollback release profile is invalid")
	}
	if result["bridge_version"] != previous.Mastermind.BridgeVersion || result["obsidian_version"] != previous.Mastermind.ObsidianVersion || result["model_sha256"] != previous.Mastermind.ModelSHA256 {
		return errors.New("rollback did not recover the exact previous Bridge, Obsidian and model")
	}
	return nil
}
