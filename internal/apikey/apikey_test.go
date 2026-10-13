package apikey

import (
	"strings"
	"testing"
)

func TestGenerateIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		k, err := Generate()
		if err != nil {
			t.Fatal(err)
		}
		if len(k) != Length {
			t.Fatalf("len %d, want %d", len(k), Length)
		}
		if !WellFormed(k) {
			t.Fatalf("generated key not well formed: %s", k)
		}
		if seen[k] {
			t.Fatal("duplicate key")
		}
		seen[k] = true
	}
}

func TestChecksumCatchesOneCharChange(t *testing.T) {
	k, _ := Generate()
	for i := len(Prefix); i < len(k); i++ {
		b := []byte(k)
		if b[i] == 'a' {
			b[i] = 'b'
		} else {
			b[i] = 'a'
		}
		if WellFormed(string(b)) {
			t.Fatalf("changing position %d was not caught", i)
		}
	}
}

func TestWellFormedRejects(t *testing.T) {
	k, _ := Generate()
	cases := map[string]string{
		"empty":        "",
		"short":        k[:len(k)-1],
		"long":         k + "a",
		"wrong prefix": "xx_" + k[3:],
		"bad char":     k[:5] + "-" + k[6:],
		"spaces":       " " + k[1:],
	}
	for name, in := range cases {
		if WellFormed(in) {
			t.Errorf("%s: accepted %q", name, in)
		}
	}
}

func TestHashAndDisplay(t *testing.T) {
	k, _ := Generate()
	if len(Hash(k)) != 64 {
		t.Fatal("hash should be 64 hex chars")
	}
	if Hash(k) == Hash(mangle(k)) {
		t.Fatal("different keys, same hash")
	}
	d := Display(k)
	if len(d) != DisplayLen || !strings.HasPrefix(k, d) {
		t.Fatalf("bad display %q", d)
	}
}

// mangle changes the last character so the key is almost but not quite right.
func mangle(key string) string {
	last := byte('x')
	if key[len(key)-1] == 'x' {
		last = 'y'
	}
	return key[:len(key)-1] + string(last)
}
