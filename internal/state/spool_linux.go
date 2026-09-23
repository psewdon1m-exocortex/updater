//go:build linux

package state

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func volatileSpoolRoot(stateDirectory string) (string, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/dev/shm", &stat); err != nil || stat.Type != 0x01021994 {
		return "", ErrSpoolInvalid
	}
	identity := sha256.Sum256([]byte(filepath.Clean(stateDirectory)))
	return filepath.Join("/dev/shm", fmt.Sprintf("exocortex-updater-spools-%x", identity[:12])), nil
}

func openSpoolRegular(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Nlink != 1 {
		file.Close()
		return nil, ErrSpoolInvalid
	}
	return file, nil
}
func spoolAvailableBytes(path string) (int64, error) {
	var stat syscall.Statfs_t
	err := syscall.Statfs(path, &stat)
	return int64(stat.Bavail) * stat.Bsize, err
}

func spoolDirectoryOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}
