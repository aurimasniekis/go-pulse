// Package audit writes and reads the append-only audit log (PL-AUD-001..003).
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
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

// Filter selects audit entries.
type Filter struct {
	Action   string // exact, or a prefix ending in "."
	Actor    string
	Target   string
	Result   string
	Since    time.Time
	Until    time.Time
	BeforeID int64
	Limit    int
}

// List returns entries newest first (PL-AUD-002).
func List(ctx context.Context, db *store.DB, f Filter) ([]Entry, error) {
	q := "SELECT id, ts, actor, action, target, ip, result, details FROM audit WHERE 1=1"
	var args []any
	if f.Action != "" {
		if strings.HasSuffix(f.Action, ".") {
			q += " AND action LIKE ? ESCAPE '\\'"
			args = append(args, strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(f.Action)+"%")
		} else {
			q += " AND action = ?"
			args = append(args, f.Action)
		}
	}
	for col, v := range map[string]string{"actor": f.Actor, "target": f.Target, "result": f.Result} {
		if v != "" {
			q += " AND " + col + " = ?"
			args = append(args, v)
		}
	}
	if !f.Since.IsZero() {
		q += " AND ts >= ?"
		args = append(args, store.Ms(f.Since))
	}
	if !f.Until.IsZero() {
		q += " AND ts < ?"
		args = append(args, store.Ms(f.Until))
	}
	if f.BeforeID > 0 {
		q += " AND id < ?"
		args = append(args, f.BeforeID)
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, f.Limit)
	rows, err := db.R().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var ts int64
		var details string
		if err := rows.Scan(&e.ID, &ts, &e.Actor, &e.Action, &e.Target, &e.IP, &e.Result, &details); err != nil {
			return nil, err
		}
		e.Time = store.FromMs(ts)
		_ = json.Unmarshal([]byte(details), &e.Details)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Prune deletes entries older than retention (PL-DB-005: 400 days).
func Prune(ctx context.Context, db *store.DB, retention time.Duration) error {
	return db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM audit WHERE ts < ?", store.Ms(time.Now().Add(-retention)))
		return err
	})
}
