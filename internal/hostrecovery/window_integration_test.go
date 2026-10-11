//go:build linux && window_integration

package hostrecovery

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This explicit integration tag runs only in a fresh disposable Linux
// container. The real Window broker and Unix API are used; systemd's process
// lifecycle is simulated because it is not PID 1 in the test container.
func TestWindowRecoveryRealBroker(t *testing.T) {
	if os.Getenv("WINDOW_RECOVERY_CONTAINER_TEST") != "1" || os.Geteuid() != 0 {
		t.Fatal("run this test only in its disposable Linux container")
	}
	for _, name := range []string{"/var/lib/updater", "/var/lib/window-ssh", "/var/lib/window", "/run/window", "/run/window-admin", "/etc/systemd/system/window.service"} {
		if _, err := os.Lstat(name); !os.IsNotExist(err) {
			t.Fatal("refusing a test container with existing service state", name, err)
		}
	}
	source := os.Getenv("WINDOW_SOURCE")
	if source == "" {
		t.Fatal("WINDOW_SOURCE is required")
	}
	work := t.TempDir()
	binary := filepath.Join(work, "window")
	build := exec.Command("go", "build", "-o", binary, "./cmd/window")
	build.Dir = source
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real Window: %v %s", err, output)
	}
	if output, err := exec.Command("useradd", "--system", "--user-group", "--home", "/var/lib/window-ssh", "--shell", "/bin/sh", "window").CombinedOutput(); err != nil {
		t.Fatalf("test account: %v %s", err, output)
	}
	for _, directory := range []string{"/var/lib/updater", "/var/lib/window-ssh/.ssh", "/var/lib/window", "/etc/systemd/system"} {
		if err := os.MkdirAll(directory, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile("/etc/systemd/system/window.service", []byte("trusted test fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	// A lifecycle shim must reject any attempt to touch a neighboring service.
	shim := fmt.Sprintf(`#!/bin/sh
set -eu
printf '%%s\n' "$*" >> %s/systemctl.log
action="$1"
shift
if [ "${1:-}" = --quiet ]; then shift; fi
[ "${1:-}" = window.service ] || exit 98
case "$action" in
  is-active) [ -f %s/pid ] && kill -0 "$(cat %s/pid)" 2>/dev/null ;;
  start)
    if [ -f %s/fail-next-start ]; then rm -f %s/fail-next-start; exit 95; fi
    %s serve >> %s/broker.log 2>&1 &
    echo "$!" > %s/pid
    ;;
  stop)
    if [ -f %s/pid ]; then kill "$(cat %s/pid)" 2>/dev/null || true; fi
    rm -f %s/pid
    for n in $(seq 1 100); do
      [ ! -S /run/window-admin/admin.sock ] && exit 0
      sleep 0.03
    done
    exit 97
    ;;
  *) exit 96 ;;
esac
`, work, work, work, work, work, binary, work, work, work, work, work)
	if err := os.WriteFile(filepath.Join(work, "systemctl"), []byte(shim), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", work+":"+os.Getenv("PATH"))
	t.Cleanup(func() {
		_ = units("stop", []string{"window.service"})
		if body, err := os.ReadFile(filepath.Join(work, "broker.log")); err == nil && len(body) > 0 {
			t.Log("broker log:", string(body))
		}
	})
	if err := units("start", []string{"window.service"}); err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/run/window-admin/admin.sock")
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	request := func(method, route string, body any, status int) map[string]any {
		t.Helper()
		data, _ := json.Marshal(body)
		response, err := client.Do(mustWindowRequest(t, method, route, data))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		content, _ := io.ReadAll(response.Body)
		if response.StatusCode != status {
			t.Fatalf("%s: %d %s", route, response.StatusCode, content)
		}
		result := map[string]any{}
		if json.Unmarshal(content, &result) != nil {
			t.Fatal("invalid broker response")
		}
		return result
	}
	if err := health("window.service"); err != nil {
		t.Fatal(err)
	}
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blob := append([]byte("\x00\x00\x00\x0bssh-ed25519\x00\x00\x00\x20"), key...)
	paired := request("POST", "/v1/pair", map[string]string{"key": "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob)}, 200)
	grant := request("POST", "/v1/open", map[string]int{"minutes": 20}, 200)
	request("POST", "/v1/live/start", nil, 200)
	if request("GET", "/v1/status", nil, 200)["live"] != true {
		t.Fatal("live precondition not reached")
	}
	// The actual exporter stops the broker and never exports its active grant.
	archive, err := ExportScope("synthetic recovery passphrase", "window")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := OpenScope(archive.Bytes, "synthetic recovery passphrase", "window")
	if err != nil {
		t.Fatal(err)
	}
	if err := health("window.service", entries...); err != nil {
		t.Fatal(err)
	}
	request("POST", "/v1/heartbeat", map[string]any{"lease_id": grant["lease_id"]}, 403)
	// Replace the live pairing with another real key, open again, then restore.
	request("POST", "/v1/pair", map[string]string{"key": strings.TrimSuffix(strings.TrimPrefix(string(syntheticWindowPairing("live")), windowAuthorizedPrefix), " window-client\n")}, 200)
	newGrant := request("POST", "/v1/open", map[string]int{"minutes": 20}, 200)
	if err := os.WriteFile(filepath.Join(work, "fail-next-start"), []byte("inject startup failure"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RestoreScope(archive.Bytes, "synthetic recovery passphrase", "window"); err == nil {
		t.Fatal("startup failure was reported as success")
	}
	current := []Entry{{Name: windowPairingFile, Data: syntheticWindowPairing("live")}}
	if err := health("window.service", current...); err != nil {
		t.Fatal("failed restore did not recover prior pairing", err)
	}
	request("POST", "/v1/heartbeat", map[string]any{"lease_id": newGrant["lease_id"]}, 403)
	newGrant = request("POST", "/v1/open", map[string]int{"minutes": 20}, 200)
	if err := RestoreScope(archive.Bytes, "synthetic recovery passphrase", "window"); err != nil {
		t.Fatal(err)
	}
	status := request("GET", "/v1/status", nil, 200)
	if status["fingerprint"] != paired["fingerprint"] || status["open"] != false || status["live"] != false {
		t.Fatal("real broker restored wrong or active pairing", status)
	}
	request("POST", "/v1/heartbeat", map[string]any{"lease_id": newGrant["lease_id"]}, 403)
	// Rehearse a killed restore after directory replacement. Only Window's
	// recorded service may be stopped/restarted during rollback repair.
	for _, wasRunning := range []bool{true, false} {
		if err := units("stop", []string{"window.service"}); err != nil {
			t.Fatal(err)
		}
		saved := []string{}
		if wasRunning {
			saved = []string{"window.service"}
		}
		if err := recordRecoveryUnits(saved, "window"); err != nil {
			t.Fatal(err)
		}
		target := "/" + windowPairingRoot
		if err := os.Rename(target, target+".recovery-previous"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(target, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("/"+windowPairingFile, syntheticWindowPairing("live"), 0644); err != nil {
			t.Fatal(err)
		}
		journal, _ := json.Marshal([]transactionRoot{{Root: windowPairingRoot, HadOriginal: true}})
		if err := os.WriteFile(journalPath("/"), journal, 0600); err != nil {
			t.Fatal(err)
		}
		if err := units("start", []string{"window.service"}); err != nil {
			t.Fatal(err)
		}
		if err := health("window.service"); err != nil {
			t.Fatal(err)
		}
		interruptedGrant := request("POST", "/v1/open", map[string]int{"minutes": 20}, 200)
		if err := ResumeInterruptedHost(); err != nil {
			t.Fatal(err)
		}
		if PendingHostRecovery() {
			t.Fatal("resume left recovery pending")
		}
		if !wasRunning {
			if runningUnit("window.service") {
				t.Fatal("resume started a previously inactive broker")
			}
			if err := units("start", []string{"window.service"}); err != nil {
				t.Fatal(err)
			}
		}
		if err := health("window.service", entries...); err != nil {
			t.Fatal(err)
		}
		request("POST", "/v1/heartbeat", map[string]any{"lease_id": interruptedGrant["lease_id"]}, 403)
	}
	if output, err := exec.Command("runuser", "-u", "window", "--", "cat", "/"+windowPairingFile).CombinedOutput(); err != nil || !bytes.Equal(output, entries[0].Data) {
		t.Fatalf("sshd account cannot read restored public pairing: %v %s", err, output)
	}
	for name, mode := range map[string]os.FileMode{"/" + windowPairingRoot: 0755, "/" + windowPairingFile: 0644} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatal("wrong restored public pairing permissions", name, err)
		}
	}
	empty, err := SealScope([]Entry{{Name: windowPairingFile}}, "synthetic recovery passphrase", "window")
	if err != nil {
		t.Fatal(err)
	}
	if err := RestoreScope(empty, "synthetic recovery passphrase", "window"); err != nil {
		t.Fatal(err)
	}
	if status := request("GET", "/v1/status", nil, 200); status["paired"] != false || status["open"] != false {
		t.Fatal("unpaired archive retained access", status)
	}
	request("POST", "/v1/open", map[string]int{"minutes": 20}, 400)
	commands, _ := os.ReadFile(filepath.Join(work, "systemctl.log"))
	if strings.Contains(string(commands), "neptune") || strings.Contains(string(commands), "gryphon") || strings.Contains(string(commands), "wyvern") || strings.Contains(string(commands), "updater.service") {
		t.Fatal("Window recovery touched another service", string(commands))
	}
	t.Log("real broker: export, paired/unpaired restore, startup-failure rollback, interrupted active/inactive restore, closed grants, invalid old leases and SSH account readability passed")
}

func mustWindowRequest(t *testing.T, method, route string, data []byte) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, "http://window.local"+route, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return request
}
