package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
		body, err := os.ReadFile(filepath.Join(dir, "jobs", entry.Name()))
		if err != nil {
			continue
		}
		var job model.Job
		if json.Unmarshal(body, &job) == nil && job.ID != "" {
			store.jobs[job.ID] = job
		}
	}
	return store, nil
}

func (s *Store) Save(job model.Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, "jobs", job.ID+".json")
	if err := os.WriteFile(path+".tmp", append(body, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return err
	}
	s.jobs[job.ID] = job
	return nil
}

func (s *Store) Get(id string) (model.Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
