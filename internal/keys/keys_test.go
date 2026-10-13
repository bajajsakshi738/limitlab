package keys

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var (
	hashA = strings.Repeat("a", 64)
	hashB = strings.Repeat("b", 64)
)

func TestAppendAndLookup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")

	a, err := Append(path, Key{Hash: hashA, Plan: "free"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Append(path, Key{Hash: hashB, Plan: "pro"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != 1 || b.ID != 2 {
		t.Fatalf("ids %d, %d", a.ID, b.ID)
	}

	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 0600", info.Mode().Perm())
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".keys-*"))
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}

	s, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Lookup(context.Background(), hashB)
	if err != nil || got.Plan != "pro" {
		t.Fatalf("lookup: %+v %v", got, err)
	}
	if _, err := s.Lookup(context.Background(), strings.Repeat("c", 64)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing key: %v", err)
	}
	if p := s.Plans(); !p["free"] || !p["pro"] || len(p) != 2 {
		t.Fatalf("plans %v", p)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := LoadFile(filepath.Join(t.TempDir(), "nope.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v", err)
	}
}

func TestBadKeyListsAreRejected(t *testing.T) {
	cases := map[string][]Key{
		"zero id":        {{ID: 0, Hash: hashA}},
		"duplicate id":   {{ID: 1, Hash: hashA}, {ID: 1, Hash: hashB}},
		"duplicate hash": {{ID: 1, Hash: hashA}, {ID: 2, Hash: hashA}},
		"short hash":     {{ID: 1, Hash: "abc"}},
		"uppercase hash": {{ID: 1, Hash: strings.Repeat("A", 64)}},
	}
	for name, list := range cases {
		if _, err := NewFileStore(list); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
