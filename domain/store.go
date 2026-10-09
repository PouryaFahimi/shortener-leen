package domain

// URLRecord is the metadata shared by storage, service, and HTTP layers.
type URLRecord struct {
	URL       string `json:"url"`
	CreatedAt string `json:"created_at"`
}

// Store describes atomic allocation and lookup, independent of persistence.
// Create must reuse existing URLs and must not overwrite an occupied code.
// Durable implementations must commit a new record before returning success.
type Store interface {
	Create(rawURL string, generate func() (string, error)) (string, error)
	Get(code string) (URLRecord, error)
}
