package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegisterAndLoadHeadUsesHeadEnvironment(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "kernel.env")
	body := `KERNEL_URL=http://127.0.0.1:18180
KERNEL_SERVICE_TOKEN=service-token-long-enough
UPDATER_SERVICE_ID=kernel
UPDATER_COMPOSE_PROJECT_DIR=/opt/kernel
UPDATER_COMPOSE_SERVICE=kernel
UPDATER_CONTAINER_NAME=exocortex-kernel
UPDATER_IMAGE_VARIABLE=KERNEL_IMAGE
UPDATER_VERSION_VARIABLE=KERNEL_VERSION
KERNEL_VERSION=1.1.0
UPDATER_LOCAL_HEALTH_URL=http://127.0.0.1:18180/api/health
UPDATER_CONTROL_TOKEN=control-token-long-enough
`
	if err := os.WriteFile(envPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := Runtime{RegistryPath: filepath.Join(dir, "heads.json"), StateDir: filepath.Join(dir, "state")}
	if err := RegisterHead(runtime.RegistryPath, "kernel", envPath); err != nil {
		t.Fatal(err)
	}
	head, err := LoadHead(runtime, "kernel")
	if err != nil {
		t.Fatal(err)
	}
	if head.Service != "kernel" || head.ComposeService != "kernel" || head.EnvFile != envPath ||
		head.VersionVariable != "KERNEL_VERSION" || head.CurrentVersion != "1.1.0" {
		t.Fatalf("unexpected head: %#v", head)
	}
	if _, err := LoadHead(runtime, "perimetr"); err == nil {
		t.Fatal("unregistered head must be rejected")
	}
}

func TestRegisterHeadRejectsUnsafeID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "head.env")
	_ = os.WriteFile(path, []byte("A=B\n"), 0o600)
	if err := RegisterHead(filepath.Join(t.TempDir(), "heads.json"), "../root", path); err == nil {
		t.Fatal("unsafe head ID must be rejected")
	}
}

func TestTwoHeadsShareOneHostRegistryWithoutOverwritingEachOther(t *testing.T) {
	dir := t.TempDir()
	kernelEnv := filepath.Join(dir, "kernel.env")
	perimetrEnv := filepath.Join(dir, "perimetr.env")
	if err := os.WriteFile(kernelEnv, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(perimetrEnv, []byte("A=2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(dir, "heads.json")
	if err := RegisterHead(registryPath, "kernel", kernelEnv); err != nil {
		t.Fatal(err)
	}
	if err := RegisterHead(registryPath, "perimetr", perimetrEnv); err != nil {
		t.Fatal(err)
	}
	registry, err := LoadRegistry(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.Heads) != 2 ||
		registry.Heads["kernel"].EnvFile != kernelEnv ||
		registry.Heads["perimetr"].EnvFile != perimetrEnv {
		t.Fatalf("shared registry lost a head profile: %#v", registry.Heads)
	}
}

func TestRegistriesAreLocalToEachVPS(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	firstEnv := filepath.Join(first, "kernel.env")
	secondEnv := filepath.Join(second, "perimetr.env")
	if err := os.WriteFile(firstEnv, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondEnv, []byte("A=2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstRegistry := filepath.Join(first, "heads.json")
	secondRegistry := filepath.Join(second, "heads.json")
	if err := RegisterHead(firstRegistry, "kernel", firstEnv); err != nil {
		t.Fatal(err)
	}
	if err := RegisterHead(secondRegistry, "perimetr", secondEnv); err != nil {
		t.Fatal(err)
	}
	one, _ := LoadRegistry(firstRegistry)
	two, _ := LoadRegistry(secondRegistry)
	if _, ok := one.Heads["perimetr"]; ok {
		t.Fatal("the Kernel VPS registry leaked a Perimetr VPS profile")
	}
	if _, ok := two.Heads["kernel"]; ok {
		t.Fatal("the Perimetr VPS registry leaked a Kernel VPS profile")
	}
}
