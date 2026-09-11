package hostrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"updater/internal/config"
)

func reconnectHeads() error {
	runtime := config.RuntimeFromEnv()
	registry, err := config.LoadRegistry(runtime.RegistryPath)
	if err != nil {
		return err
	}
	for id := range registry.Heads {
		head, err := config.LoadHead(runtime, id)
		if err != nil {
			return err
		}
		output, err := exec.Command("docker", "inspect", "--format", "{{.State.Running}}", head.ContainerName).Output()
		if err != nil || strings.TrimSpace(string(output)) != "true" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		command := exec.CommandContext(ctx, "docker", "compose", "--env-file", head.EnvFile, "-f", filepath.Join(head.ProjectDir, head.ComposeFile), "up", "-d", "--no-deps", "--force-recreate", head.ComposeService)
		command.Dir = head.ProjectDir
		err = command.Run()
		cancel()
		if err != nil {
			return fmt.Errorf("reconnect restored helper credentials for head %s failed", id)
		}
	}
	return nil
}

const activeHelpersFile = "/var/lib/updater/recovery-active-helpers.json"

func PendingHostRecovery() bool {
	_, err := os.Stat(activeHelpersFile)
	return PendingRecovery("/") || err == nil
}
func ResumeInterruptedHost() error {
	running := runningHelpers()
	if body, err := os.ReadFile(activeHelpersFile); err == nil {
		var saved []string
		if len(body) > 1024 || json.Unmarshal(body, &saved) != nil {
			return errors.New("invalid helper restart journal")
		}
		for _, unit := range saved {
			if unit != "neptune.service" && unit != "gryphon.service" {
				return errors.New("invalid helper restart unit")
			}
			found := false
			for _, current := range running {
				if current == unit {
					found = true
				}
			}
			if !found {
				running = append(running, unit)
			}
		}
	}
	if err := units("stop", running); err != nil {
		return err
	}
	if err := RecoverInterrupted("/"); err != nil {
		return err
	}
	if err := errors.Join(units("start", running), reconnectHeads()); err != nil {
		return err
	}
	if err := os.Remove(activeHelpersFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func runningHelpers() []string {
	result := []string{}
	for _, unit := range []string{"neptune.service", "gryphon.service"} {
		if exec.Command("systemctl", "is-active", "--quiet", unit).Run() == nil {
			result = append(result, unit)
		}
	}
	return result
}
func units(action string, names []string) error {
	for _, unit := range names {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		err := exec.CommandContext(ctx, "systemctl", action, unit).Run()
		cancel()
		if err != nil {
			return fmt.Errorf("helper %s failed: %s", action, unit)
		}
	}
	return nil
}
func Export(password string) (archive []byte, err error) {
	if PendingHostRecovery() {
		return nil, errors.New("an interrupted host recovery must be resumed first")
	}
	running := runningHelpers()
	body, _ := json.Marshal(running)
	if err = durableFile(activeHelpersFile, body); err != nil {
		return nil, err
	}
	if err = syncDirectory(filepath.Dir(activeHelpersFile)); err != nil {
		return nil, err
	}
	defer func() {
		restartErr := units("start", running)
		err = errors.Join(err, restartErr)
		if restartErr == nil {
			err = errors.Join(err, os.Remove(activeHelpersFile))
		}
	}()
	if err := units("stop", running); err != nil {
		return nil, err
	}
	entries, err := Collect("/")
	if err != nil {
		return nil, err
	}
	defer func() {
		for _, entry := range entries {
			clear(entry.Data)
		}
	}()
	return Seal(entries, password)
}
func ownership() error {
	for _, root := range roots {
		if _, err := os.Stat("/" + root); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		owner, group := "root", "root"
		switch {
		case strings.HasPrefix(root, "var/lib/neptune"):
			owner, group = "neptune", "neptune"
		case strings.HasPrefix(root, "var/lib/gryphon"):
			owner, group = "gryphon", "gryphon-clients"
		case strings.HasPrefix(root, "etc/neptune"):
			group = "neptune"
		case strings.HasPrefix(root, "etc/gryphon"):
			group = "gryphon-clients"
		}
		account, err := user.Lookup(owner)
		if err != nil {
			return err
		}
		userGroup, err := user.LookupGroup(group)
		if err != nil {
			return err
		}
		uid, _ := strconv.Atoi(account.Uid)
		gid, _ := strconv.Atoi(userGroup.Gid)
		err = filepath.WalkDir("/"+root, func(filename string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("unexpected recovery symlink")
			}
			currentGID := gid
			if strings.HasPrefix(filename, "/etc/neptune/clients") {
				clientGroup, err := user.LookupGroup("neptune-clients")
				if err != nil {
					return err
				}
				currentGID, _ = strconv.Atoi(clientGroup.Gid)
			}
			if err := os.Chown(filename, uid, currentGID); err != nil {
				return err
			}
			mode := os.FileMode(0600)
			if entry.IsDir() {
				mode = 0700
			}
			if strings.HasPrefix(filename, "/etc/") {
				mode = 0640
				if entry.IsDir() {
					mode = 0750
				}
			}
			return os.Chmod(filename, mode)
		})
		if err != nil {
			return err
		}
	}
	return nil
}
func health(unit string) error {
	socket, host := "/run/neptune/neptuned.sock", "neptune.local"
	if unit == "gryphon.service" {
		socket, host = "/run/gryphon/client.sock", "gryphon.local"
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	for attempt := 0; attempt < 30; attempt++ {
		response, err := client.Get("http://" + host + "/v1/health")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("restored helper health check failed: %s", unit)
}

// Restore is called from a supervisor outside updater.service, after trusted
// installers provision binaries, identities and release trust on the new host.
func Restore(archive []byte, password string) error {
	entries, err := Open(archive, password)
	if err != nil {
		return err
	}
	defer func() {
		for _, entry := range entries {
			clear(entry.Data)
		}
	}()
	wanted := []string{}
	for _, unit := range []string{"neptune", "gryphon"} {
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name, "var/lib/"+unit+"/") {
				if _, err := os.Stat("/etc/systemd/system/" + unit + ".service"); err != nil {
					return fmt.Errorf("install trusted %s binaries before recovery", unit)
				}
				wanted = append(wanted, unit+".service")
				break
			}
		}
	}
	running := runningHelpers()
	if PendingHostRecovery() {
		return errors.New("an interrupted host recovery must be resumed first")
	}
	restartBody, _ := json.Marshal(running)
	if err := durableFile(activeHelpersFile, restartBody); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(activeHelpersFile)); err != nil {
		return err
	}
	defer func() {
		if !PendingRecovery("/") {
			_ = os.Remove(activeHelpersFile)
		}
	}()
	if err := units("stop", running); err != nil {
		_ = units("start", running)
		return err
	}
	if err := units("stop", []string{"updater.service"}); err != nil {
		_ = units("start", running)
		return err
	}
	defer func() {
		if !PendingRecovery("/") {
			_ = os.Remove(activeHelpersFile)
		}
		_ = units("start", []string{"updater.service"})
	}()
	err = Apply("/", entries, func() error {
		if err := ownership(); err != nil {
			return err
		}
		if err := units("start", wanted); err != nil {
			_ = units("stop", wanted)
			return err
		}
		for _, unit := range wanted {
			if err := health(unit); err != nil {
				_ = units("stop", wanted)
				return err
			}
		}
		if err := reconnectHeads(); err != nil {
			_ = units("stop", wanted)
			return err
		}
		return nil
	})
	if err != nil {
		_ = ownership()
		_ = units("start", running)
		_ = reconnectHeads()
		return err
	}
	return nil
}
