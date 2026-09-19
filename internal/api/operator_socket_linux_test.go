//go:build linux

package api

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"updater/internal/console"
)

func TestOperatorSocketPermissionsCollisionAndUnexpectedPaths(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-only listener fixture; run this test with sudo in Linux acceptance")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "admin", "operator.sock")
	service := filepath.Join(dir, "service", "updater.sock")
	listener, err := listenOperator(path, service)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("operator socket not mode 0600")
	}
	if duplicate, err := listenOperator(path, service); err == nil {
		duplicate.Close()
		t.Fatal("active listener was replaced")
	}
	if same, err := listenOperator(filepath.Join(dir, "service", "admin.sock"), service); err == nil {
		same.Close()
		t.Fatal("operator socket shared the service-mounted directory")
	}
	if nested, err := listenOperator(filepath.Join(dir, "service", "nested", "admin.sock"), service); err == nil {
		nested.Close()
		t.Fatal("operator socket was placed inside a service-mounted directory")
	}
	file := filepath.Join(dir, "admin", "regular-file")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := listenOperator(file, service); err == nil {
		t.Fatal("regular file was replaced")
	}
	data, _ := os.ReadFile(file)
	if string(data) != "keep" {
		t.Fatal("existing file changed")
	}
	link := filepath.Join(dir, "admin", "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := listenOperator(link, service); err == nil {
		t.Fatal("symlink was replaced")
	}
}

func TestOperatorSocketPeerAndActualClientRoundTrip(t *testing.T) {
	if os.Getenv("UPDATER_TEST_NONROOT_PEER") == "1" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		client := console.NewClient(os.Getenv("UPDATER_TEST_PEER_SOCKET"))
		defer client.Close()
		if _, err := client.Snapshot(ctx); err == nil {
			os.Exit(9)
		}
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("requires root to exercise Linux peer credentials")
	}
	dir, err := os.MkdirTemp("", "operator-peer-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "admin", "operator.sock")
	listener, err := listenOperator(path, filepath.Join(dir, "service", "updater.sock"))
	if err != nil {
		t.Fatal(err)
	}
	s := operatorFixture(t)
	server := &http.Server{Handler: s.operatorHandler()}
	defer server.Close()
	go server.Serve(listener)
	client := console.NewClient(path)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	result, err := client.Snapshot(ctx)
	if err != nil || result.Protocol != console.Protocol {
		t.Fatalf("root round trip: %v", err)
	}
	// Deliberately weaken filesystem permissions in this disposable fixture.
	// The kernel peer-UID check must still deny a non-root local connection.
	if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	copyPath := filepath.Join(dir, "peer-test")
	target, err := os.OpenFile(copyPath, os.O_CREATE|os.O_WRONLY, 0755)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatal("copy peer helper")
	}
	command := exec.Command(copyPath, "-test.run=^TestOperatorSocketPeerAndActualClientRoundTrip$")
	command.Env = append(os.Environ(), "UPDATER_TEST_NONROOT_PEER=1", "UPDATER_TEST_PEER_SOCKET="+path)
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("non-root peer was accepted: %v %s", err, output)
	}
}

func TestOperatorClientReconnectsAfterListenerReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "reconnect.sock")
	start := func(version string) (net.Listener, *http.Server) {
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `{"protocol":1,"host":"`+version+`"}`)
		})}
		go server.Serve(listener)
		return listener, server
	}
	firstListener, first := start("before")
	client := console.NewClient(path)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if snapshot, err := client.Snapshot(ctx); err != nil || snapshot.Host != "before" {
		t.Fatal("initial connection failed")
	}
	first.Close()
	firstListener.Close()
	if _, err := client.Snapshot(ctx); err == nil {
		t.Fatal("missing listener reported healthy")
	}
	secondListener, second := start("after")
	defer second.Close()
	defer secondListener.Close()
	if snapshot, err := client.Snapshot(ctx); err != nil || snapshot.Host != "after" {
		t.Fatalf("reconnect failed: %v", err)
	}
}

func TestOperatorUnitKeepsSeparateRuntimeDirectory(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate unit fixture")
	}
	body, err := os.ReadFile(filepath.Join(filepath.Dir(source), "..", "..", "systemd", "updater.service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"RuntimeDirectory=exocortex exocortex-admin", "RuntimeDirectoryPreserve=yes", "ReadWritePaths=/dev/shm /run/exocortex /run/exocortex-admin"} {
		if !strings.Contains(string(body), required) {
			t.Fatalf("unit missing %s", required)
		}
	}
}
