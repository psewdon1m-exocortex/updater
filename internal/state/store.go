package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"updater/internal/model"
)

type Store struct {
	dir  string
	mu   sync.Mutex
	jobs map[string]model.Job
}

var safeJobID = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)

func normalizeCompletion(job model.Job) model.Job {
	switch job.State {
	case "COMPLETED", "ROLLED_BACK", "FAILED", "ROLLBACK_FAILED":
		if job.FinishedAt == nil {
			finished := job.UpdatedAt
			job.FinishedAt = &finished
		}
	default:
		job.FinishedAt = nil
	}
	return job
}

func New(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "jobs"), 0o750); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "backups"), 0o700); err != nil {
		return nil, err
	}
	store := &Store{dir: dir, jobs: map[string]model.Job{}}
	entries, _ := os.ReadDir(filepath.Join(dir, "jobs"))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, "jobs", entry.Name()))
		if err != nil {
			continue
		}
		var job model.Job
		if json.Unmarshal(body, &job) == nil && safeJobID.MatchString(job.ID) && entry.Name() == job.ID+".json" {
			store.jobs[job.ID] = normalizeCompletion(job)
		}
	}
	return store, nil
}

func (s *Store) Save(job model.Job) error {
	job = normalizeCompletion(job)
	if !safeJobID.MatchString(job.ID) {
		return errors.New("invalid job ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	body, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	if len(body) > 65536 {
		return errors.New("job exceeds persistence limit")
	}
	path := filepath.Join(s.dir, "jobs", job.ID+".json")
	file, err := os.OpenFile(path+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = file.Write(append(body, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	err = directory.Sync()
	_ = directory.Close()
	if err != nil {
		return err
	}
	s.jobs[job.ID] = job
	return nil
}

func (s *Store) Get(id string) (model.Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !safeJobID.MatchString(id) {
		return model.Job{}, false
	}
	// A supervised self-update completes in another process after daemon restart.
	if body, err := os.ReadFile(filepath.Join(s.dir, "jobs", id+".json")); err == nil && len(body) <= 65536 {
		var latest model.Job
		if json.Unmarshal(body, &latest) == nil && latest.ID == id {
			s.jobs[id] = normalizeCompletion(latest)
		}
	}
	job, ok := s.jobs[id]
	return job, ok
}

func (s *Store) ByRequestID(id string) (model.Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, job := range s.jobs {
		if job.RequestID == id {
			return job, true
		}
	}
	return model.Job{}, false
}

func (s *Store) List() []model.Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]model.Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		items = append(items, job)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	return items
}

func (s *Store) BackupPath(jobID, filename string) (string, error) {
	name := filepath.Base(filename)
	if name == "." || name == "" {
		return "", errors.New("backup filename is invalid")
	}
	dir := filepath.Join(s.dir, "backups", jobID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func (s *Store) Prune(maxJobs int, olderThan time.Time) error {
	if maxJobs <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]model.Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		items = append(items, job)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	terminal := func(state string) bool {
		switch state {
		case "COMPLETED", "ROLLED_BACK", "FAILED", "ROLLBACK_FAILED":
			return true
		default:
			return false
		}
	}
	for index, job := range items {
		if !terminal(job.State) || (index < maxJobs && !job.CreatedAt.Before(olderThan)) {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, "jobs", job.ID+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.RemoveAll(filepath.Join(s.dir, "backups", job.ID)); err != nil {
			return err
		}
		delete(s.jobs, job.ID)
	}
	return nil
}
