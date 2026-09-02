package socketmount

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"updater/internal/config"
)

func TestRepairLeavesCurrentBindMountRunning(t *testing.T) {
	repairer, hostDirectory := testRepairer(t)
	repairer.processMountPath = func(int, string) string { return hostDirectory }
	recreated := false
	repairer.recreate = func(context.Context, config.HeadConfig) error {
		recreated = true
		return nil
	}

	report := repairer.Repair(context.Background())
	if recreated || len(report.Recreated) != 0 || len(report.Checked) != 1 {
		t.Fatalf("unexpected repair report: %#v", report)
	}
}

func TestRepairRecreatesOnlyAStaleBindMount(t *testing.T) {
	repairer, _ := testRepairer(t)
	staleDirectory := t.TempDir()
	repairer.processMountPath = func(int, string) string { return staleDirectory }
	recreated := ""
	repairer.recreate = func(_ context.Context, head config.HeadConfig) error {
		recreated = head.ComposeService
		return nil
	}

	report := repairer.Repair(context.Background())
	if recreated != "kernel" {
		t.Fatalf("recreated service = %q", recreated)
	}
	if len(report.Recreated) != 1 || report.Recreated[0] != "kernel" || len(report.Warnings) != 0 {
		t.Fatalf("unexpected repair report: %#v", report)
	}
}

func testRepairer(t *testing.T) (*Repairer, string) {
	t.Helper()
	directory := t.TempDir()
	hostDirectory := filepath.Join(directory, "run", "exocortex")
	if err := os.MkdirAll(hostDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(directory, "kernel.env")
	envBody := `KERNEL_URL=http://127.0.0.1:18180
KERNEL_SERVICE_TOKEN=service-token-long-enough
UPDATER_SERVICE_ID=kernel
UPDATER_COMPOSE_PROJECT_DIR=/opt/kernel
UPDATER_COMPOSE_FILE=compose.production.yaml
UPDATER_COMPOSE_SERVICE=kernel
UPDATER_CONTAINER_NAME=exocortex-kernel
UPDATER_IMAGE_VARIABLE=KERNEL_IMAGE
UPDATER_VERSION_VARIABLE=KERNEL_VERSION
KERNEL_VERSION=1.0.0
UPDATER_LOCAL_HEALTH_URL=http://127.0.0.1:18180/api/health
UPDATER_CONTROL_TOKEN=control-token-long-enough
`
	if err := os.WriteFile(envPath, []byte(envBody), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := config.Runtime{
		SocketPath:   filepath.Join(hostDirectory, "updater.sock"),
		RegistryPath: filepath.Join(directory, "heads.json"),
	}
	if err := config.RegisterHead(runtime.RegistryPath, "kernel", envPath); err != nil {
		t.Fatal(err)
	}
	repairer := New(runtime)
	repairer.inspectContainer = func(context.Context, string) (containerState, error) {
		return containerState{
			State: struct {
				PID int `json:"Pid"`
			}{PID: 42},
			Mounts: []containerMount{{Source: hostDirectory, Destination: "/run/exocortex"}},
		}, nil
	}
	return repairer, hostDirectory
}
