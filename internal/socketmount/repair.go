package socketmount

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"updater/internal/config"
)

type containerMount struct {
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
}

type containerState struct {
	State struct {
		PID int `json:"Pid"`
	} `json:"State"`
	Mounts []containerMount `json:"Mounts"`
}

type Report struct {
	Checked   []string
	Recreated []string
	Warnings  []string
}

type Repairer struct {
	runtime          config.Runtime
	inspectContainer func(context.Context, string) (containerState, error)
	processMountPath func(int, string) string
	recreate         func(context.Context, config.HeadConfig) error
}

func New(runtime config.Runtime) *Repairer {
	repairer := &Repairer{runtime: runtime}
	repairer.inspectContainer = inspectContainer
	repairer.processMountPath = processMountPath
	repairer.recreate = recreate
	return repairer
}

// Repair recreates only running head containers whose updater socket directory
// is bound to an inode that is no longer reachable through the host path. This
// can happen when an older updater.service removes and recreates its systemd
// RuntimeDirectory while Docker keeps the original bind mount alive.
func (r *Repairer) Repair(ctx context.Context) Report {
	report := Report{}
	hostDirectory := filepath.Clean(filepath.Dir(r.runtime.SocketPath))
	hostInfo, err := os.Stat(hostDirectory)
	if err != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("stat updater socket directory: %v", err))
		return report
	}
	registry, err := config.LoadRegistry(r.runtime.RegistryPath)
	if err != nil {
		report.Warnings = append(report.Warnings, fmt.Sprintf("load head registry: %v", err))
		return report
	}
	ids := make([]string, 0, len(registry.Heads))
	for id := range registry.Heads {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		head, loadErr := config.LoadHead(r.runtime, id)
		if loadErr != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("head %s: %v", id, loadErr))
			continue
		}
		container, inspectErr := r.inspectContainer(ctx, head.ContainerName)
		if inspectErr != nil || container.State.PID <= 0 {
			// A registered head may not have been started yet during its initial
			// installation. Its first start will bind the current directory.
			continue
		}
		destination := mountDestination(container.Mounts, hostDirectory, hostInfo)
		if destination == "" {
			continue
		}
		report.Checked = append(report.Checked, id)
		mountedInfo, statErr := os.Stat(r.processMountPath(container.State.PID, destination))
		if statErr == nil && os.SameFile(hostInfo, mountedInfo) {
			continue
		}
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			report.Warnings = append(report.Warnings, fmt.Sprintf("head %s: inspect mounted socket directory: %v", id, statErr))
			continue
		}
		if recreateErr := r.recreate(ctx, head); recreateErr != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("head %s: %v", id, recreateErr))
			continue
		}
		report.Recreated = append(report.Recreated, id)
	}
	return report
}

func mountDestination(mounts []containerMount, hostDirectory string, hostInfo os.FileInfo) string {
	for _, mount := range mounts {
		if filepath.Clean(mount.Source) == hostDirectory {
			return mount.Destination
		}
		sourceInfo, err := os.Stat(mount.Source)
		if err == nil && os.SameFile(hostInfo, sourceInfo) {
			return mount.Destination
		}
	}
	return ""
}

func inspectContainer(ctx context.Context, name string) (containerState, error) {
	output, err := exec.CommandContext(ctx, "docker", "inspect", name).CombinedOutput()
	if err != nil {
		return containerState{}, fmt.Errorf("inspect container %s: %s", name, strings.TrimSpace(string(output)))
	}
	var containers []containerState
	if err := json.Unmarshal(output, &containers); err != nil {
		return containerState{}, fmt.Errorf("decode container %s inspection: %w", name, err)
	}
	if len(containers) != 1 {
		return containerState{}, fmt.Errorf("inspect container %s returned %d records", name, len(containers))
	}
	return containers[0], nil
}

func processMountPath(pid int, destination string) string {
	cleaned := strings.TrimPrefix(path.Clean(destination), "/")
	return filepath.Join("/proc", strconv.Itoa(pid), "root", filepath.FromSlash(cleaned))
}

func recreate(ctx context.Context, head config.HeadConfig) error {
	arguments := []string{
		"compose", "--env-file", head.EnvFile,
		"-f", filepath.Join(head.ProjectDir, head.ComposeFile),
		"up", "-d", "--no-deps", "--force-recreate", head.ComposeService,
	}
	command := exec.CommandContext(ctx, "docker", arguments...)
	command.Dir = head.ProjectDir
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("recreate stale updater socket mount: %s", strings.TrimSpace(string(output)))
	}
	return nil
}
