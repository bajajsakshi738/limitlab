// Package keys looks up API keys by their hash.
package keys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrNotFound = errors.New("key not found")

type Key struct {
	ID      int64  `json:"id"`
	Hash    string `json:"hash"`
	Prefix  string `json:"prefix"`
	Owner   string `json:"owner"`
	Plan    string `json:"plan"`
	Revoked bool   `json:"revoked"`
}

type Store interface {
	Lookup(ctx context.Context, hash string) (Key, error)
}

// FileStore reads keys from a JSON file once at startup. It's a stand-in
// until the keys move to Postgres, and stays useful for tests. It is never
// written after loading, so it needs no lock.
type FileStore struct {
	byHash map[string]Key
}

// NewFileStore builds a store from list, rejecting anything that would
// make two keys share an ID (and so share a rate limit bucket).
func NewFileStore(list []Key) (*FileStore, error) {
	s := &FileStore{byHash: make(map[string]Key, len(list))}
	ids := make(map[int64]bool, len(list))
	for _, k := range list {
		switch {
		case k.ID <= 0:
			return nil, fmt.Errorf("key %q: id must be positive", k.Prefix)
		case ids[k.ID]:
			return nil, fmt.Errorf("duplicate key id %d", k.ID)
		case !isSHA256Hex(k.Hash):
			return nil, fmt.Errorf("key %d: hash must be 64 lowercase hex characters", k.ID)
		case s.byHash[k.Hash].ID != 0:
			return nil, fmt.Errorf("key %d: duplicate hash", k.ID)
		}
		ids[k.ID] = true
		s.byHash[k.Hash] = k
	}
	return s, nil
}

func LoadFile(path string) (*FileStore, error) {
	list, err := ReadFile(path)
	if err != nil {
		return nil, err
	}
	s, err := NewFileStore(list)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

func (s *FileStore) Lookup(_ context.Context, hash string) (Key, error) {
	k, ok := s.byHash[hash]
	if !ok {
		return Key{}, ErrNotFound
	}
	return k, nil
}

// Plans returns every plan name used by a key, so startup can check they
// all exist.
func (s *FileStore) Plans() map[string]bool {
	out := map[string]bool{}
	for _, k := range s.byHash {
		out[k.Plan] = true
	}
	return out
}

func ReadFile(path string) ([]Key, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var list []Key
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return list, nil
}

// Append adds k to the file (creating it if needed) and returns it with
// its new ID. It assumes one writer at a time, which is fine for a tool
// you run by hand.
func Append(path string, k Key) (Key, error) {
	list, err := ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Key{}, err
	}
	var maxID int64
	for _, existing := range list {
		maxID = max(maxID, existing.ID)
	}
	k.ID = maxID + 1
	list = append(list, k)
	if _, err := NewFileStore(list); err != nil {
		return Key{}, err
	}

	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return Key{}, err
	}
	if err := writeAtomic(path, append(b, '\n')); err != nil {
		return Key{}, err
	}
	return k, nil
}

// writeAtomic writes to a temp file and renames it over path, so a crash
// halfway through leaves the old file instead of a truncated one. (The
// directory isn't fsynced, so a power cut right after could still lose
// the new key; fine for a tool run by hand.)
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".keys-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed

	// CreateTemp makes the file 0600: hashes aren't secrets, but there's no
	// reason for anyone else to read them.
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
