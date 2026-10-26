package datastore

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// MaxKeyLen is the longest key a backend accepts, in bytes (the S3 limit).
const MaxKeyLen = 1024

// MaxComponentLen is the longest '/'-separated component of a key, in bytes (the
// limit of common filesystems; the repository's own keys are far shorter).
const MaxComponentLen = 255

// TempPrefix is reserved: the directory backend keeps in-flight writes under
// it, so no repository key starts with it.
const TempPrefix = "tmp/"

// ErrInvalidKey is returned for a key no backend stores.
var ErrInvalidKey = errors.New("datastore: invalid key")

// ValidKey checks a key: non-empty, at most MaxKeyLen bytes of valid UTF-8,
// '/'-separated components that are never empty, "." or ".." and at most
// MaxComponentLen bytes, no leading or
// trailing '/', no control characters, no backslash, and not under TempPrefix.
// Every backend applies it to every call, so a key can never address anything
// outside the repository, whatever the storage underneath.
func ValidKey(key string) error {
	if key == "" || len(key) > MaxKeyLen {
		return fmt.Errorf("%w: length %d", ErrInvalidKey, len(key))
	}
	if !utf8.ValidString(key) {
		return fmt.Errorf("%w: not UTF-8", ErrInvalidKey)
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return fmt.Errorf("%w: control or backslash character", ErrInvalidKey)
		}
	}
	for _, c := range strings.Split(key, "/") {
		if c == "" || c == "." || c == ".." || len(c) > MaxComponentLen {
			return fmt.Errorf("%w: bad path component in %q", ErrInvalidKey, key)
		}
	}
	if strings.HasPrefix(key, TempPrefix) {
		return fmt.Errorf("%w: %q is reserved", ErrInvalidKey, TempPrefix)
	}
	return nil
}

// validPrefix checks a listing prefix: like a key, but it may be empty, end in
// '/' and stop in the middle of a component.
func validPrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	if len(prefix) > MaxKeyLen || !utf8.ValidString(prefix) || strings.HasPrefix(prefix, "/") {
		return fmt.Errorf("%w: prefix %q", ErrInvalidKey, prefix)
	}
	for _, r := range prefix {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return fmt.Errorf("%w: control or backslash character", ErrInvalidKey)
		}
	}
	comps := strings.Split(strings.TrimSuffix(prefix, "/"), "/")
	for _, c := range comps {
		if c == "" || c == "." || c == ".." || len(c) > MaxComponentLen {
			return fmt.Errorf("%w: bad path component in prefix %q", ErrInvalidKey, prefix)
		}
	}
	if strings.HasPrefix(prefix, TempPrefix) {
		return fmt.Errorf("%w: %q is reserved", ErrInvalidKey, TempPrefix)
	}
	return nil
}

// ErrSize is returned by Put when the reader does not deliver exactly size bytes.
var ErrSize = errors.New("datastore: object size does not match the declared size")

// readExactly reads exactly size bytes from r, rejecting a short or long
// reader. It never holds more than the object (memory backend only).
func readExactly(r io.Reader, size int64) ([]byte, error) {
	if size < 0 {
		return nil, fmt.Errorf("%w: negative size", ErrSize)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("%w: reader ended early", ErrSize)
		}
		return nil, err
	}
	var one [1]byte
	if n, _ := r.Read(one[:]); n > 0 {
		return nil, fmt.Errorf("%w: reader holds more than %d bytes", ErrSize, size)
	}
	return buf, nil
}
