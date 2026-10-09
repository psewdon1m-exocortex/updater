package hostrecovery

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
	"updater/internal/config"
	"updater/internal/kernel"
)

func ResolveOrigin(runtime config.Runtime, override string) (string, error) {
	if override != "" {
		return validatedOrigin(override)
	}
	cfg, err := config.LoadHost(runtime)
	if err != nil {
		return "", err
	}
	if cfg.KernelURL != "" && cfg.KernelTokenFile != "" {
		token, err := config.HostKernelToken(cfg)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err == nil {
			snapshot, err := kernel.LoadLive(cfg.KernelURL, token, filepath.Join(runtime.StateDir, "register-host.json"), 5*time.Second, "services.saturn.sni", "services.saturn.port")
			if err == nil {
				sni, err := kernel.String(snapshot, "services.saturn.sni")
				if err != nil {
					return "", err
				}
				port, err := kernel.String(snapshot, "services.saturn.port")
				if err != nil {
					return "", err
				}
				value, err := strconv.Atoi(port)
				if err != nil || value < 1 || value > 65535 {
					return "", errors.New("invalid Saturn port in Kernel Register")
				}
				host := sni
				if value != 443 {
					host = net.JoinHostPort(sni, port)
				}
				return validatedOrigin("https://" + host)
			}
			if !errors.Is(err, kernel.ErrUnavailable) {
				return "", fmt.Errorf("Saturn resolution denied or invalid: %w", err)
			}
		}
	}
	if cfg.RecoveryGatewayURL != "" {
		return validatedOrigin(cfg.RecoveryGatewayURL)
	}
	return "", errors.New("Saturn origin is unavailable; configure Updater's Kernel connection or use the advanced recovery origin override")
}

func validatedOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("Saturn address must be an HTTPS origin")
	}
	u.Path = ""
	return u.String(), nil
}
