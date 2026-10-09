package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"example.com/url-shortener/domain"
)

func openTestFile(t *testing.T, path string) *FileStorage {
	t.Helper()
	store, err := OpenFileStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestFileRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "data", "links.json")
	first := openTestFile(t, path)
	code, err := first.Create("https://example.com/path", fixed("abc123"))
	if err != nil {
		t.Fatal(err)
	}
	original, err := first.Get(code)
	if err != nil {
		t.Fatal(err)
	}
	second := openTestFile(t, path)
	loaded, err := second.Get(code)
	if err != nil || loaded != original {
		t.Fatalf("loaded=%v original=%v err=%v", loaded, original, err)
	}
	duplicate, err := second.Create(original.URL, func() (string, error) { t.Fatal("restart duplicate generated a code"); return "", nil })
	if err != nil || duplicate != code {
		t.Fatalf("duplicate=%q err=%v", duplicate, err)
	}
	if _, err := second.Get("unknown"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing=%v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state snapshot
	if err := json.Unmarshal(data, &state); err != nil || state.Version != 1 || len(state.Links) != 1 {
		t.Fatalf("snapshot=%v err=%v", state, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("snapshot mode: %v %v", info, err)
	}
}

func TestFileRejectsBadSnapshots(t *testing.T) {
	valid := `{"url":"https://example.com","created_at":"2026-01-01T00:00:00Z"}`
	for _, data := range []string{
		"", "{", `{"version":2,"links":{}}`, `{"version":1}`, `{"version":1,"links":{},"extra":true}`,
		`{"version":1,"links":{}} {}`, `{"version":1,"links":{"bad":` + valid + `}}`,
		`{"version":1,"links":{"bad/00":` + valid + `}}`,
		`{"version":1,"links":{"abc123":{"url":"ftp://example.com","created_at":"2026-01-01T00:00:00Z"}}}`,
		`{"version":1,"links":{"abc123":{"url":"no URL","created_at":"2026-01-01T00:00:00Z"}}}`,
		`{"version":1,"links":{"abc123":{"url":"https://example.com","created_at":"bad"}}}`,
		`{"version":1,"links":{"abc123":` + valid + `,"def456":` + valid + `}}`,
	} {
		t.Run(data, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "links.json")
			if err := os.WriteFile(path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenFileStorage(path); err == nil {
				t.Fatal("bad snapshot accepted")
			}
		})
	}
	if _, err := OpenFileStorage(""); err == nil {
		t.Fatal("empty path accepted")
	}
	parent := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(parent, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileStorage(filepath.Join(parent, "links.json")); err == nil {
		t.Fatal("file parent accepted")
	}
	if _, err := OpenFileStorage(t.TempDir()); err == nil {
		t.Fatal("directory snapshot accepted")
	}
}

func TestFileFailedCommitLeavesOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "links.json")
	store := openTestFile(t, path)
	if _, err := store.Create("https://first.example", fixed("abc123")); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("disk failure")
	commit := store.commit
	store.commit = func(snapshot) (bool, error) { return false, failure }
	if _, err := store.Create("https://second.example", fixed("def456")); !errors.Is(err, failure) {
		t.Fatalf("commit=%v", err)
	}
	if _, err := store.Get("def456"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("failed create became visible: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("failed write changed disk")
	}
	store.commit = commit
	if _, err := store.Create("https://second.example", fixed("def456")); err != nil {
		t.Fatal(err)
	}
}

func TestFileAmbiguousCommitFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "links.json")
	store := openTestFile(t, path)
	commit := store.commit
	failure := errors.New("directory sync failure")
	store.commit = func(state snapshot) (bool, error) {
		renamed, err := commit(state)
		if err != nil {
			return renamed, err
		}
		return true, failure
	}
	if _, err := store.Create("https://example.com", fixed("abc123")); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err := store.Get("abc123"); !errors.Is(err, failure) {
		t.Fatalf("failed store served data: %v", err)
	}
	if _, err := store.Create("https://another.example", fixed("def456")); !errors.Is(err, failure) {
		t.Fatalf("failed store accepted write: %v", err)
	}
	reopened := openTestFile(t, path)
	if record, err := reopened.Get("abc123"); err != nil || record.URL != "https://example.com" {
		t.Fatalf("recovery=%v %v", record, err)
	}
}

func TestFileAllocationErrors(t *testing.T) {
	store := openTestFile(t, filepath.Join(t.TempDir(), "links.json"))
	failure := errors.New("generator failed")
	if _, err := store.Create("https://example.com", func() (string, error) { return "", failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, err := store.Create("https://example.com", fixed("!")); err == nil {
		t.Fatal("invalid generated code accepted")
	}
	if _, err := store.Create("https://example.com", fixed("abc123")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("https://other.example", fixed("abc123")); !errors.Is(err, domain.ErrCodeCollision) {
		t.Fatalf("collision=%v", err)
	}
}

func TestSnapshotFilesystemFailures(t *testing.T) {
	state := snapshot{Version: 1, Links: map[string]domain.URLRecord{}}
	root := t.TempDir()
	if renamed, err := writeSnapshot(filepath.Join(root, "missing", "links.json"), state); err == nil || renamed {
		t.Fatal("missing directory accepted")
	}
	destination := filepath.Join(root, "destination")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	if renamed, err := writeSnapshot(destination, state); err == nil || renamed {
		t.Fatal("rename to directory succeeded")
	}
	tempFiles, err := filepath.Glob(filepath.Join(root, ".links-*.tmp"))
	if err != nil || len(tempFiles) != 0 {
		t.Fatalf("temporary files leaked: %v %v", tempFiles, err)
	}
}

func TestFileConcurrentCreateReadAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "links.json")
	store := openTestFile(t, path)
	var wg sync.WaitGroup
	failures := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			shared, err := store.Create("https://same.example", fixed("same00"))
			if err != nil || shared != "same00" {
				failures <- fmt.Errorf("shared=%q err=%v", shared, err)
				return
			}
			raw := fmt.Sprintf("https://example.com/%d", i)
			code, err := store.Create(raw, fixed(fmt.Sprintf("%06d", i)))
			if err != nil {
				failures <- err
				return
			}
			if record, err := store.Get(code); err != nil || record.URL != raw {
				failures <- fmt.Errorf("read=%v %v", record, err)
			}
		}(i)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	reopened := openTestFile(t, path)
	if len(reopened.memory.urls) != 21 || len(reopened.memory.codesByURL) != 21 {
		t.Fatal("restart lost concurrent records")
	}
}
