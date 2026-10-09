package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"example.com/url-shortener/domain"
)

type snapshot struct {
	Version int                         `json:"version"`
	Links   map[string]domain.URLRecord `json:"links"`
}

// FileStorage supports one writer process per path. Each committed snapshot
// contains all records, so startup can rebuild the reverse index without drift.
type FileStorage struct {
	lock   sync.RWMutex
	memory *MemoryStorage
	commit func(snapshot) (bool, error)
	failed error
}

func OpenFileStorage(path string) (*FileStorage, error) {
	if path == "" {
		return nil, errors.New("storage path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("storage path: %w", err)
	}
	if err := makeDurableDirectory(filepath.Dir(absolute)); err != nil {
		return nil, fmt.Errorf("storage directory: %w", err)
	}
	memory := NewMemoryStorage()
	file, err := os.Open(absolute)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("open storage: %w", err)
	}
	if err == nil {
		defer file.Close()
		decoder := json.NewDecoder(file)
		decoder.DisallowUnknownFields()
		var state snapshot
		if err := decoder.Decode(&state); err != nil {
			return nil, fmt.Errorf("decode storage: %w", err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return nil, errors.New("storage contains trailing data")
		}
		if state.Version != 1 || state.Links == nil {
			return nil, errors.New("unsupported or incomplete storage snapshot")
		}
		for code, record := range state.Links {
			if !validCode(code) {
				return nil, errors.New("storage contains an invalid code")
			}
			target, err := url.ParseRequestURI(record.URL)
			if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
				return nil, errors.New("storage contains an invalid URL")
			}
			if _, err := time.Parse(time.RFC3339, record.CreatedAt); err != nil {
				return nil, fmt.Errorf("storage timestamp: %w", err)
			}
			if _, exists := memory.codesByURL[record.URL]; exists {
				return nil, errors.New("storage contains duplicate URLs")
			}
			memory.urls[code] = record
			memory.codesByURL[record.URL] = code
		}
	}
	return &FileStorage{memory: memory, commit: func(state snapshot) (bool, error) { return writeSnapshot(absolute, state) }}, nil
}

func validCode(code string) bool {
	if len(code) < 6 || len(code) > 8 {
		return false
	}
	for _, ch := range code {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9') {
			return false
		}
	}
	return true
}

func (s *FileStorage) Create(rawURL string, generate func() (string, error)) (string, error) {
	s.lock.Lock()
	defer s.lock.Unlock()
	if s.failed != nil {
		return "", s.failed
	}
	if code, exists := s.memory.codesByURL[rawURL]; exists {
		return code, nil
	}
	candidate := &MemoryStorage{urls: maps.Clone(s.memory.urls), codesByURL: maps.Clone(s.memory.codesByURL)}
	code, err := candidate.Create(rawURL, generate)
	if err != nil {
		return "", err
	}
	if !validCode(code) {
		return "", errors.New("generator returned an invalid code")
	}
	renamed, err := s.commit(snapshot{Version: 1, Links: candidate.urls})
	if err != nil {
		wrapped := fmt.Errorf("persist link: %w", err)
		if renamed {
			// The file was replaced but directory durability is uncertain.
			// Fail closed until restart rather than serve stale in-memory state.
			s.failed = wrapped
		}
		return "", wrapped
	}
	s.memory = candidate
	return code, nil
}

func (s *FileStorage) Get(code string) (domain.URLRecord, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()
	if s.failed != nil {
		return domain.URLRecord{}, s.failed
	}
	return s.memory.Get(code)
}

// Sync new directories and their nearest existing parent so a successful first
// commit does not depend on unsynced parent entries created by MkdirAll.
func makeDurableDirectory(path string) error {
	var missing []string
	existing := path
	for {
		info, err := os.Stat(existing)
		if err == nil {
			if !info.IsDir() {
				return errors.New("storage parent is not a directory")
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, existing)
		existing = filepath.Dir(existing)
	}
	if len(missing) == 0 {
		return nil
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	for _, directory := range append(missing, existing) {
		file, err := os.Open(directory)
		if err != nil {
			return err
		}
		err = file.Sync()
		file.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// The bool reports whether rename happened, allowing Create to distinguish an
// unchanged snapshot from an ambiguous commit after a directory sync failure.
func writeSnapshot(path string, state snapshot) (bool, error) {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return false, err
	}
	defer directory.Close()
	temp, err := os.CreateTemp(filepath.Dir(path), ".links-*.tmp")
	if err != nil {
		return false, err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if err := json.NewEncoder(temp).Encode(state); err != nil {
		return false, err
	}
	if err := temp.Sync(); err != nil {
		return false, err
	}
	if err := temp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return false, err
	}
	if err := directory.Sync(); err != nil {
		return true, err
	}
	return true, nil
}
