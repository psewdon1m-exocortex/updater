package component

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func validatePreservedRuntimeUnit(path, runtimeDirectory string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 1024*1024 {
		return errors.New("helper systemd unit is not a bounded regular file")
	}
	section, directories, preserve := "", []string{}, ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line
			continue
		}
		if section != "[Service]" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "RuntimeDirectory":
			directories = strings.Fields(value)
		case "RuntimeDirectoryPreserve":
			preserve = strings.TrimSpace(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	found := false
	for _, directory := range directories {
		if directory == runtimeDirectory {
			found = true
			break
		}
	}
	if !found || preserve != "yes" {
		return fmt.Errorf("helper systemd unit must preserve RuntimeDirectory=%s", runtimeDirectory)
	}
	return nil
}

func managedSystemdUnit(linkPath, expectedTarget string) (string, error) {
	info, err := os.Lstat(linkPath)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", fmt.Errorf("helper unit %s is not managed by Updater; rerun the verified Updater installer", linkPath)
	}
	resolved, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		return "", err
	}
	if filepath.Clean(resolved) != filepath.Clean(expectedTarget) {
		return "", fmt.Errorf("helper unit %s resolves outside the Updater-managed unit directory", linkPath)
	}
	targetInfo, err := os.Stat(expectedTarget)
	if err != nil {
		return "", err
	}
	if !targetInfo.Mode().IsRegular() {
		return "", errors.New("Updater-managed helper unit is not a regular file")
	}
	return expectedTarget, nil
}

func daemonReload(ctx context.Context) error {
	if output, err := exec.CommandContext(ctx, "systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemd daemon-reload failed: %s", strings.TrimSpace(string(output)))
	}
	return nil
}
