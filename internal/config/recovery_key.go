package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

type recoveryKey struct {
	Schema  string `json:"schema"`
	Service string `json:"service"`
	Key     string `json:"key"`
}

func recoveryKeyPath(runtime Runtime, service string) (string, error) {
	switch service {
	case "updater", "neptune", "gryphon", "wyvern":
	default:
		return "", errors.New("unknown recovery service")
	}
	return filepath.Join(runtime.StateDir, "recovery-keys", service+".json"), nil
}

// The recovery key is independent of producer credentials and is never returned
// through the operator API. Existing passphrase archives remain readable.
func ReadRecoveryKey(filename, service string) (string, error) {
	fd, err := syscall.Open(filename, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(fd), filename)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || owner.Uid != 0 || info.Mode().Perm()&0o077 != 0 || info.Size() > 4096 {
		return "", errors.New("recovery key must be a private root-owned regular file")
	}
	body := make([]byte, info.Size())
	if _, err := file.ReadAt(body, 0); err != nil {
		return "", err
	}
	defer clear(body)
	var key recoveryKey
	if json.Unmarshal(body, &key) != nil || key.Schema != "exocortex.host-recovery.key.v1" || key.Service != service || len(key.Key) != 64 {
		return "", errors.New("invalid key or wrong recovery service")
	}
	raw, err := hex.DecodeString(key.Key)
	defer clear(raw)
	if err != nil || len(raw) != 32 {
		return "", errors.New("invalid recovery key")
	}
	return key.Key, nil
}

func ManagedRecoveryKey(runtime Runtime, service string, create bool) (string, error) {
	filename, err := recoveryKeyPath(runtime, service)
	if err != nil {
		return "", err
	}
	key, err := ReadRecoveryKey(filename, service)
	if err == nil || !create || !errors.Is(err, os.ErrNotExist) {
		return key, err
	}
	directory := filepath.Dir(filename)
	if err = os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || owner.Uid != 0 || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("recovery key directory must be private and root-owned")
	}
	random := make([]byte, 32)
	if _, err = rand.Read(random); err != nil {
		return "", err
	}
	defer clear(random)
	body, err := json.Marshal(recoveryKey{"exocortex.host-recovery.key.v1", service, hex.EncodeToString(random)})
	if err != nil {
		return "", err
	}
	defer clear(body)
	if err = writeNewRecoveryKey(filename, body); errors.Is(err, os.ErrExist) {
		return ReadRecoveryKey(filename, service)
	}
	if err != nil {
		return "", err
	}
	return ReadRecoveryKey(filename, service)
}

func writeNewRecoveryKey(filename string, body []byte) error {
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(body)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(filename)
	}
	return err
}

func ExportRecoveryKey(runtime Runtime, service, destination string) error {
	key, err := ManagedRecoveryKey(runtime, service, true)
	if err != nil {
		return err
	}
	body, err := json.Marshal(recoveryKey{"exocortex.host-recovery.key.v1", service, key})
	if err != nil {
		return err
	}
	defer clear(body)
	return writeNewRecoveryKey(destination, body)
}
