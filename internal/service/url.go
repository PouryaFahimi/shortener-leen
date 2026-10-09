package service

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"net/url"

	"example.com/url-shortener/domain"
)

type URLService struct {
	storage domain.Store
}

func NewURLService(store domain.Store) *URLService {
	return &URLService{storage: store}
}

func (s *URLService) Shorten(rawURL string) (string, error) {
	if rawURL == "" {
		return "", fmt.Errorf("shorten: %w", domain.ErrEmptyURL)
	}
	target, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return "", fmt.Errorf("shorten: %w: %v", domain.ErrInvalidURL, err)
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return "", fmt.Errorf("shorten: %w", domain.ErrInvalidSchema)
	}
	if target.Host == "" {
		return "", fmt.Errorf("shorten: %w: missing host", domain.ErrInvalidURL)
	}
	// Exact input strings define URL equality; no submitted URL is fetched.
	code, err := s.storage.Create(rawURL, generateCode)
	if err != nil {
		return "", fmt.Errorf("shorten: %w", err)
	}
	return code, nil
}

func (s *URLService) Resolve(code string) (string, error) {
	record, err := s.storage.Get(code)
	if err != nil {
		return "", fmt.Errorf("resolve: %w", err)
	}
	return record.URL, nil
}

func (s *URLService) MetaData(code string) (domain.URLRecord, error) {
	record, err := s.storage.Get(code)
	if err != nil {
		return domain.URLRecord{}, fmt.Errorf("metadata: %w", err)
	}
	return record, nil
}

func generateCode() (string, error) {
	const characters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	limit := big.NewInt(int64(len(characters)))
	code := make([]byte, 7)
	sum := 0
	for i := 0; i < 6; i++ {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("random character: %w", err)
		}
		code[i] = characters[n.Int64()]
		// Weighted checksum suffix; the lookup still requires the exact code.
		sum += (i + 1) * int(code[i])
	}
	// checksum
	code[6] = characters[sum%len(characters)]
	return string(code), nil
}
