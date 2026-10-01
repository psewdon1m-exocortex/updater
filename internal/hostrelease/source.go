package hostrelease

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"updater/internal/config"
	"updater/internal/kernel"
)

type Source struct {
	Component  string `json:"component"`
	Repository string `json:"repository"`
	Origin     string `json:"origin"`
	Reason     string `json:"reason,omitempty"`
}

func Resolve(runtime config.Runtime, component string) (Source, error) {
	if component != "updater" && component != "neptune" && component != "gryphon" && component != "wyvern" && component != "window" {
		return Source{}, fmt.Errorf("unknown host component %q", component)
	}
	cfg, err := config.LoadHost(runtime)
	if err != nil {
		return Source{}, err
	}
	reason := "Updater Kernel connection is not configured"
	if cfg.KernelURL != "" && cfg.KernelTokenFile != "" {
		token, err := config.HostKernelToken(cfg)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return Source{}, err
			}
			reason = "Updater Kernel machine token file is unavailable"
		} else {
			cache := filepath.Join(runtime.StateDir, "register-host.json")
			snapshot, err := kernel.LoadLive(cfg.KernelURL, token, cache, 5*time.Second, "repositories."+component+".url")
			if err == nil {
				repository, err := kernel.String(snapshot, "repositories."+component+".url")
				if err != nil {
					return Source{}, err
				}
				if err := validateRepository(repository); err != nil {
					return Source{}, err
				}
				return Source{Component: component, Repository: repository, Origin: "kernel"}, nil
			}
			if !errors.Is(err, kernel.ErrUnavailable) {
				return Source{}, err
			}
			reason = err.Error()
		}
	}
	repository := cfg.ReleaseSources[component]
	if repository == "" {
		return Source{}, fmt.Errorf("%s release source is unavailable: %s; configure its HTTPS repository in updater tui", component, reason)
	}
	if err := validateRepository(repository); err != nil {
		return Source{}, err
	}
	return Source{Component: component, Repository: repository, Origin: "tui-fallback", Reason: reason}, nil
}

// ResolveWindowBootstrap is only for the root CLI's exact-version Window
// installation. Its intended caller is Window's signed bootstrap, which seeds
// this host-owned URL. Updater independently verifies the selected signed
// release. Ordinary discovery and TUI updates still use Resolve and fail
// closed on an incomplete reachable Kernel Register.
func ResolveWindowBootstrap(runtime config.Runtime) (Source, error) {
	cfg, err := config.LoadHost(runtime)
	if err != nil {
		return Source{}, err
	}
	repository := cfg.ReleaseSources["window"]
	if repository == "" {
		return Source{}, errors.New("Window exact-version bootstrap source is absent; run the signed Window bootstrap first")
	}
	if err := validateRepository(repository); err != nil {
		return Source{}, err
	}
	return Source{Component: "window", Repository: repository, Origin: "exact-bootstrap"}, nil
}

func validateRepository(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSpace(u.Path) == "" {
		return errors.New("host release source must identify an HTTPS repository")
	}
	return nil
}
