package config

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"updater/internal/model"
)

var safeID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

type Runtime struct {
	SocketPath        string
	StateDir          string
	RegistryPath      string
	DryRun            bool
	CommandTimeoutSec int
	MaxRetainedJobs   int
	RetentionDays     int
	UpdaterVersion    string
}

type HeadConfig struct {
	ID                 string
	EnvFile            string
	Service            string
	KernelURL          string
	KernelServiceToken string
	KernelCachePath    string
	ProjectDir         string
	ComposeFile        string
	ComposeService     string
	ContainerName      string
	ImageVariable      string
	VersionVariable    string
	CurrentVersion     string
	LocalHealthURL     string
	PublicHealthURL    string
	ControlToken       string
	RestoreURL         string
	RestoreField       string
}

func RuntimeFromEnv() Runtime {
	return Runtime{
		SocketPath:        value("UPDATER_SOCKET_PATH", "/run/exocortex/updater.sock"),
		StateDir:          value("UPDATER_STATE_DIR", "/var/lib/updater"),
		RegistryPath:      value("UPDATER_HEADS_FILE", "/etc/exocortex/updater-heads.json"),
		DryRun:            os.Getenv("UPDATER_DRY_RUN") == "true",
		CommandTimeoutSec: intValue("UPDATER_COMMAND_TIMEOUT_SEC", 300),
		MaxRetainedJobs:   intValue("UPDATER_MAX_RETAINED_JOBS", 20),
		RetentionDays:     intValue("UPDATER_RETENTION_DAYS", 30),
	}
}

func value(key, fallback string) string {
	if current := strings.TrimSpace(os.Getenv(key)); current != "" {
		return current
	}
	return fallback
}

func intValue(key string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func LoadRegistry(path string) (model.Registry, error) {
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return model.Registry{Heads: map[string]model.Head{}}, nil
	}
	if err != nil {
		return model.Registry{}, err
	}
	var registry model.Registry
	if err := json.Unmarshal(body, &registry); err != nil {
		return model.Registry{}, fmt.Errorf("invalid head registry: %w", err)
	}
	if registry.Heads == nil {
		registry.Heads = map[string]model.Head{}
	}
	return registry, nil
}

func SaveRegistry(path string, registry model.Registry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	body, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(body, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func RegisterHead(path, id, envFile string) error {
	if !safeID.MatchString(id) {
		return errors.New("head ID must use lowercase letters, numbers and hyphens")
	}
	resolved, err := filepath.Abs(envFile)
	if err != nil {
		return err
	}
	if info, err := os.Stat(resolved); err != nil || info.IsDir() {
		return fmt.Errorf("head environment file is unavailable: %s", resolved)
	}
	registry, err := LoadRegistry(path)
	if err != nil {
		return err
	}
	registry.Heads[id] = model.Head{ID: id, EnvFile: resolved}
	return SaveRegistry(path, registry)
}

func ParseEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, raw, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		raw = strings.Trim(strings.TrimSpace(raw), `"'`)
		if key != "" {
			values[key] = raw
		}
	}
	return values, scanner.Err()
}

func LoadHead(runtime Runtime, headID string) (HeadConfig, error) {
	registry, err := LoadRegistry(runtime.RegistryPath)
	if err != nil {
		return HeadConfig{}, err
	}
	head, ok := registry.Heads[headID]
	if !ok {
		return HeadConfig{}, fmt.Errorf("head %q is not registered on this VPS", headID)
	}
	values, err := ParseEnvFile(head.EnvFile)
	if err != nil {
		return HeadConfig{}, err
	}
	config := HeadConfig{
		ID:                 headID,
		EnvFile:            head.EnvFile,
		Service:            fallback(values["UPDATER_SERVICE_ID"], headID),
		KernelURL:          values["KERNEL_URL"],
		KernelServiceToken: values["KERNEL_SERVICE_TOKEN"],
		KernelCachePath:    fallback(values["UPDATER_KERNEL_CACHE_PATH"], filepath.Join(runtime.StateDir, "register-"+headID+".json")),
		ProjectDir:         values["UPDATER_COMPOSE_PROJECT_DIR"],
		ComposeFile:        fallback(values["UPDATER_COMPOSE_FILE"], "compose.yaml"),
		ComposeService:     values["UPDATER_COMPOSE_SERVICE"],
		ContainerName:      values["UPDATER_CONTAINER_NAME"],
		ImageVariable:      values["UPDATER_IMAGE_VARIABLE"],
		VersionVariable:    values["UPDATER_VERSION_VARIABLE"],
		LocalHealthURL:     values["UPDATER_LOCAL_HEALTH_URL"],
		PublicHealthURL:    values["UPDATER_PUBLIC_HEALTH_URL"],
		ControlToken:       values["UPDATER_CONTROL_TOKEN"],
		RestoreURL:         values["UPDATER_RESTORE_URL"],
		RestoreField:       fallback(values["UPDATER_RESTORE_FIELD"], "file"),
	}
	if config.KernelURL == "" || config.KernelServiceToken == "" {
		return HeadConfig{}, errors.New("head environment must define KERNEL_URL and KERNEL_SERVICE_TOKEN")
	}
	for key, current := range map[string]string{
		"UPDATER_COMPOSE_PROJECT_DIR": config.ProjectDir,
		"UPDATER_COMPOSE_SERVICE":     config.ComposeService,
		"UPDATER_CONTAINER_NAME":      config.ContainerName,
		"UPDATER_IMAGE_VARIABLE":      config.ImageVariable,
		"UPDATER_VERSION_VARIABLE":    config.VersionVariable,
		"UPDATER_LOCAL_HEALTH_URL":    config.LocalHealthURL,
		"UPDATER_CONTROL_TOKEN":       config.ControlToken,
	} {
		if strings.TrimSpace(current) == "" {
			return HeadConfig{}, fmt.Errorf("head environment is missing %s", key)
		}
	}
	config.CurrentVersion = strings.TrimSpace(values[config.VersionVariable])
	if config.CurrentVersion == "" {
		return HeadConfig{}, fmt.Errorf(
			"head environment is missing the current version in %s",
			config.VersionVariable,
		)
	}
	return config, nil
}

func fallback(value, replacement string) string {
	if strings.TrimSpace(value) == "" {
		return replacement
	}
	return strings.TrimSpace(value)
}
