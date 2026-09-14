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

type watchedDirectory struct {
	Name     string
	Path     string
	Required bool
}

type Repairer struct {
	runtime          config.Runtime
	directories      []watchedDirectory
	inspectContainer func(context.Context, string) (containerState, error)
	processMountPath func(int, string) string
	recreate         func(context.Context, config.HeadConfig) error
}

func New(runtime config.Runtime) *Repairer {
	repairer := &Repairer{
		runtime: runtime,
		directories: []watchedDirectory{
			{Name: "updater", Path: filepath.Clean(filepath.Dir(runtime.SocketPath)), Required: true},
			{Name: "neptune", Path: "/run/neptune"},
			{Name: "gryphon", Path: "/run/gryphon"},
		},
	}
	repairer.inspectContainer = inspectContainer
	repairer.processMountPath = processMountPath
	repairer.recreate = recreate
	return repairer
}

// Repair recreates only running head containers whose Updater, Neptune or
// Gryphon socket directory is bound to an inode that is no longer reachable
// through the current host path. RuntimeDirectoryPreserve prevents this during
// normal upgrades; this reconciliation also repairs legacy units, manual
// stop/start cycles and interrupted host recovery.
func (r *Repairer) Repair(ctx context.Context) Report {
	report := Report{}
	type availableDirectory struct {
		name string
		path string
		info os.FileInfo
	}
	available := make([]availableDirectory, 0, len(r.directories))
	for _, watched := range r.directories {
		hostDirectory := filepath.Clean(watched.Path)
		hostInfo, err := os.Stat(hostDirectory)
		if errors.Is(err, os.ErrNotExist) && !watched.Required {
			continue
		}
		if err != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("stat %s socket directory: %v", watched.Name, err))
			continue
		}
		available = append(available, availableDirectory{name: watched.Name, path: hostDirectory, info: hostInfo})
	}
	if len(available) == 0 {
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
		checked := false
		stale := []string{}
		for _, directory := range available {
			destination := mountDestination(container.Mounts, directory.path, directory.info)
			if destination == "" {
				continue
			}
			checked = true
			mountedInfo, statErr := os.Stat(r.processMountPath(container.State.PID, destination))
			if statErr == nil && os.SameFile(directory.info, mountedInfo) {
				continue
			}
			if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
				report.Warnings = append(report.Warnings, fmt.Sprintf("head %s: inspect mounted %s socket directory: %v", id, directory.name, statErr))
				continue
			}
			stale = append(stale, directory.name)
		}
		if checked {
			report.Checked = append(report.Checked, id)
		}
		if len(stale) == 0 {
			continue
		}
		if recreateErr := r.recreate(ctx, head); recreateErr != nil {
			report.Warnings = append(report.Warnings, fmt.Sprintf("head %s: repair stale %s socket mount: %v", id, strings.Join(stale, ", "), recreateErr))
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
		return fmt.Errorf("recreate service: %s", strings.TrimSpace(string(output)))
	}
	return nil
}
