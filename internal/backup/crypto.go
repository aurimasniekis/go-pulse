package backup

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
	"github.com/klauspost/compress/s2"
	"golang.org/x/crypto/hkdf"
)

// ParseRecipients parses age recipients (age1… X25519 keys), one per line or comma-separated; # comments are ignored.
func ParseRecipients(s string) ([]age.Recipient, error) {
	var out []age.Recipient
	for _, line := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == ',' }) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		r, err := age.ParseX25519Recipient(line)
		if err != nil {
			return nil, fmt.Errorf("backup: age recipient %q: %w", line, err)
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, errors.New("backup: no age recipients")
	}
	return out, nil
}

// ParseIdentities parses age identities (AGE-SECRET-KEY-1…), as in an age-keygen file.
func ParseIdentities(s string) ([]age.Identity, error) {
	ids, err := age.ParseIdentities(strings.NewReader(s))
	if err != nil {
		return nil, errors.New("backup: invalid age identity")
	}
	return ids, nil
}

// GenerateIdentity returns a new identity file (age-keygen format) and its recipient.
func GenerateIdentity() (identityFile, recipient string, err error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", "", err
	}
	r := id.Recipient().String()
	return "# public key: " + r + "\n" + id.String() + "\n", r, nil
}

// seal compresses (S2 block format) and encrypts an object; open reverses it. Every stored object goes through these.
func seal(rs []age.Recipient, plain []byte) ([]byte, error) {
	return encrypt(rs, s2.EncodeBetter(nil, plain))
}

func open(ids []age.Identity, cipher []byte) ([]byte, error) {
	b, err := decrypt(ids, cipher)
	if err != nil {
		return nil, err
	}
	out, err := s2.Decode(nil, b)
	if err != nil {
		return nil, ErrDecrypt
	}
	return out, nil
}

func encrypt(rs []age.Recipient, plain []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, rs...)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plain); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ErrDecrypt means the identity does not match or the object was modified.
var ErrDecrypt = errors.New("backup: cannot decrypt (wrong identity or damaged object)")

func decrypt(ids []age.Identity, cipher []byte) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(cipher), ids...)
	if err != nil {
		return nil, ErrDecrypt
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, ErrDecrypt
	}
	return b, nil
}

// NameKey derives the chunk-naming key from the master key. Chunk names are HMACs of their plaintext, so equal
// content dedupes across backups while the storage provider cannot test guesses of the content.
func NameKey(master []byte) ([]byte, error) {
	k := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, master, nil, []byte("pulse backup chunk names v1")), k); err != nil {
		return nil, err
	}
	return k, nil
}

func chunkName(key, data []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return hex.EncodeToString(m.Sum(nil))
}
