//go:build linux

package api

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type operatorListener struct{ *net.UnixListener }

// Check the peer as well as filesystem permissions. Membership of the service
// socket group must never grant access to host-wide operator actions.
func (l operatorListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		raw, err := connection.SyscallConn()
		var peer *syscall.Ucred
		if err == nil {
			err = raw.Control(func(fd uintptr) { peer, _ = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED) })
		}
		if err == nil && peer != nil && peer.Uid == 0 {
			return connection, nil
		}
		_ = connection.Close()
	}
}

func listenOperator(path, serviceSocket string) (net.Listener, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("operator socket requires a root-owned daemon")
	}
	relative, relErr := filepath.Rel(filepath.Dir(serviceSocket), filepath.Dir(path))
	insideServiceMount := relErr != nil || relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
	if !filepath.IsAbs(path) || insideServiceMount {
		return nil, errors.New("operator socket must be in a separate absolute directory")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || owner.Uid != 0 || info.Mode().Perm()&0022 != 0 {
		return nil, errors.New("operator directory must be root-owned and not group/world writable")
	}
	if current, err := os.Lstat(path); err == nil {
		owner, ok := current.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 || current.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("refusing to replace an unexpected operator socket path")
		}
		connection, dialErr := net.DialTimeout("unix", path, 250*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			return nil, errors.New("another operator listener is already active")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return nil, errors.New("cannot prove the existing operator socket is stale")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("protect operator socket: %w", err)
	}
	return operatorListener{listener}, nil
}
