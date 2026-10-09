package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

func LocalHostID() (string, error) {
	body, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	return "host-" + hex.EncodeToString(digest[:12]), nil
}

var hostIdentifier = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// HostConfig is owned by the host, never by a registered application head.
// The Kernel machine token is kept in a separate root-owned file.
type HostConfig struct {
	KernelURL          string            `json:"kernel_url,omitempty"`
	KernelTokenFile    string            `json:"kernel_token_file,omitempty"`
	HostID             string            `json:"host_id,omitempty"`
	ReleaseSources     map[string]string `json:"release_sources,omitempty"`
	RecoveryGatewayURL string            `json:"recovery_gateway_url,omitempty"`
	RecoveryTokenFiles map[string]string `json:"recovery_token_files,omitempty"`
	RecoverySlugs      map[string]string `json:"recovery_slugs,omitempty"`
}

func HostConfigFile(runtime Runtime) string {
	if runtime.HostConfigPath != "" {
		return runtime.HostConfigPath
	}
	return filepath.Join(filepath.Dir(runtime.RegistryPath), "updater-host.json")
}

func validHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

func validateHost(cfg HostConfig) error {
	if cfg.KernelURL != "" && !validHTTPS(cfg.KernelURL) {
		return errors.New("Kernel URL must be HTTPS without userinfo, query or fragment")
	}
	if cfg.HostID != "" && !hostIdentifier.MatchString(cfg.HostID) {
		return errors.New("host ID must use lowercase letters, numbers, hyphens or underscores")
	}
	if cfg.KernelTokenFile != "" && !filepath.IsAbs(cfg.KernelTokenFile) {
		return errors.New("Kernel token file must have an absolute path")
	}
	if cfg.RecoveryGatewayURL != "" && !validHTTPS(cfg.RecoveryGatewayURL) {
		return errors.New("recovery Gateway URL must be HTTPS without userinfo, query or fragment")
	}
	for service, tokenFile := range cfg.RecoveryTokenFiles {
		if service != "updater" && service != "neptune" && service != "gryphon" && service != "wyvern" {
			return fmt.Errorf("unknown recovery service %q", service)
		}
		if !filepath.IsAbs(tokenFile) {
			return fmt.Errorf("recovery token file for %s must be absolute", service)
		}
	}
	for service, slug := range cfg.RecoverySlugs {
		if service != "updater" && service != "neptune" && service != "gryphon" && service != "wyvern" {
			return fmt.Errorf("unknown recovery service %q", service)
		}
		if !hostIdentifier.MatchString(slug) {
			return fmt.Errorf("recovery producer slug for %s is invalid", service)
		}
	}
	for component, raw := range cfg.ReleaseSources {
		if component != "updater" && component != "neptune" && component != "gryphon" && component != "wyvern" && component != "window" {
			return fmt.Errorf("unknown host component %q", component)
		}
		if !validHTTPS(raw) {
			return fmt.Errorf("release source for %s must be HTTPS", component)
		}
	}
	return nil
}

func LoadHost(runtime Runtime) (HostConfig, error) {
	body, err := os.ReadFile(HostConfigFile(runtime))
	if errors.Is(err, os.ErrNotExist) {
		return HostConfig{ReleaseSources: map[string]string{}}, nil
	}
	if err != nil {
		return HostConfig{}, err
	}
	var cfg HostConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		return HostConfig{}, fmt.Errorf("invalid host configuration: %w", err)
	}
	if cfg.ReleaseSources == nil {
		cfg.ReleaseSources = map[string]string{}
	}
	if cfg.RecoveryTokenFiles == nil {
		cfg.RecoveryTokenFiles = map[string]string{}
	}
	if cfg.RecoverySlugs == nil {
		cfg.RecoverySlugs = map[string]string{}
	}
	return cfg, validateHost(cfg)
}

func SaveHost(runtime Runtime, cfg HostConfig) error {
	if err := validateHost(cfg); err != nil {
		return err
	}
	path := HostConfigFile(runtime)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	staged, err := os.CreateTemp(filepath.Dir(path), ".updater-host-*")
	if err != nil {
		return err
	}
	defer os.Remove(staged.Name())
	defer staged.Close()
	if err := staged.Chmod(0o600); err != nil {
		return err
	}
	if _, err := staged.Write(append(body, '\n')); err != nil {
		return err
	}
	if err := staged.Sync(); err != nil {
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	return os.Rename(staged.Name(), path)
}

func HostKernelToken(cfg HostConfig) (string, error) {
	if cfg.KernelTokenFile == "" {
		return "", errors.New("Updater Kernel machine token is not configured")
	}
	info, err := os.Lstat(cfg.KernelTokenFile)
	if err != nil {
		return "", err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || owner.Uid != 0 || info.Mode().Perm()&0o037 != 0 {
		return "", errors.New("Updater Kernel machine token file must be root-owned, regular and private")
	}
	body, err := os.ReadFile(cfg.KernelTokenFile)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(body))
	if token == "" {
		return "", errors.New("Updater Kernel machine token is empty")
	}
	return token, nil
}
