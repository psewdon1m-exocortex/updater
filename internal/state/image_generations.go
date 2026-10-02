package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ImageGenerations is independent of the bounded job history. Cleanup needs
// the previous deployment even after its job has been pruned.
func (s *Store) ImageGenerations() (map[string][]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.imageGenerationsLocked()
}

func (s *Store) imageGenerationsLocked() (map[string][]string, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, "image-generations.json"))
	if errors.Is(err, os.ErrNotExist) {
		return map[string][]string{}, nil
	}
	if err != nil || len(data) > 65536 {
		return nil, errors.New("image generation ledger is unavailable")
	}
	var generations map[string][]string
	if json.Unmarshal(data, &generations) != nil || generations == nil {
		return nil, errors.New("image generation ledger is invalid")
	}
	return generations, nil
}

func (s *Store) SaveImageGeneration(headID string, refs []string) error {
	if headID == "" || len(refs) == 0 {
		return errors.New("image generation is incomplete")
	}
	for _, ref := range refs {
		if strings.TrimSpace(ref) == "" || len(ref) > 512 {
			return errors.New("image generation reference is invalid")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.imageGenerationsLocked()
	if err != nil {
		return err
	}
	items[headID] = append([]string(nil), refs...)
	data, err := json.Marshal(items)
	if err != nil || len(data) > 65536 {
		return errors.New("image generation ledger exceeded its limit")
	}
	path := filepath.Join(s.dir, "image-generations.json")
	file, err := os.OpenFile(path+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
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
	directory, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	err = directory.Sync()
	_ = directory.Close()
	return err
}
