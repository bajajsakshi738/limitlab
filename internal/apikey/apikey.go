// Package apikey generates and checks API keys.
//
// A key looks like ll_<32 random base62 chars><6 char checksum>. The checksum
// is a CRC32 of the random part. It is NOT a security feature (anyone can
// compute it); it just lets us reject typos and garbage without a DB lookup.
package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"hash/crc32"
	"strings"
)

const (
	Prefix      = "ll_"
	randomLen   = 32
	checksumLen = 6
	Length      = len(Prefix) + randomLen + checksumLen

	// how much of the key we show in the UI and logs
	DisplayLen = 10
)

const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// Generate returns a new random key.
func Generate() (string, error) {
	random := make([]byte, 0, randomLen)
	buf := make([]byte, 64)
	for len(random) < randomLen {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			// 248 = 62*4. Dropping bytes >= 248 keeps every character equally
			// likely; b % 62 on the full 0-255 range would favour the first few.
			if b >= 248 {
				continue
			}
			random = append(random, alphabet[b%62])
			if len(random) == randomLen {
				break
			}
		}
	}
	return Prefix + string(random) + checksum(string(random)), nil
}

// WellFormed reports whether key has the right shape and checksum.
func WellFormed(key string) bool {
	if len(key) != Length || !strings.HasPrefix(key, Prefix) {
		return false
	}
	body := key[len(Prefix):]
	for i := 0; i < len(body); i++ {
		if strings.IndexByte(alphabet, body[i]) < 0 {
			return false
		}
	}
	random, sum := body[:randomLen], body[randomLen:]
	return checksum(random) == sum
}

// Hash is what we store. Keys have ~190 bits of randomness, so a plain
// SHA-256 is enough; a slow hash like bcrypt only matters for guessable
// secrets such as passwords.
func Hash(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

// Display returns the part of the key that is safe to show.
func Display(key string) string {
	if len(key) <= DisplayLen {
		return key
	}
	return key[:DisplayLen]
}

func checksum(s string) string {
	n := crc32.ChecksumIEEE([]byte(s))
	out := make([]byte, checksumLen)
	for i := checksumLen - 1; i >= 0; i-- {
		out[i] = alphabet[n%62]
		n /= 62
	}
	return string(out)
}
