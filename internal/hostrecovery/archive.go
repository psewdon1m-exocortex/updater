// Package hostrecovery archives helper data only. Executables, systemd units,
// release trust keys and head deployment environments must be provisioned by the signed installer.
package hostrecovery

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

const MaxBytes = 128 * 1024 * 1024
const magic = "EXOCORTEX-HELPER-RECOVERY-1\n"

var roots = []string{"etc/neptune", "etc/gryphon", "var/lib/neptune", "var/lib/gryphon", "var/lib/updater/jobs", "var/lib/updater/backups"}

type Entry struct {
	Name string
	Data []byte
}

func allowed(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\\x00:") || path.Clean(name) != name || strings.HasPrefix(name, "/") {
		return false
	}
	for _, root := range roots {
		if strings.HasPrefix(name, root+"/") {
			return true
		}
	}
	return false
}
func rooted(root, name string) (string, error) {
	if !allowed(name) {
		return "", errors.New("recovery entry is outside helper data roots")
	}
	target := filepath.Join(root, filepath.FromSlash(name))
	current := root
	for _, part := range strings.Split(name, "/") {
		current = filepath.Join(current, part)
		if info, err := os.Lstat(current); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("recovery target contains a symbolic link")
		}
	}
	return target, nil
}
func Seal(entries []Entry, password string) ([]byte, error) {
	if len(entries) == 0 {
		return nil, errors.New("no helper state is available for recovery")
	}
	if len(password) < 16 || len(password) > 1024 {
		return nil, errors.New("recovery passphrase must contain 16 to 1024 bytes")
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	total := 0
	seen := map[string]bool{}
	for _, entry := range entries {
		if !allowed(entry.Name) || seen[entry.Name] {
			return nil, errors.New("invalid or duplicate recovery entry")
		}
		seen[entry.Name] = true
		total += len(entry.Data)
		if total > MaxBytes || len(seen) > 10000 {
			return nil, errors.New("helper recovery exceeds its byte or file limit")
		}
		if err := validateData(entry); err != nil {
			return nil, err
		}
		member, err := writer.Create(entry.Name)
		if err != nil {
			return nil, err
		}
		if _, err = member.Write(entry.Data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, 600000, 32)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	header := append(append([]byte(magic), salt...), nonce...)
	return gcm.Seal(header, nonce, archive.Bytes(), header), nil
}
func Open(encrypted []byte, password string) ([]Entry, error) {
	headerSize := len(magic) + 16 + 12
	if len(password) < 16 || len(password) > 1024 || len(encrypted) < headerSize+16 || len(encrypted) > MaxBytes+2*1024*1024 || string(encrypted[:len(magic)]) != magic {
		return nil, errors.New("invalid helper recovery archive")
	}
	salt := encrypted[len(magic) : len(magic)+16]
	nonce := encrypted[len(magic)+16 : headerSize]
	key, err := pbkdf2.Key(sha256.New, password, salt, 600000, 32)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	plaintext, err := gcm.Open(nil, nonce, encrypted[headerSize:], encrypted[:headerSize])
	if err != nil {
		return nil, errors.New("incorrect passphrase or damaged helper archive")
	}
	defer clear(plaintext)
	reader, err := zip.NewReader(bytes.NewReader(plaintext), int64(len(plaintext)))
	if err != nil {
		return nil, errors.New("invalid helper archive payload")
	}
	if len(reader.File) > 10000 {
		return nil, errors.New("helper archive has too many entries")
	}
	entries := []Entry{}
	seen := map[string]bool{}
	total := 0
	for _, file := range reader.File {
		if !allowed(file.Name) || seen[file.Name] || file.Mode()&os.ModeType != 0 || file.UncompressedSize64 > MaxBytes {
			return nil, errors.New("invalid helper archive member")
		}
		seen[file.Name] = true
		source, err := file.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(source, MaxBytes-int64(total)+1))
		source.Close()
		if err != nil {
			return nil, err
		}
		total += len(data)
		if total > MaxBytes {
			return nil, errors.New("helper archive expansion limit exceeded")
		}
		entry := Entry{Name: file.Name, Data: data}
		if err := validateData(entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func validateData(entry Entry) error {
	if strings.HasPrefix(entry.Name, "etc/") {
		if strings.HasSuffix(entry.Name, ".env") {
			for _, line := range strings.Split(string(entry.Data), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				key, value, ok := strings.Cut(line, "=")
				if !ok || !regexp.MustCompile(`^(NEPTUNE_[A-Z0-9_]+|Neptune__[A-Za-z0-9]+|GRYPHON_[A-Z0-9_]+)$`).MatchString(key) || strings.ContainsAny(value, "`$\x00") {
					return errors.New("unsupported helper environment setting")
				}
			}
		} else if !strings.HasSuffix(entry.Name, ".json") && !strings.HasSuffix(entry.Name, ".token") {
			return errors.New("unsupported helper configuration file")
		}
	}
	if strings.HasPrefix(entry.Name, "var/lib/updater/jobs/") {
		var job struct {
			BackupPath string `json:"backup_path"`
			HeadID     string `json:"head_id"`
			ID         string `json:"id"`
		}
		if json.Unmarshal(entry.Data, &job) != nil || !regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`).MatchString(job.HeadID) || path.Base(entry.Name) != job.ID+".json" {
			return errors.New("invalid restored Updater job")
		}
		if job.BackupPath != "" && (!strings.HasPrefix(job.BackupPath, "/var/lib/updater/backups/") || path.Clean(job.BackupPath) != job.BackupPath) {
			return errors.New("invalid restored backup path")
		}
	}
	return nil
}

// Collect is called only while the helper daemons are stopped and Updater is idle.
func Collect(root string) ([]Entry, error) {
	entries := []Entry{}
	total := int64(0)
	for _, base := range roots {
		start := filepath.Join(root, filepath.FromSlash(base))
		if _, err := os.Stat(start); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		err := filepath.WalkDir(start, func(filename string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("helper recovery refuses symbolic links")
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() {
				return errors.New("unsupported helper state file")
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
			if total > MaxBytes || len(entries) >= 10000 {
				return errors.New("helper recovery limit exceeded; review retention before retrying")
			}
			name, err := filepath.Rel(root, filename)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(filename)
			if err != nil {
				return err
			}
			entries = append(entries, Entry{Name: filepath.ToSlash(name), Data: data})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return entries, nil
}
