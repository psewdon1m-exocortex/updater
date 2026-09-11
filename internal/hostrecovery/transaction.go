package hostrecovery

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type transactionRoot struct {
	Root        string `json:"root"`
	HadOriginal bool   `json:"had_original"`
}

func journalPath(root string) string {
	return filepath.Join(root, "var/lib/updater/recovery-transaction.json")
}
func durableFile(filename string, data []byte) error {
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	return errors.Join(err, closeErr)
}
func syncDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
func fixedRoot(root, relative string) (string, error) {
	valid := false
	for _, candidate := range roots {
		if candidate == relative {
			valid = true
		}
	}
	if !valid {
		return "", errors.New("invalid recovery transaction root")
	}
	// rooted checks every existing ancestor; the suffix is only used for validation.
	if _, err := rooted(root, relative+"/validation"); err != nil {
		return "", err
	}
	return filepath.Join(root, filepath.FromSlash(relative)), nil
}
func PendingRecovery(root string) bool { _, err := os.Stat(journalPath(root)); return err == nil }

// RecoverInterrupted restores all previous directories from a durable prepare
// journal. It is safe to repeat after interruption between any two renames.
func RecoverInterrupted(root string) error {
	filename := journalPath(root)
	body, err := os.ReadFile(filename)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(body) > 8192 {
		return errors.New("invalid recovery journal")
	}
	var plan []transactionRoot
	if json.Unmarshal(body, &plan) != nil || len(plan) != len(roots) {
		return errors.New("invalid recovery journal")
	}
	for index, item := range plan {
		if item.Root != roots[index] {
			return errors.New("invalid recovery journal order")
		}
		target, err := fixedRoot(root, item.Root)
		if err != nil {
			return err
		}
		previous, stage := target+".recovery-previous", target+".recovery-stage"
		for _, candidate := range []string{previous, stage} {
			if info, err := os.Lstat(candidate); err == nil && info.Mode()&os.ModeSymlink != 0 {
				return errors.New("recovery staging path is a symbolic link")
			}
		}
		if _, err := os.Stat(previous); err == nil {
			if err := os.RemoveAll(target); err != nil {
				return err
			}
			if err := os.Rename(previous, target); err != nil {
				return err
			}
		} else if !item.HadOriginal {
			if _, err := os.Stat(stage); os.IsNotExist(err) {
				if err := os.RemoveAll(target); err != nil {
					return err
				}
			}
		}
		if err := os.RemoveAll(stage); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(target)); err != nil {
			return err
		}
	}
	if err := os.Remove(filename); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(filename))
}

// Apply stages and fsyncs every file before mutation. Previous directories are
// retained until verification; the journal enables automatic restart recovery.
func Apply(root string, entries []Entry, verify func() error) error {
	if len(entries) == 0 {
		return errors.New("empty helper recovery is not allowed")
	}
	if err := RecoverInterrupted(root); err != nil {
		return err
	}
	if _, err := Collect(root); err != nil {
		return err
	}
	seen := map[string]bool{}
	total := 0
	for _, entry := range entries {
		if _, err := rooted(root, entry.Name); err != nil {
			return err
		}
		if err := validateData(entry); err != nil {
			return err
		}
		total += len(entry.Data)
		if seen[entry.Name] || len(seen) >= 10000 || total > MaxBytes {
			return errors.New("invalid recovery transaction size or duplicate member")
		}
		seen[entry.Name] = true
	}
	plan := []transactionRoot{}
	for _, relative := range roots {
		target, err := fixedRoot(root, relative)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		for _, stale := range []string{target + ".recovery-stage", target + ".recovery-previous"} {
			if info, err := os.Lstat(stale); err == nil && info.Mode()&os.ModeSymlink != 0 {
				return errors.New("unsafe stale recovery directory")
			}
			if err := os.RemoveAll(stale); err != nil {
				return err
			}
		}
		stage := target + ".recovery-stage"
		if err := os.Mkdir(stage, 0700); err != nil {
			return err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name, relative+"/") {
				filename := filepath.Join(stage, filepath.FromSlash(strings.TrimPrefix(entry.Name, relative+"/")))
				if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
					return err
				}
				if err := durableFile(filename, entry.Data); err != nil {
					return err
				}
				if err := syncDirectory(filepath.Dir(filename)); err != nil {
					return err
				}
			}
		}
		if err := syncDirectory(stage); err != nil {
			return err
		}
		_, err = os.Stat(target)
		plan = append(plan, transactionRoot{Root: relative, HadOriginal: err == nil})
	}
	journal := journalPath(root)
	body, _ := json.Marshal(plan)
	if err := durableFile(journal, body); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(journal)); err != nil {
		return err
	}
	fail := func(cause error) error { return errors.Join(cause, RecoverInterrupted(root)) }
	for _, item := range plan {
		target, _ := fixedRoot(root, item.Root)
		if item.HadOriginal {
			if err := os.Rename(target, target+".recovery-previous"); err != nil {
				return fail(err)
			}
		}
		if err := os.Rename(target+".recovery-stage", target); err != nil {
			return fail(err)
		}
		if err := syncDirectory(filepath.Dir(target)); err != nil {
			return fail(err)
		}
	}
	if err := verify(); err != nil {
		return fail(err)
	}
	if err := os.Remove(journal); err != nil {
		return fail(err)
	}
	if err := syncDirectory(filepath.Dir(journal)); err != nil {
		return err
	}
	for _, item := range plan {
		target, _ := fixedRoot(root, item.Root)
		if err := os.RemoveAll(target + ".recovery-previous"); err != nil {
			return err
		}
	}
	return nil
}
