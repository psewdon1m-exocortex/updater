package state

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const MaximumSpoolBytes int64 = 8 * 1024 * 1024 * 1024
const SpoolQuotaBytes int64 = 96 * 1024 * 1024 * 1024
const SpoolFilename = "mastermind-backup.zip"

var spoolID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var spoolHash = regexp.MustCompile(`^[a-f0-9]{64}$`)
var ErrSpoolBusy = errors.New("backup spool is busy")
var ErrSpoolInvalid = errors.New("backup spool is invalid, unavailable or not bound to this request")
var ErrSpoolQuota = errors.New("backup spool quota or free-space budget is exhausted")

type Spool struct {
	ID        string    `json:"spool_id"`
	HeadID    string    `json:"head_id"`
	RequestID string    `json:"request_id"`
	Filename  string    `json:"filename"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	ClaimedBy string    `json:"claimed_by,omitempty"`
}

func privateDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !spoolDirectoryOwned(info) {
		return ErrSpoolInvalid
	}
	return os.Chmod(path, 0700)
}

// ZIP bytes are transient, RAM-backed material. Durable jobs contain identities only.
func (s *Store) spoolRoot() (string, error) {
	root, err := volatileSpoolRoot(s.dir)
	if err != nil {
		return "", err
	}
	if err = privateDirectory(root); err != nil {
		return "", err
	}
	return root, nil
}

// Call only after the daemon owns its listener. A restart requires the saved copy again.
func (s *Store) CleanupVolatileSpools() error {
	root, err := volatileSpoolRoot(s.dir)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(root); os.IsNotExist(err) {
		return nil
	}
	if err = privateDirectory(root); err != nil {
		return err
	}
	return os.RemoveAll(root)
}

func (s *Store) ValidatedSpool(head, request, id string) (Spool, string, error) {
	item, root, release, err := s.acquireSpool(head, id, "SEALED", "CLAIMED")
	if err != nil {
		return Spool{}, "", err
	}
	defer release()
	if item.RequestID != request {
		return Spool{}, "", ErrSpoolInvalid
	}
	path, err := verifySpool(root, item)
	return item, path, err
}

func (s *Store) ReleaseSpool(head, id, job string) error {
	s.spoolMu.Lock()
	defer s.spoolMu.Unlock()
	root, err := s.spoolRoot()
	if err != nil {
		return err
	}
	item, err := readSpool(root, id)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || item.HeadID != head || item.ClaimedBy != "" && item.ClaimedBy != job || s.spoolActive[id] {
		return ErrSpoolInvalid
	}
	directory, err := spoolDirectory(root, id)
	if err != nil {
		return err
	}
	return os.RemoveAll(directory)
}

func spoolDirectory(root, id string) (string, error) {
	if !spoolID.MatchString(id) {
		return "", ErrSpoolInvalid
	}
	path := filepath.Join(root, id)
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !spoolDirectoryOwned(info) {
		return "", ErrSpoolInvalid
	}
	return path, nil
}

func readSpool(root, id string) (Spool, error) {
	dir, err := spoolDirectory(root, id)
	if err != nil {
		return Spool{}, err
	}
	file, err := openSpoolRegular(filepath.Join(dir, "metadata.json"))
	if err != nil {
		return Spool{}, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, 8193))
	if err != nil || len(body) > 8192 {
		return Spool{}, ErrSpoolInvalid
	}
	var item Spool
	if json.Unmarshal(body, &item) != nil || item.ID != id || !safeJobID.MatchString(item.HeadID) || !safeJobID.MatchString(item.RequestID) ||
		item.Filename != SpoolFilename || item.Size <= 0 || item.Size > MaximumSpoolBytes || !spoolHash.MatchString(item.SHA256) ||
		(item.State != "INCOMPLETE" && item.State != "SEALED" && item.State != "CLAIMED") {
		return Spool{}, ErrSpoolInvalid
	}
	return item, nil
}

func writeSpool(root string, item Spool) error {
	dir, err := spoolDirectory(root, item.ID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(item)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, "metadata-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(body); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	target := filepath.Join(dir, "metadata.json")
	if info, err := os.Lstat(target); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return ErrSpoolInvalid
	}
	if err = os.Rename(name, target); err != nil {
		return err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (s *Store) spools(root string) ([]Spool, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	if len(entries) > 1024 {
		return nil, ErrSpoolQuota
	}
	result := []Spool{}
	for _, entry := range entries {
		if !spoolID.MatchString(entry.Name()) {
			return nil, ErrSpoolInvalid
		}
		item, err := readSpool(root, entry.Name())
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *Store) CreateSpool(head, request, filename string, size int64, sha string, now time.Time) (Spool, error) {
	if !safeJobID.MatchString(head) || !safeJobID.MatchString(request) || filename != SpoolFilename || size <= 0 || size > MaximumSpoolBytes || !spoolHash.MatchString(sha) {
		return Spool{}, ErrSpoolInvalid
	}
	s.spoolMu.Lock()
	defer s.spoolMu.Unlock()
	root, err := s.spoolRoot()
	if err != nil {
		return Spool{}, err
	}
	if err = s.pruneSpools(root, now); err != nil {
		return Spool{}, err
	}
	items, err := s.spools(root)
	if err != nil {
		return Spool{}, err
	}
	var reserved, actual int64
	for _, item := range items {
		if item.HeadID == head && item.RequestID == request {
			if item.Size != size || item.SHA256 != sha || item.Filename != filename {
				return Spool{}, ErrSpoolInvalid
			}
			return item, nil
		}
		reserved += item.Size
		if info, err := os.Lstat(filepath.Join(root, item.ID, SpoolFilename)); err == nil && info.Mode().IsRegular() {
			actual += info.Size()
		}
	}
	quota := s.spoolQuota
	if quota == 0 {
		quota = SpoolQuotaBytes
	}
	if len(items) >= 32 || reserved+size > quota {
		return Spool{}, ErrSpoolQuota
	}
	free, err := spoolAvailableBytes(root)
	if err != nil {
		return Spool{}, err
	}
	if free < size+reserved-actual+64*1024*1024 {
		return Spool{}, ErrSpoolQuota
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return Spool{}, err
	}
	item := Spool{ID: hex.EncodeToString(random), HeadID: head, RequestID: request, Filename: filename, Size: size, SHA256: sha,
		State: "INCOMPLETE", CreatedAt: now.UTC(), ExpiresAt: now.Add(time.Hour).UTC()}
	if err = os.Mkdir(filepath.Join(root, item.ID), 0700); err != nil {
		return Spool{}, err
	}
	if err = writeSpool(root, item); err != nil {
		return Spool{}, err
	}
	return item, nil
}

func (s *Store) acquireSpool(head, id string, states ...string) (Spool, string, func(), error) {
	s.spoolMu.Lock()
	defer s.spoolMu.Unlock()
	root, err := s.spoolRoot()
	if err != nil {
		return Spool{}, "", nil, err
	}
	item, err := readSpool(root, id)
	if err != nil || item.HeadID != head {
		return Spool{}, "", nil, ErrSpoolInvalid
	}
	allowed := false
	for _, state := range states {
		allowed = allowed || state == item.State
	}
	if !allowed || item.ClaimedBy == "" && !item.ExpiresAt.After(time.Now()) {
		return Spool{}, "", nil, ErrSpoolInvalid
	}
	if s.spoolActive == nil {
		s.spoolActive = map[string]bool{}
	}
	if s.spoolActive[id] || len(s.spoolActive) >= 2 {
		return Spool{}, "", nil, ErrSpoolBusy
	}
	s.spoolActive[id] = true
	release := func() { s.spoolMu.Lock(); delete(s.spoolActive, id); s.spoolMu.Unlock() }
	return item, root, release, nil
}

// The caller supplies a cancellable reader with a per-read no-progress deadline.
func (s *Store) UploadSpool(ctx context.Context, head, id string, input io.Reader, length int64) error {
	item, root, release, err := s.acquireSpool(head, id, "INCOMPLETE")
	if err != nil {
		return err
	}
	defer release()
	if length != item.Size {
		return ErrSpoolInvalid
	}
	dir, _ := spoolDirectory(root, id)
	target := filepath.Join(dir, SpoolFilename)
	if info, err := os.Lstat(target); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return ErrSpoolInvalid
		}
		if err = os.Remove(target); err != nil {
			return err
		}
	}
	// Retry reuses the same reservation and replaces the incomplete file before streaming.
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	buffer := make([]byte, 1024*1024)
	var written int64
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		count, readErr := input.Read(buffer)
		if count > 0 {
			written += int64(count)
			if written > item.Size {
				return ErrSpoolInvalid
			}
			if _, err = file.Write(buffer[:count]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
		if count == 0 {
			return io.ErrNoProgress
		}
	}
	if written != item.Size {
		return io.ErrUnexpectedEOF
	}
	return file.Sync()
}

func verifySpool(root string, item Spool) (string, error) {
	dir, err := spoolDirectory(root, item.ID)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, SpoolFilename)
	file, err := openSpoolRegular(path)
	if err != nil {
		return "", ErrSpoolInvalid
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() != item.Size {
		return "", ErrSpoolInvalid
	}
	hash := sha256.New()
	size, err := io.CopyBuffer(hash, io.LimitReader(file, item.Size+1), make([]byte, 1024*1024))
	if err != nil || size != item.Size || hex.EncodeToString(hash.Sum(nil)) != item.SHA256 {
		return "", ErrSpoolInvalid
	}
	return path, file.Sync()
}

func (s *Store) SealSpool(head, id string) (Spool, error) {
	item, root, release, err := s.acquireSpool(head, id, "INCOMPLETE", "SEALED")
	if err != nil {
		return Spool{}, err
	}
	defer release()
	path, err := verifySpool(root, item)
	if err != nil {
		return Spool{}, err
	}
	if err = os.Chmod(path, 0400); err != nil {
		return Spool{}, err
	}
	item.State = "SEALED"
	if err = writeSpool(root, item); err != nil {
		return Spool{}, err
	}
	return item, nil
}

func (s *Store) ClaimSpool(head, request, id, job string) (string, error) {
	if !safeJobID.MatchString(job) {
		return "", ErrSpoolInvalid
	}
	item, root, release, err := s.acquireSpool(head, id, "SEALED", "CLAIMED")
	if err != nil {
		return "", err
	}
	defer release()
	if item.RequestID != request || item.ClaimedBy != "" && item.ClaimedBy != job {
		return "", ErrSpoolInvalid
	}
	path, err := verifySpool(root, item)
	if err != nil {
		return "", err
	}
	item.ClaimedBy = job
	item.State = "CLAIMED"
	if err = writeSpool(root, item); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Store) pruneSpools(root string, now time.Time) error {
	items, err := s.spools(root)
	if err != nil {
		return err
	}
	for _, item := range items {
		if s.spoolActive[item.ID] {
			continue
		}
		expired := item.ClaimedBy == "" && !item.ExpiresAt.After(now)
		if item.ClaimedBy != "" {
			job, exists := s.Get(item.ClaimedBy)
			// A completed or failed transfer never becomes server-side backup retention.
			expired = exists && job.FinishedAt != nil
		}
		if expired {
			dir, err := spoolDirectory(root, item.ID)
			if err != nil {
				return err
			}
			if !strings.HasPrefix(dir, root+string(os.PathSeparator)) {
				return ErrSpoolInvalid
			}
			if err = os.RemoveAll(dir); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) PruneSpools(now time.Time) error {
	s.spoolMu.Lock()
	defer s.spoolMu.Unlock()
	root, err := s.spoolRoot()
	if err != nil {
		return err
	}
	return s.pruneSpools(root, now)
}
