package component

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"updater/internal/console"
	"updater/internal/release"
)

const wyvernUnitPath = "/etc/exocortex/units/wyvern.service"
const wyvernCurrent = "/var/lib/updater/components/wyvern/current.json"
const wyvernJournal = "/var/lib/updater/components/wyvern/transaction.json"
const wyvernMaintenance = "/etc/wyvern/maintenance"

type WyvernManifest struct {
	Schema       string   `json:"schema"`
	Product      string   `json:"product"`
	Version      string   `json:"version"`
	Image        string   `json:"image"`
	APIVersion   int      `json:"api_version"`
	ConfigSchema string   `json:"config_schema"`
	Capabilities []string `json:"capabilities"`
	SourceSHA    string   `json:"source_sha,omitempty"`
	Installer    *struct {
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	} `json:"installer,omitempty"`
}

func ParseWyvernManifest(body []byte) (WyvernManifest, error) {
	var manifest WyvernManifest
	if len(body) > 65536 {
		return manifest, errors.New("Wyvern manifest exceeds its limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || decoder.Decode(new(any)) != io.EOF || manifest.Schema != "exocortex.wyvern.release.v1" || manifest.Product != "wyvern" || !release.Stable(manifest.Version) || manifest.APIVersion != 1 || manifest.ConfigSchema != "exocortex.wyvern.config.v1" ||
		!regexp.MustCompile(`^ghcr\.io/[a-z0-9_.-]+/[a-z0-9_.-]+@sha256:[a-f0-9]{64}$`).MatchString(manifest.Image) || len(manifest.Capabilities) < 4 || len(manifest.Capabilities) > 32 {
		return manifest, errors.New("Wyvern release manifest is incompatible or invalid")
	}
	seen := map[string]bool{}
	for _, cap := range manifest.Capabilities {
		if !regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`).MatchString(cap) || seen[cap] {
			return manifest, errors.New("Invalid Wyvern capabilities")
		}
		seen[cap] = true
	}
	for _, required := range []string{"text", "streaming", "structured_output", "token_count"} {
		if !seen[required] {
			return manifest, errors.New("Wyvern release lacks baseline capabilities")
		}
	}
	return manifest, nil
}

type wyvernTransaction struct {
	Phase        string          `json:"phase"`
	Previous     *WyvernManifest `json:"previous"`
	Candidate    WyvernManifest  `json:"candidate"`
	WasReady     bool            `json:"was_ready"`
	WasDraining  bool            `json:"was_draining"`
	PreviousUnit string          `json:"previous_unit,omitempty"`
}
type WyvernDeploymentError struct {
	RolledBack     bool
	RollbackFailed bool
}

func (e WyvernDeploymentError) Error() string {
	if e.RollbackFailed {
		return "Wyvern activation and rollback failed; repair is required"
	}
	if e.RolledBack {
		return "Wyvern activation failed; the previous runtime was restored"
	}
	return "Wyvern installation failed; inspect the managed component"
}

type WyvernDeployment struct {
	Root         string
	Run          func(context.Context, string, ...string) ([]byte, error)
	Read         func(context.Context) (console.WyvernView, error)
	Control      func(context.Context, string) (console.WyvernStatus, error)
	Poll         time.Duration
	DrainTimeout time.Duration
}

func (d WyvernDeployment) path(value string) string {
	if d.Root == "" {
		return value
	}
	return filepath.Join(d.Root, strings.TrimPrefix(value, "/"))
}
func (d WyvernDeployment) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if d.Run != nil {
		return d.Run(ctx, name, args...)
	}
	result, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("Wyvern %s operation failed", name)
	}
	return result, nil
}
func (d WyvernDeployment) read(ctx context.Context) (console.WyvernView, error) {
	if d.Read != nil {
		return d.Read(ctx)
	}
	return console.ReadWyvern(ctx, console.WyvernAdminSocket)
}
func (d WyvernDeployment) control(ctx context.Context, kind string) (console.WyvernStatus, error) {
	if d.Control != nil {
		return d.Control(ctx, kind)
	}
	return console.ControlWyvern(ctx, console.WyvernAdminSocket, kind)
}
func (d WyvernDeployment) installed() (*WyvernManifest, error) {
	body, err := os.ReadFile(d.path(wyvernCurrent))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	manifest, err := ParseWyvernManifest(body)
	return &manifest, err
}
func (d WyvernDeployment) writeJournal(transaction wyvernTransaction) error {
	return atomicWyvernFile(d.path(wyvernJournal), transaction, -1)
}
func (d WyvernDeployment) activate(ctx context.Context, manifest WyvernManifest, ready bool, paused bool) error {
	if err := atomicWyvernBytes(d.path("/etc/wyvern/wyvern.env"), []byte("WYVERN_IMAGE="+manifest.Image+"\nWYVERN_VERSION="+manifest.Version+"\n"), -1); err != nil {
		return err
	}
	if _, err := d.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if _, err := d.run(ctx, "systemctl", "enable", "wyvern.service"); err != nil {
		return err
	}
	if _, err := d.run(ctx, "systemctl", "restart", "wyvern.service"); err != nil {
		return err
	}
	poll := d.Poll
	if poll <= 0 {
		poll = 250 * time.Millisecond
	}
	for attempt := 0; attempt < 40; attempt++ {
		view, err := d.read(ctx)
		if err == nil && view.Status.Version == manifest.Version && view.Status.APIVersion == 1 && (!ready || view.Status.ConfigurationLoaded) {
			if err := os.Remove(d.path(wyvernMaintenance)); err != nil && !os.IsNotExist(err) {
				return err
			}
			kind := "resume"
			if paused {
				kind = "drain"
			}
			status, err := d.control(ctx, kind)
			if err == nil && status.Version == manifest.Version && (!ready || paused || status.Ready) {
				return nil
			}
			if err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
	return errors.New("Wyvern did not verify the selected version and configuration")
}
func (d WyvernDeployment) Prepare(ctx context.Context) error {
	for _, dir := range []string{"/etc/wyvern", "/run/wyvern", "/run/wyvern-admin", "/var/lib/wyvern"} {
		mode := os.FileMode(0750)
		if dir == "/etc/wyvern" {
			mode = 0755
		}
		if dir == "/var/lib/wyvern" {
			mode = 0700
		}
		if info, err := os.Lstat(d.path(dir)); err == nil && !info.IsDir() {
			return errors.New("Wyvern directory is not a real directory")
		}
		if err := os.MkdirAll(d.path(dir), mode); err != nil {
			return err
		}
		if err := os.Chmod(d.path(dir), mode); err != nil {
			return err
		}
		if d.Root == "" && dir != "/etc/wyvern" {
			if err := os.Chown(dir, 10001, 10001); err != nil {
				return err
			}
		}
	}
	for _, socket := range []string{"/run/wyvern/client.sock", "/run/wyvern-admin/admin.sock"} {
		filename := d.path(socket)
		info, err := os.Lstat(filename)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("Wyvern socket path contains another object")
		}
		connection, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", filename)
		if err == nil {
			_ = connection.Close()
			return errors.New("Wyvern endpoint is still active")
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			return errors.New("Wyvern socket could not be proven stale")
		}
		if err := os.Remove(filename); err != nil {
			return err
		}
	}
	return nil
}
func (d WyvernDeployment) Apply(ctx context.Context, candidate WyvernManifest) (result error) {
	encoded, _ := json.Marshal(candidate)
	if _, err := ParseWyvernManifest(encoded); err != nil {
		return err
	}
	previous, err := d.installed()
	if err != nil {
		return err
	}
	if body, err := os.ReadFile(d.path(wyvernJournal)); err == nil {
		var pending wyvernTransaction
		if json.Unmarshal(body, &pending) != nil || (pending.Phase != "completed" && pending.Phase != "rolled_back") {
			return errors.New("An interrupted Wyvern transaction requires repair before another update")
		}
	}
	transaction := wyvernTransaction{Phase: "prepared", Previous: previous, Candidate: candidate}
	if previous != nil {
		view, err := d.read(ctx)
		if err != nil {
			return errors.New("Installed Wyvern is unavailable; repair it before updating")
		}
		if view.Status.Version != previous.Version {
			return errors.New("Installed Wyvern version does not match its managed manifest")
		}
		transaction.WasReady, transaction.WasDraining = view.Status.Ready, view.Status.Drain
		caps := map[string]bool{}
		for _, cap := range candidate.Capabilities {
			caps[cap] = true
		}
		for _, adapter := range view.Catalog.Adapters {
			for _, profile := range adapter.Profiles {
				for _, cap := range profile.Capabilities {
					if !caps[cap] {
						return errors.New("Candidate cannot serve all configured Adapter profiles")
					}
				}
			}
		}
		if view.Status.ConfigurationLoaded {
			if _, err := d.control(ctx, "reload"); err != nil {
				return errors.New("Fresh Kernel/Volt configuration is required before a runtime update")
			}
		}
	}
	if _, err := d.run(ctx, "docker", "pull", candidate.Image); err != nil {
		return err
	}
	version, err := d.run(ctx, "docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", candidate.Image, "version", "--json")
	var identity struct {
		Service    string `json:"service"`
		Version    string `json:"version"`
		APIVersion int    `json:"api_version"`
	}
	if err != nil || json.Unmarshal(version, &identity) != nil || identity.Service != "wyvern" || identity.Version != candidate.Version || identity.APIVersion != 1 {
		return errors.New("Verified image does not report the selected Wyvern identity")
	}
	if previous == nil {
		if _, err := os.Lstat(d.path("/etc/systemd/system/wyvern.service")); err == nil {
			return errors.New("An existing Wyvern unit is not registered with Updater")
		}
		output, err := d.run(ctx, "docker", "container", "ls", "--all", "--filter", "name=^exocortex-wyvern$", "--format", "{{.ID}}")
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(output)) > 0 {
			return errors.New("A Wyvern container already exists outside the managed installation")
		}
	} else if _, err := managedSystemdUnit(d.path("/etc/systemd/system/wyvern.service"), d.path(wyvernUnitPath)); err != nil {
		return err
	}
	if previous != nil {
		unit, err := os.ReadFile(d.path(wyvernUnitPath))
		if err != nil || len(unit) > 32768 {
			return errors.New("Cannot preserve the managed Wyvern unit")
		}
		transaction.PreviousUnit = string(unit)
	}
	if err := d.writeJournal(transaction); err != nil {
		return err
	}
	stopped := false
	defer func() {
		if result == nil {
			return
		}
		if stopped {
			result = d.rollback(transaction)
			return
		}
		_ = os.Remove(d.path(wyvernMaintenance))
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if previous != nil && !transaction.WasDraining {
			_, _ = d.control(cleanup, "resume")
		}
		transaction.Phase = "rolled_back"
		if err := d.writeJournal(transaction); err != nil {
			result = errors.New("Wyvern was retained but its transaction journal requires repair")
		}
	}()
	if previous != nil {
		if _, err := d.control(ctx, "drain"); err != nil {
			return err
		}
		drainTimeout := d.DrainTimeout
		if drainTimeout <= 0 {
			drainTimeout = 30 * time.Second
		}
		deadline := time.Now().Add(drainTimeout)
		for {
			view, err := d.read(ctx)
			if err != nil {
				return err
			}
			if view.Status.ActiveRequests == 0 {
				break
			}
			if time.Now().After(deadline) {
				transaction.Phase = "rolled_back"
				_ = d.writeJournal(transaction)
				return errors.New("Active Wyvern requests did not drain; the existing runtime was retained")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	if err := os.MkdirAll(d.path("/etc/wyvern"), 0755); err != nil {
		return err
	}
	if err := atomicWyvernBytes(d.path(wyvernMaintenance), []byte("maintenance\n"), -1); err != nil {
		return err
	}
	transaction.Phase = "stopping"
	if err := d.writeJournal(transaction); err != nil {
		return err
	}
	// A stop command can succeed on the host even if its response is lost.
	stopped = true
	if previous != nil {
		if _, err := d.run(ctx, "systemctl", "stop", "wyvern.service"); err != nil {
			return err
		}
	}
	if err := d.Prepare(ctx); err != nil {
		return err
	}
	if err := d.installUnit(wyvernUnit); err != nil {
		return err
	}
	transaction.Phase = "activating"
	if err := d.writeJournal(transaction); err != nil {
		return err
	}
	if err := d.activate(ctx, candidate, transaction.WasReady, transaction.WasDraining); err == nil {
		if err := atomicWyvernFile(d.path(wyvernCurrent), candidate, -1); err != nil {
			return err
		}
		transaction.Phase = "completed"
		return d.writeJournal(transaction)
	} else {
		return err
	}
}

func (d WyvernDeployment) installUnit(unit string) error {
	link, target := d.path("/etc/systemd/system/wyvern.service"), d.path(wyvernUnitPath)
	if _, err := os.Lstat(link); err == nil {
		if _, err := managedSystemdUnit(link, target); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := atomicWyvernBytes(target, []byte(unit), -1); err != nil {
		return err
	}
	if err := os.Chmod(target, 0644); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
		return err
	}
	if _, err := os.Lstat(link); os.IsNotExist(err) {
		if d.Root != "" {
			return os.Symlink(target, link)
		}
		_, err := d.run(context.Background(), "systemctl", "link", target)
		return err
	}
	return nil
}
func (d WyvernDeployment) rollback(transaction wyvernTransaction) error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if transaction.Previous == nil {
		_, _ = d.run(ctx, "systemctl", "stop", "wyvern.service")
		transaction.Phase = "failed"
		_ = d.writeJournal(transaction)
		return WyvernDeploymentError{}
	}
	if transaction.PreviousUnit == "" || d.installUnit(transaction.PreviousUnit) != nil {
		transaction.Phase = "rollback_failed"
		_ = d.writeJournal(transaction)
		return WyvernDeploymentError{RollbackFailed: true}
	}
	_ = atomicWyvernBytes(d.path(wyvernMaintenance), []byte("maintenance\n"), -1)
	if err := d.activate(ctx, *transaction.Previous, transaction.WasReady, transaction.WasDraining); err != nil {
		transaction.Phase = "rollback_failed"
		_ = d.writeJournal(transaction)
		return WyvernDeploymentError{RollbackFailed: true}
	}
	if err := atomicWyvernFile(d.path(wyvernCurrent), transaction.Previous, -1); err != nil {
		return WyvernDeploymentError{RollbackFailed: true}
	}
	transaction.Phase = "rolled_back"
	if err := d.writeJournal(transaction); err != nil {
		return err
	}
	return WyvernDeploymentError{RolledBack: true}
}
func (d WyvernDeployment) Repair(ctx context.Context) error {
	body, err := os.ReadFile(d.path(wyvernJournal))
	if err == nil {
		var pending wyvernTransaction
		if json.Unmarshal(body, &pending) != nil {
			return errors.New("Invalid Wyvern recovery journal")
		}
		candidate, _ := json.Marshal(pending.Candidate)
		if _, err := ParseWyvernManifest(candidate); err != nil {
			return err
		}
		if pending.Previous != nil {
			previous, _ := json.Marshal(pending.Previous)
			if _, err := ParseWyvernManifest(previous); err != nil {
				return err
			}
		}
		if pending.Phase != "completed" && pending.Phase != "rolled_back" {
			if pending.Previous != nil {
				err := d.rollback(pending)
				var outcome WyvernDeploymentError
				if errors.As(err, &outcome) && outcome.RolledBack {
					return nil
				}
				return err
			}
			if err := d.installUnit(wyvernUnit); err != nil {
				return err
			}
			if err := d.activate(ctx, pending.Candidate, false, false); err != nil {
				return err
			}
			if err := atomicWyvernFile(d.path(wyvernCurrent), pending.Candidate, -1); err != nil {
				return err
			}
			pending.Phase = "completed"
			return d.writeJournal(pending)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	manifest, err := d.installed()
	if err != nil {
		return err
	}
	if manifest == nil {
		return errors.New("Wyvern is not installed")
	}
	if _, err := managedSystemdUnit(d.path("/etc/systemd/system/wyvern.service"), d.path(wyvernUnitPath)); err != nil {
		return err
	}
	_, bootstrapError := os.Stat(d.path("/etc/wyvern/identity/bootstrap.json"))
	return d.activate(ctx, *manifest, bootstrapError == nil, false)
}

const wyvernUnit = `[Unit]
Description=Exocortex Wyvern LLM gateway
Requires=docker.service
After=docker.service network-online.target
[Service]
Type=simple
RuntimeDirectory=wyvern wyvern-admin
RuntimeDirectoryPreserve=yes
RuntimeDirectoryMode=0750
EnvironmentFile=/etc/wyvern/wyvern.env
ExecStartPre=/usr/bin/updater wyvern prepare-runtime
ExecStartPre=-/usr/bin/docker rm -f exocortex-wyvern
ExecStart=/usr/bin/docker run --rm --init --name exocortex-wyvern --label io.exocortex.managed=wyvern --read-only --cap-drop ALL --security-opt no-new-privileges:true --pids-limit 128 --memory 256m --cpus 2 --env WYVERN_BOOTSTRAP_FILE=/etc/wyvern/identity/bootstrap.json --tmpfs /tmp:rw,nosuid,nodev,noexec,size=16m --mount type=bind,source=/etc/wyvern,target=/etc/wyvern,readonly --mount type=bind,source=/run/wyvern,target=/run/wyvern --mount type=bind,source=/run/wyvern-admin,target=/run/wyvern-admin --mount type=bind,source=/var/lib/wyvern,target=/var/lib/wyvern $WYVERN_IMAGE
ExecStop=/usr/bin/docker stop --time 35 exocortex-wyvern
Restart=on-failure
RestartSec=3
TimeoutStopSec=45
[Install]
WantedBy=multi-user.target
`
