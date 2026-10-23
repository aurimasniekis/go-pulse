// Package audit writes and reads the append-only audit log (PL-AUD-001..003).
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/store"
)

// Actor is who performed an action.
type Actor struct {
	Name string // "admin" (auth none), an SSO subject later, "system", or "token:<id>"
	IP   string
}

// System is the actor for automatic actions.
var System = Actor{Name: "system"}

// Result values.
const (
	OK     = "ok"
	Denied = "denied"
	Error  = "error"
)

// Entry is one audit record. Details must never contain secrets (PL-AUD-003).
type Entry struct {
	ID      int64          `json:"id"`
	Time    time.Time      `json:"time"`
	Actor   string         `json:"actor"`
	Action  string         `json:"action"`
	Target  string         `json:"target"`
	IP      string         `json:"ip"`
	Result  string         `json:"result"`
	Details map[string]any `json:"details,omitempty"`
}

// Insert appends an entry inside the caller's transaction, so the audit record commits with the change it describes.
func Insert(ctx context.Context, tx *sql.Tx, a Actor, action, target, result string, details map[string]any) error {
	d := []byte("{}")
	if len(details) > 0 {
		b, err := json.Marshal(details)
		if err != nil {
			return err
		}
		d = b
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO audit(ts, actor, action, target, ip, result, details) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		store.Ms(store.Now()), a.Name, action, target, a.IP, result, string(d))
	return err
}

// Write appends an entry in its own transaction.
func Write(ctx context.Context, db *store.DB, a Actor, action, target, result string, details map[string]any) error {
	return db.Write(ctx, func(tx *sql.Tx) error { return Insert(ctx, tx, a, action, target, result, details) })
}
