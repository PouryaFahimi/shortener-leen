package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"example.com/url-shortener/domain"
)

// LinkService is the handler's consumer contract; the service implements it.
type LinkService interface {
	Shorten(string) (string, error)
	Resolve(string) (string, error)
	MetaData(string) (domain.URLRecord, error)
}

type URLHandler struct {
	service LinkService
	base    string
}

func NewURLHandler(service LinkService, base string) *URLHandler {
	return &URLHandler{service: service, base: strings.TrimRight(base, "/") + "/"}
}

type ShortenRequest struct {
	URL string `json:"url"`
}

type ShortenResponse struct {
	Code     string `json:"code"`
	ShortURL string `json:"short_url"`
}

func (h *URLHandler) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/shorten", h.Shorten)
	mux.HandleFunc("/api/v1/links/{code}", h.MetaData)
	mux.HandleFunc("/{code}", h.Redirect)
	return mux
}

func (h *URLHandler) Shorten(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var request ShortenRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	// Require one JSON value; reject additional objects and malformed suffixes.
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	code, err := h.service.Shorten(request.URL)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ShortenResponse{Code: code, ShortURL: h.base + code})
}

func (h *URLHandler) Redirect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	code := r.PathValue("code")
	target, err := h.service.Resolve(code)
	if err != nil {
		writeDomainError(w, err)
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (h *URLHandler) MetaData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	response, err := h.service.MetaData(r.PathValue("code"))
	if err != nil {
		writeDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func writeDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidURL):
		writeError(w, http.StatusBadRequest, "url is invalid")
	case errors.Is(err, domain.ErrNotFound):
		writeError(w, http.StatusNotFound, "link not found")
	default:
		writeError(w, http.StatusInternalServerError, "server error")
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, struct {
		Error string `json:"error"`
	}{Error: message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		// Headers are already committed; a second HTTP error would corrupt the
		// response. Do not log submitted URLs or backend error text.
		log.Print("Failed to write JSON response")
	}
}
