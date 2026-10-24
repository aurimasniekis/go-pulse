package users

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// Keys are derived from the master key (HKDF-SHA256, distinct info strings): one MACs tokens, recovery codes and
// throttle-free lookups; the other seals TOTP secrets, OIDC client secrets and PKCE verifiers with
// XChaCha20-Poly1305 and a per-field AAD. Without the master key none of them can be checked or opened.
type Keys struct {
	mac  []byte
	seal []byte
}

// DeriveKeys derives the user-auth keys from the master key.
func DeriveKeys(master []byte) (*Keys, error) {
	if len(master) != 32 {
		return nil, fmt.Errorf("users: master key must be 32 bytes")
	}
	mac, err := hkdf.Key(sha256.New, master, nil, "pulse users mac v1", 32)
	if err != nil {
		return nil, err
	}
	seal, err := hkdf.Key(sha256.New, master, nil, "pulse users seal v1", chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	return &Keys{mac: mac, seal: seal}, nil
}

// MAC returns HMAC-SHA256 over domain ‖ "|" ‖ msg.
func (k *Keys) MAC(domain string, msg []byte) []byte {
	h := hmac.New(sha256.New, k.mac)
	h.Write([]byte(domain + "|"))
	h.Write(msg)
	return h.Sum(nil)
}

// Seal encrypts plaintext bound to aad: nonce ‖ ciphertext+tag.
func (k *Keys) Seal(aad, plaintext []byte) ([]byte, error) {
	a, err := chacha20poly1305.NewX(k.seal)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, a.NonceSize(), a.NonceSize()+len(plaintext)+a.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return a.Seal(nonce, nonce, plaintext, aad), nil
}

var errOpen = errors.New("users: sealed value does not open (wrong master key or tampered data)")

// Open decrypts a value sealed with the same aad.
func (k *Keys) Open(aad, blob []byte) ([]byte, error) {
	a, err := chacha20poly1305.NewX(k.seal)
	if err != nil {
		return nil, err
	}
	if len(blob) < a.NonceSize()+a.Overhead() {
		return nil, errOpen
	}
	pt, err := a.Open(nil, blob[:a.NonceSize()], blob[a.NonceSize():], aad)
	if err != nil {
		return nil, errOpen
	}
	return pt, nil
}
