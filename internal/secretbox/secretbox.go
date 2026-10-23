// Package secretbox seals small secrets (the key store) with XChaCha20-Poly1305 under a key derived from the master key.
//
// Format (ADR-0017): magic "PULSEKS1" ‖ version (1 byte) ‖ salt (32) ‖ nonce (24) ‖ ciphertext+tag.
// The AEAD key is HKDF-SHA256(master, salt, "pulse keystore v1"); the whole header is the AAD.
package secretbox

import (
	"bytes"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	magic    = "PULSEKS1"
	version  = 1
	saltLen  = 32
	info     = "pulse keystore v1"
	headerSz = len(magic) + 1 + saltLen + chacha20poly1305.NonceSizeX
)

// KeySize is the master key length in bytes.
const KeySize = 32

var (
	// ErrBadKey means the master key does not open the sealed data (wrong key or tampered file).
	ErrBadKey = errors.New("secretbox: master key does not open the sealed data (wrong key or corrupted file)")
	// ErrFormat means the data is not a sealed blob of a supported version.
	ErrFormat = errors.New("secretbox: not a PULSEKS1 sealed blob or unsupported version")
)

// ParseMasterKey decodes a 32-byte master key given as hex or base64 (standard or URL, padded or not).
func ParseMasterKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := hex.DecodeString(s); err == nil && len(b) == KeySize {
		return checkKey(b)
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil && len(b) == KeySize {
			return checkKey(b)
		}
	}
	return nil, fmt.Errorf("secretbox: master key must be %d bytes encoded as hex or base64", KeySize)
}

func checkKey(b []byte) ([]byte, error) {
	if bytes.Equal(b, make([]byte, KeySize)) {
		return nil, errors.New("secretbox: master key is all zeros")
	}
	return b, nil
}

// GenerateMasterKey returns a new random master key, hex encoded.
func GenerateMasterKey() string {
	b := make([]byte, KeySize)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func deriveKey(master, salt []byte) ([]byte, error) {
	if len(master) != KeySize {
		return nil, fmt.Errorf("secretbox: master key must be %d bytes", KeySize)
	}
	return hkdf.Key(sha256.New, master, salt, info, chacha20poly1305.KeySize)
}

// Seal encrypts plaintext under the master key with a fresh salt and nonce.
func Seal(master, plaintext []byte) ([]byte, error) {
	header := make([]byte, headerSz)
	copy(header, magic)
	header[len(magic)] = version
	if _, err := rand.Read(header[len(magic)+1:]); err != nil {
		return nil, err
	}
	salt := header[len(magic)+1 : len(magic)+1+saltLen]
	nonce := header[len(magic)+1+saltLen:]
	key, err := deriveKey(master, salt)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	return aead.Seal(header, nonce, plaintext, header), nil
}

// Open decrypts a blob produced by Seal.
func Open(master, blob []byte) ([]byte, error) {
	if len(blob) < headerSz+chacha20poly1305.Overhead || string(blob[:len(magic)]) != magic || blob[len(magic)] != version {
		return nil, ErrFormat
	}
	header := blob[:headerSz]
	salt := header[len(magic)+1 : len(magic)+1+saltLen]
	nonce := header[len(magic)+1+saltLen:]
	key, err := deriveKey(master, salt)
	if err != nil {
		return nil, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, nonce, blob[headerSz:], header)
	if err != nil {
		return nil, ErrBadKey
	}
	return pt, nil
}

// WriteFileAtomic writes data to path via a temp file in the same directory, fsync and rename.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
