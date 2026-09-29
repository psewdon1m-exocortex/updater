package selfupdate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUpdaterSystemdSandboxAllowsWyvernPolicyDirectory(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test source path")
	}
	unit := filepath.Join(filepath.Dir(source), "..", "..", "systemd", "updater.service")
	body, err := os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "ProtectSystem=strict") {
		t.Fatal("Updater filesystem sandbox is not strict")
	}
	line := ""
	for _, candidate := range strings.Split(text, "\n") {
		if strings.HasPrefix(candidate, "ReadWritePaths=") {
			line = candidate
			break
		}
	}
	if line == "" || !strings.Contains(" "+line+" ", " -/etc/vault ") {
		t.Fatal("Updater cannot atomically replace Saturn's protected environment file")
	}
	if !strings.Contains(" "+line+" ", " /etc/systemd/journald@wyvern.conf.d ") {
		t.Fatal("Updater cannot atomically replace Wyvern's managed journald policy")
	}
	installer, err := os.ReadFile(filepath.Join(filepath.Dir(source), "..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(installer), "install -d -o root -g root -m 0755 /etc/systemd/journald@wyvern.conf.d") {
		t.Fatal("signed host preparation does not create Wyvern's journald policy directory")
	}
	if strings.Contains(" "+line+" ", " /etc ") || strings.Contains(" "+line+" ", " -/etc ") {
		t.Fatal("Updater sandbox grants write access to all of /etc")
	}
}
