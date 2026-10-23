package query

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// ErrCursor is returned for malformed cursors or cursors used with other filters (PL-EXP-004).
var ErrCursor = errors.New("invalid or mismatched cursor")

type cursor struct {
	Stream string    `json:"s"`
	Dir    Direction `json:"d"`
	Seq    uint64    `json:"q"`
	Hash   string    `json:"h"`
}

// filterHash binds a cursor to the query's filters so it cannot be replayed against different ones.
func filterHash(r *Request) string {
	subs := slices.Clone(r.Subjects)
	slices.Sort(subs)
	hk := make([]string, 0, len(r.Headers))
	for k, v := range r.Headers {
		hk = append(hk, k+"\x00"+v)
	}
	slices.Sort(hk)
	b, _ := json.Marshal([]any{r.Stream, subs, r.Type, hk, r.Contains, r.JSONPath, r.JSONValue})
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:8])
}

func encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s string, r *Request) (*cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, ErrCursor
	}
	var c cursor
	if err := json.Unmarshal(b, &c); err != nil || c.Seq == 0 {
		return nil, ErrCursor
	}
	if c.Stream != r.Stream || c.Dir != r.Dir || c.Hash != filterHash(r) {
		return nil, ErrCursor
	}
	return &c, nil
}
