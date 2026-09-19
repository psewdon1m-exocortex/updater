package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"updater/internal/config"
	"updater/internal/state"
)

// MigrateBackupRetention runs before serving requests. Legacy job metadata and
// Compose rollback remain usable, but no retained archive or copied .env survives.
func MigrateBackupRetention(runtime config.Runtime, store *state.Store) error {
	root, err := filepath.Abs(filepath.Join(runtime.StateDir, "backups"))
	if err != nil {
		return err
	}
	if info, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("legacy backup root must be a real directory")
	}
	for _, listed := range store.List() {
		job, ok := store.Get(listed.ID)
		if !ok || job.FinishedAt == nil {
			continue
		}
		directory := filepath.Join(root, job.ID)
		if info, err := os.Lstat(directory); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
			return errors.New("unsafe legacy backup directory")
		}
		if job.BackupPath == "" {
			// A previous startup may have committed the metadata and stopped before
			// deleting its directory. Repeat that final step without touching other paths.
			if job.RecoveryMode == "operator-copy" {
				if err = os.RemoveAll(directory); err != nil {
					return err
				}
			}
			continue
		}
		target, err := filepath.Abs(job.BackupPath)
		if err != nil {
			return err
		}
		if filepath.Dir(target) != directory {
			return errors.New("legacy backup path escapes its managed job directory")
		}
		info, err := os.Lstat(target)
		if err == nil {
			if !info.Mode().IsRegular() || info.Size() > 128*1024*1024 {
				return errors.New("invalid legacy backup file")
			}
			data, err := os.ReadFile(target)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(data)
			clear(data)
			job.BackupSHA256 = hex.EncodeToString(digest[:])
			job.BackupFilename = filepath.Base(target)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if job.DeploymentSnapshot != "" {
			source, err := filepath.Abs(job.DeploymentSnapshot)
			if err != nil {
				return err
			}
			if filepath.Dir(source) != directory {
				return errors.New("legacy deployment path escapes its managed job directory")
			}
			info, err := os.Lstat(source)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Size() > 4*1024*1024 {
				return errors.New("invalid legacy deployment snapshot")
			}
			data, err := os.ReadFile(source)
			if err != nil {
				return err
			}
			var files []deploymentFile
			err = json.Unmarshal(data, &files)
			clear(data)
			if err != nil {
				return err
			}
			for i := range files {
				if files[i].Name == ".env" {
					// Existing operator values remain owned by the live .env. The image and
					// version are restored explicitly; unknown post-update defaults are kept.
					files[i].TrailingNewlines = string(files[i].Data[len(strings.TrimRight(string(files[i].Data), "\r\n")):])
					clear(files[i].Data)
					files[i].Data = nil
					files[i].EnvironmentMetadata = true
				}
			}
			destination := filepath.Join(runtime.StateDir, "deployments", job.ID, "deployment.json")
			body, err := json.Marshal(files)
			if err != nil {
				return err
			}
			if err = atomicDeploymentWrite(destination, body); err != nil {
				return err
			}
			job.DeploymentSnapshot = destination
		}
		job.BackupPath = ""
		job.RecoveryMode = "operator-copy"
		job.RollbackAvailable = job.RollbackAvailable && job.BackupSHA256 != ""
		if err = store.Save(job); err != nil {
			return err
		}
		// directory is derived from a validated store job ID, under a verified root.
		if err = os.RemoveAll(directory); err != nil {
			return err
		}
	}
	// Before the legacy job record was committed, a crash could leave only its
	// generated archive directory. Limit orphan cleanup to the engine's ID shape.
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	generatedID := regexp.MustCompile(`^[0-9]{10}-[a-f0-9]{37}$`)
	for _, entry := range entries {
		if entry.IsDir() && generatedID.MatchString(entry.Name()) {
			if _, known := store.Get(entry.Name()); !known {
				if err = os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
