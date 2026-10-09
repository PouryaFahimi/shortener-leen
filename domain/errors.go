package domain

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrInvalidURL    = errors.New("invalid url")
	ErrEmptyURL      = fmt.Errorf("%w: empty url", ErrInvalidURL)
	ErrInvalidSchema = fmt.Errorf("%w: unsupported scheme", ErrInvalidURL)
	ErrCodeCollision = errors.New("code allocation exhausted")
)
