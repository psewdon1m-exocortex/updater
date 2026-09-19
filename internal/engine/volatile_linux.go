package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"updater/internal/model"
)

// Called after the single daemon has claimed its socket, before accepting work.
func CleanupVolatileRecovery() error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/dev/shm", &stat); err != nil || stat.Type != 0x01021994 {
		return nil // Restore itself fails closed if a tmpfs path cannot be provided.
	}
	entries, err := os.ReadDir("/dev/shm")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "exocortex-recovery-") {
			if err = os.RemoveAll(filepath.Join("/dev/shm", entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) hasVolatile(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.volatile[id]) > 0
}

func (e *Engine) clearVolatile(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	clear(e.volatile[id])
	delete(e.volatile, id)
}

// Recovery material may be materialized only on verified RAM-backed storage.
// In particular, never fall back to /tmp when tmpfs is unavailable.
func recoveryDirectory() (string, func(), error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/dev/shm", &stat); err != nil || stat.Type != 0x01021994 {
		return "", nil, errors.New("RAM-backed recovery storage is unavailable; restore the saved ZIP manually")
	}
	dir, err := os.MkdirTemp("/dev/shm", "exocortex-recovery-")
	if err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

func (e *Engine) recoveryFile(id string) (string, func(), error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	data := e.volatile[id]
	if len(data) == 0 {
		return "", nil, errors.New("upload the saved pre-update ZIP to recover this operation")
	}
	dir, cleanup, err := recoveryDirectory()
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, "backup.zip")
	if err = os.WriteFile(path, data, 0600); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

// StartDownloaded is the only HTTP installation entry point. The receipt is
// minted by the authenticated head and binds the bytes to one target version.
func (e *Engine) StartDownloaded(request model.UpdateRequest, token string) (model.Job, error) {
	if err := VerifyBackupReceipt(request, token); err != nil {
		return model.Job{}, err
	}
	return e.Start(request)
}

func (e *Engine) RollbackDownloaded(id string, backup model.Backup) (model.Job, error) {
	job, ok := e.store.Get(id)
	if !ok || job.RecoveryMode != "operator-copy" || job.FinishedAt == nil {
		return model.Job{}, errors.New("saved-copy rollback is unavailable")
	}
	if backup.SHA256 != job.BackupSHA256 {
		return model.Job{}, errors.New("upload the original pre-update ZIP; checksum does not match")
	}
	data, err := decodeBackup(backup)
	if err != nil {
		return model.Job{}, err
	}
	defer clear(data)
	e.mu.Lock()
	if e.busy || len(e.volatile[id]) > 0 {
		e.mu.Unlock()
		return model.Job{}, errors.New("another update is already running")
	}
	e.volatile[id] = append([]byte(nil), data...)
	e.mu.Unlock()
	result, err := e.Rollback(id)
	if err != nil {
		e.clearVolatile(id)
	}
	return result, err
}
