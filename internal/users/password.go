package users

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
	"golang.org/x/text/unicode/norm"
)

// MaxPasswordBytes is the upper bound after normalization; longer input is rejected, never truncated.
const MaxPasswordBytes = 1024

// Params are versioned Argon2id parameters; hashes record theirs and are upgraded on login.
type Params struct {
	Version uint32
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
	KeyLen  uint32
	SaltLen int
}

// ParamSets lists every parameter version ever used.
var ParamSets = map[uint32]Params{
	// 46 MiB, t=1: OWASP's Argon2id minimum. pulsed runs in 448 MiB; HashConcurrency bounds parallel hashes.
	1: {Version: 1, Memory: 46 * 1024, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16},
}

// CurrentParams is the version used for new hashes.
var CurrentParams = ParamSets[1]

// NormalizePassword applies NFKC and enforces length bounds.
func NormalizePassword(pw string, minBytes int) ([]byte, error) {
	if len(pw) > 4*MaxPasswordBytes {
		return nil, Errf(KindInvalid, "password must be at most %d bytes", MaxPasswordBytes)
	}
	n := norm.NFKC.String(pw)
	switch {
	case strings.ContainsRune(n, 0):
		return nil, Errf(KindInvalid, "password must not contain NUL characters")
	case len(n) > MaxPasswordBytes:
		return nil, Errf(KindInvalid, "password must be at most %d bytes", MaxPasswordBytes)
	case len(n) < minBytes:
		return nil, Errf(KindInvalid, "password must be at least %d bytes", minBytes)
	}
	return []byte(n), nil
}

// HashPassword returns a PHC-format Argon2id hash: $argon2id$v=19$m=…,t=…,p=…$salt$hash.
func HashPassword(pw []byte, p Params) (string, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey(pw, salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Time, p.Threads, enc.EncodeToString(salt),
		enc.EncodeToString(key)), nil
}

var errBadHash = errors.New("malformed password hash")

// VerifyPassword checks pw against a PHC hash in constant time.
func VerifyPassword(pw []byte, phc string) (bool, error) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return false, errBadHash
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || t == 0 || p == 0 || m > 4*1024*1024 || t > 100 {
		return false, errBadHash
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false, errBadHash
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false, errBadHash
	}
	got := argon2.IDKey(pw, salt, t, m, p, uint32(len(want))) //nolint:gosec // bounded above
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash is verified for unknown accounts, so response time does not reveal whether an account exists.
func dummyHash() string {
	dummyMu.Lock()
	defer dummyMu.Unlock()
	if h, ok := dummyByVersion[CurrentParams.Version]; ok {
		return h
	}
	h, err := HashPassword([]byte("pulse-dummy-password-for-timing"), CurrentParams)
	if err != nil {
		panic(err)
	}
	dummyByVersion[CurrentParams.Version] = h
	return h
}

var (
	dummyMu        sync.Mutex
	dummyByVersion = map[uint32]string{}
)
