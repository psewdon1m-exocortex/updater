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
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const scopedMagic = "EXOCORTEX-HELPER-RECOVERY-2\n"

var scopedRoots = map[string][]string{
	"updater": {"var/lib/updater/jobs", "var/lib/updater/backups"},
	"neptune": {"etc/neptune", "var/lib/neptune"},
	"gryphon": {"etc/gryphon", "var/lib/gryphon"},
	"wyvern":  {"etc/wyvern/identity", "etc/exocortex/wyvern", "var/lib/wyvern"},
}

var RecoveryScopes = []string{"updater", "neptune", "gryphon", "wyvern"}

func ValidScope(scope string) bool { _, ok := scopedRoots[scope]; return ok }

func allowedForScope(scope, name string) bool {
	for _, root := range scopedRoots[scope] {
		if strings.HasPrefix(name, root+"/") {
			return true
		}
	}
	return false
}

func CollectScope(root, scope string) ([]Entry, error) {
	selected, ok := scopedRoots[scope]
	if !ok {
		return nil, errors.New("unknown helper recovery scope")
	}
	entries, err := collectRoots(root, selected)
	if err != nil || scope != "updater" {
		return entries, err
	}
	filtered := entries[:0]
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name, "var/lib/updater/jobs/") {
			var job struct {
				FinishedAt      *time.Time `json:"finished_at"`
				RecoveryPending bool       `json:"recovery_pending"`
			}
			if json.Unmarshal(entry.Data, &job) != nil {
				return nil, fmt.Errorf("invalid Updater job %q in recovery source", entry.Name)
			}
			if job.FinishedAt == nil || job.RecoveryPending {
				clear(entry.Data)
				continue
			}
		}
		filtered = append(filtered, entry)
	}
	return filtered, nil
}

func scopedHeader(scope string, salt, nonce []byte) []byte {
	header := []byte(scopedMagic + scope + "\n")
	header = append(header, salt...)
	return append(header, nonce...)
}

func SealScope(entries []Entry, password, scope string) ([]byte, error) {
	if !ValidScope(scope) {
		return nil, errors.New("unknown helper recovery scope")
	}
	if len(password) < 16 || len(password) > 1024 {
		return nil, errors.New("recovery passphrase must contain 16 to 1024 bytes")
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	total := 0
	seen := map[string]bool{}
	for _, entry := range entries {
		if !allowedForScope(scope, entry.Name) || seen[entry.Name] {
			return nil, errors.New("invalid or cross-scope recovery entry")
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
	header := scopedHeader(scope, salt, nonce)
	return gcm.Seal(header, nonce, archive.Bytes(), header), nil
}

func scopedArchiveHeader(encrypted []byte) (scope string, headerSize int, err error) {
	if !bytes.HasPrefix(encrypted, []byte(scopedMagic)) {
		return "", 0, errors.New("invalid scoped helper recovery archive")
	}
	rest := encrypted[len(scopedMagic):]
	end := bytes.IndexByte(rest, '\n')
	if end < 1 || end > 16 {
		return "", 0, errors.New("invalid helper recovery scope header")
	}
	scope = string(rest[:end])
	if !ValidScope(scope) {
		return "", 0, errors.New("unknown helper recovery scope")
	}
	return scope, len(scopedMagic) + end + 1 + 16 + 12, nil
}

func InspectScope(encrypted []byte) (string, error) {
	scope, _, err := scopedArchiveHeader(encrypted)
	return scope, err
}

func OpenScope(encrypted []byte, password, expectedScope string) ([]Entry, error) {
	scope, headerSize, err := scopedArchiveHeader(encrypted)
	if err != nil {
		return nil, err
	}
	if expectedScope != "" && scope != expectedScope {
		return nil, errors.New("helper recovery archive belongs to another service")
	}
	if len(password) < 16 || len(password) > 1024 || len(encrypted) < headerSize+16 || len(encrypted) > MaxBytes+2*1024*1024 {
		return nil, errors.New("invalid scoped helper recovery archive")
	}
	saltOffset := headerSize - 28
	salt := encrypted[saltOffset : saltOffset+16]
	nonce := encrypted[saltOffset+16 : headerSize]
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
		if !allowedForScope(scope, file.Name) || seen[file.Name] || file.Mode()&os.ModeType != 0 || file.UncompressedSize64 > MaxBytes {
			return nil, errors.New("invalid or cross-scope helper archive member")
		}
		seen[file.Name] = true
		source, err := file.Open()
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(source, MaxBytes-int64(total)+1))
		closeErr := source.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
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

func ScopeFilename(scope, setID string) string {
	return filepath.Base(scope + "-" + setID + ".exorecovery")
}
