package storage

import (
	"fmt"
	"sync"
	"time"

	"example.com/url-shortener/domain"
)

// URLRecord remains available here for callers of the earlier storage API.
type URLRecord = domain.URLRecord

const maxCodeAttempts = 128

type MemoryStorage struct {
	lock       sync.RWMutex
	urls       map[string]domain.URLRecord
	codesByURL map[string]string
}

func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{
		urls:       make(map[string]domain.URLRecord),
		codesByURL: make(map[string]string),
	}
}

// Create holds one write lock across duplicate lookup, code allocation, and
// insertion; readers cannot observe partially updated indexes.
func (s *MemoryStorage) Create(rawURL string, generate func() (string, error)) (string, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if code, exists := s.codesByURL[rawURL]; exists {
		return code, nil
	}
	for attempt := 0; attempt < maxCodeAttempts; attempt++ {
		code, err := generate()
		if err != nil {
			return "", fmt.Errorf("generate code: %w", err)
		}
		if _, exists := s.urls[code]; exists {
			continue
		}
		s.urls[code] = domain.URLRecord{
			URL:       rawURL,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		s.codesByURL[rawURL] = code
		return code, nil
	}
	return "", fmt.Errorf("create link after %d attempts: %w", maxCodeAttempts, domain.ErrCodeCollision)
}

func (s *MemoryStorage) Get(code string) (domain.URLRecord, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()
	record, exists := s.urls[code]
	if !exists {
		return domain.URLRecord{}, fmt.Errorf("lookup code %q: %w", code, domain.ErrNotFound)
	}
	return record, nil
}
