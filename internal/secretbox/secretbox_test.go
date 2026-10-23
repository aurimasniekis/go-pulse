package secretbox

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	key, err := ParseMasterKey(GenerateMasterKey())
	if err != nil {
		t.Fatal(err)
	}
	pt := []byte(`{"operator":"SO..."}`)
	blob, err := Seal(key, pt)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, pt) {
		t.Fatal("plaintext visible in sealed blob")
	}
	got, err := Open(key, blob)
	if err != nil || !bytes.Equal(got, pt) {
		t.Fatalf("open: %v %q", err, got)
	}
	blob2, _ := Seal(key, pt)
	if bytes.Equal(blob, blob2) {
		t.Fatal("two seals must differ (fresh salt/nonce)")
	}
}

func TestOpenFailsClosed(t *testing.T) {
	key, _ := ParseMasterKey(GenerateMasterKey())
	other, _ := ParseMasterKey(GenerateMasterKey())
	blob, _ := Seal(key, []byte("secret"))

	if _, err := Open(other, blob); !errors.Is(err, ErrBadKey) {
		t.Fatalf("wrong key: %v", err)
	}
	for _, pos := range []int{0, len(magic), headerSz - 1, headerSz, len(blob) - 1} {
		mut := bytes.Clone(blob)
		mut[pos] ^= 1
		if _, err := Open(key, mut); err == nil {
			t.Fatalf("tamper at %d not detected", pos)
		}
	}
	if _, err := Open(key, blob[:headerSz]); !errors.Is(err, ErrFormat) {
		t.Fatalf("truncated: %v", err)
	}
}

func TestParseMasterKey(t *testing.T) {
	raw := bytes.Repeat([]byte{7}, KeySize)
	for _, s := range []string{hex.EncodeToString(raw), base64.StdEncoding.EncodeToString(raw), base64.RawURLEncoding.EncodeToString(raw), " " + hex.EncodeToString(raw) + "\n"} {
		got, err := ParseMasterKey(s)
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("%q: %v", s, err)
		}
	}
	for _, s := range []string{"", "abc", hex.EncodeToString(raw[:16]), hex.EncodeToString(make([]byte, KeySize))} {
		if _, err := ParseMasterKey(s); err == nil {
			t.Fatalf("%q accepted", s)
		}
	}
}

func TestWriteFileAtomic(t *testing.T) {
	p := filepath.Join(t.TempDir(), "keys.enc")
	if err := WriteFileAtomic(p, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	st, _ := os.Stat(p)
	if string(b) != "b" || st.Mode().Perm() != 0o600 {
		t.Fatalf("got %q mode %v", b, st.Mode())
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Fatalf("temp files left: %v", entries)
	}
}
