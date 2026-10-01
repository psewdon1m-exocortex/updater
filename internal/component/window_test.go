package component

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowManifestIdentityCompatibilityAndUnitPolicy(t *testing.T) {
	manifest := windowManifest{Schema: "exocortex.window.release.v1", Product: "window-linux", Version: "0.1.2", Runtime: "linux-amd64", MinimumUpdater: "0.6.7", BinarySHA256: strings.Repeat("a", 64), UnitSHA256: strings.Repeat("b", 64), UpdaterBootstrapSHA256: strings.Repeat("c", 64)}
	body, _ := json.Marshal(manifest)
	if _, err := parseWindowManifest(body, "0.1.2", "linux-amd64", "0.6.7"); err != nil {
		t.Fatal(err)
	}
	if _, err := parseWindowManifest(body, "0.1.2", "linux-arm64", "0.6.7"); err == nil {
		t.Fatal("wrong platform accepted")
	}
	if _, err := parseWindowManifest(body, "0.1.2", "linux-amd64", "0.6.6"); err == nil {
		t.Fatal("old Updater accepted")
	}
	if _, err := parseWindowManifest(append(body, []byte(` {}`)...), "0.1.2", "linux-amd64", "0.6.7"); err == nil {
		t.Fatal("trailing manifest accepted")
	}
	path := filepath.Join(t.TempDir(), "window.service")
	unit := []byte("[Service]\nExecStart=/usr/local/bin/window serve\nUser=root\nProtectSystem=strict\nNoNewPrivileges=true\nRestrictAddressFamilies=AF_UNIX\nRuntimeDirectory=window window-admin\nRuntimeDirectoryPreserve=yes\n")
	if err := os.WriteFile(path, unit, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateWindowUnit(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[Service]\nExecStart=/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateWindowUnit(path); err == nil {
		t.Fatal("unsafe unit accepted")
	}
}
