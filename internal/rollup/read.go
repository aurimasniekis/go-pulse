package rollup

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Range is a named chart range (PL-ROLL-006) and the resolution that serves it.
type Range struct {
	Name     string
	Duration time.Duration
	Res      Resolution
}

// Ranges offered by the Overview.
var Ranges = map[string]Range{
	"1h":  {"1h", time.Hour, Minute},
	"24h": {"24h", 24 * time.Hour, Minute},
	"7d":  {"7d", 7 * 24 * time.Hour, Hour},
	"30d": {"30d", 30 * 24 * time.Hour, Hour},
	"1y":  {"1y", 365 * 24 * time.Hour, Day},
}

// Bounds returns the [from, to) bucket bounds for a range ending now.
func (r Range) Bounds(now time.Time) (int64, int64) {
	to := now.UTC().Unix()
	to = to - to%r.Res.Step + r.Res.Step
	from := to - int64(r.Duration/time.Second)
	return from - from%r.Res.Step, to
}

// Series returns zero-filled points for one dimension key over [from, to).
func (e *Engine) Series(ctx context.Context, stream string, res Resolution, dim, key string, from, to int64) ([]Point, error) {
	rows, err := e.db.R().QueryContext(ctx, fmt.Sprintf(`SELECT bucket, msgs, bytes FROM %s
		WHERE stream = ? AND dim = ? AND key = ? AND bucket >= ? AND bucket < ? ORDER BY bucket`, res.Table), stream, dim, key, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	got := map[int64]Point{}
	for rows.Next() {
		var p Point
		if err := rows.Scan(&p.T, &p.Msgs, &p.Bytes); err != nil {
			return nil, err
		}
		got[p.T] = p
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Point, 0, (to-from)/res.Step)
	for b := from; b < to; b += res.Step {
		p, ok := got[b]
		if !ok {
			p = Point{T: b}
		}
		out = append(out, p)
	}
	return out, nil
}

// KeyCount is one dimension key's total over a window.
type KeyCount struct {
	Key   string `json:"key"`
	Msgs  int64  `json:"msgs"`
	Bytes int64  `json:"bytes"`
}

// Top returns the keys of a dimension with the most messages in [from, to).
func (e *Engine) Top(ctx context.Context, stream string, res Resolution, dim string, from, to int64, limit int) ([]KeyCount, error) {
	rows, err := e.db.R().QueryContext(ctx, fmt.Sprintf(`SELECT key, SUM(msgs) AS m, SUM(bytes) FROM %s
		WHERE stream = ? AND dim = ? AND bucket >= ? AND bucket < ? GROUP BY key ORDER BY m DESC, key LIMIT ?`, res.Table),
		stream, dim, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []KeyCount{}
	for rows.Next() {
		var k KeyCount
		if err := rows.Scan(&k.Key, &k.Msgs, &k.Bytes); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// Sum totals one dimension key over [from, to).
func (e *Engine) Sum(ctx context.Context, stream string, res Resolution, dim, key string, from, to int64) (KeyCount, error) {
	k := KeyCount{Key: key}
	err := e.db.R().QueryRowContext(ctx, fmt.Sprintf(`SELECT COALESCE(SUM(msgs), 0), COALESCE(SUM(bytes), 0) FROM %s
		WHERE stream = ? AND dim = ? AND key = ? AND bucket >= ? AND bucket < ?`, res.Table), stream, dim, key, from, to).Scan(&k.Msgs, &k.Bytes)
	return k, err
}

// Forecast is the disk outlook for one stream (PL-ROLL-005, ADR-0021).
type Forecast struct {
	CurrentBytes  int64      `json:"current_bytes"`
	MaxBytes      int64      `json:"max_bytes"`
	UsedRatio     float64    `json:"used_ratio"`
	AvgDailyBytes int64      `json:"avg_daily_bytes"`
	SampleDays    float64    `json:"sample_days" doc:"How much history the average is based on"`
	HorizonDays   *float64   `json:"horizon_days,omitempty" doc:"max_bytes / average daily bytes"`
	FullAt        *time.Time `json:"full_at,omitempty" doc:"When max_bytes is reached at the current rate (absent once full or with no traffic)"`
	DiscardingOld bool       `json:"discarding_old" doc:"The stream is at its limit and drops its oldest events"`
}

// ComputeForecast derives the forecast from an average daily byte rate.
func ComputeForecast(current, maxBytes, avgDaily int64, sampleDays float64, now time.Time) Forecast {
	f := Forecast{CurrentBytes: current, MaxBytes: maxBytes, AvgDailyBytes: avgDaily, SampleDays: sampleDays}
	if maxBytes > 0 {
		f.UsedRatio = float64(current) / float64(maxBytes)
		f.DiscardingOld = current >= maxBytes*98/100
	}
	if avgDaily <= 0 || maxBytes <= 0 {
		return f
	}
	h := float64(maxBytes) / float64(avgDaily)
	f.HorizonDays = &h
	if current < maxBytes && !f.DiscardingOld {
		left := float64(maxBytes-current) / float64(avgDaily)
		t := now.Add(time.Duration(left * float64(24*time.Hour)))
		f.FullAt = &t
	}
	return f
}

// AverageDaily is the mean bytes/day over the last 30 days of committed rollups (minimum one hour of data).
func (e *Engine) AverageDaily(ctx context.Context, stream string) (int64, float64, error) {
	now := e.o.Now().UTC()
	from := now.Add(-30 * 24 * time.Hour).Unix()
	var first, bytes int64
	err := e.db.R().QueryRowContext(ctx, `SELECT COALESCE(MIN(bucket), 0), COALESCE(SUM(bytes), 0) FROM rollup_1h
		WHERE stream = ? AND dim = 'total' AND bucket >= ?`, stream, from).Scan(&first, &bytes)
	if err != nil || first == 0 {
		return 0, 0, err
	}
	span := float64(now.Unix()-first) / 86400
	if span < 1.0/24 {
		span = 1.0 / 24
	}
	return int64(float64(bytes) / span), span, nil
}

// Node is one level of the subject tree (PL-ROLL-007).
type Node struct {
	Token    string    `json:"token"`
	Path     string    `json:"path"`
	Leaf     bool      `json:"leaf" doc:"An exact subject exists at this path"`
	Children int       `json:"children" doc:"Distinct next tokens below this path"`
	Subjects int       `json:"subjects" doc:"Indexed subjects at or below this path"`
	Msgs     int64     `json:"msgs"`
	Bytes    int64     `json:"bytes"`
	LastSeen time.Time `json:"last_seen"`
	LastType string    `json:"last_type,omitempty"`
}

// Children lists the next level below prefix ("" for the roots), aggregated from the subject index.
func (e *Engine) Children(ctx context.Context, stream, prefix string) ([]Node, error) {
	q := `SELECT subject, msgs, bytes, last_seen, last_type FROM subjects WHERE stream = ?`
	args := []any{stream}
	if prefix != "" {
		q += ` AND (subject = ? OR subject LIKE ? ESCAPE '\')`
		args = append(args, prefix, likeEscape(prefix)+".%")
	}
	rows, err := e.db.R().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	nodes := map[string]*Node{}
	next := map[string]map[string]bool{}
	depth := 0
	if prefix != "" {
		depth = Depth(prefix)
	}
	for rows.Next() {
		var subj, typ string
		var msgs, bytes, last int64
		if err := rows.Scan(&subj, &msgs, &bytes, &last, &typ); err != nil {
			return nil, err
		}
		if subj == prefix {
			continue // the prefix itself is the parent
		}
		toks := strings.Split(subj, ".")
		tok := toks[depth]
		path := strings.Join(toks[:depth+1], ".")
		n := nodes[tok]
		if n == nil {
			n = &Node{Token: tok, Path: path}
			nodes[tok] = n
			next[tok] = map[string]bool{}
		}
		n.Subjects++
		n.Msgs += msgs
		n.Bytes += bytes
		if ls := time.Unix(last, 0).UTC(); ls.After(n.LastSeen) {
			n.LastSeen, n.LastType = ls, typ
		}
		if len(toks) == depth+1 {
			n.Leaf = true
		} else {
			next[tok][toks[depth+1]] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Node, 0, len(nodes))
	for tok, n := range nodes {
		n.Children = len(next[tok])
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Msgs > out[j].Msgs || (out[i].Msgs == out[j].Msgs && out[i].Token < out[j].Token)
	})
	return out, nil
}

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// SubjectInfo is one indexed subject.
type SubjectInfo struct {
	Subject   string     `json:"subject"`
	FirstSeen time.Time  `json:"first_seen"`
	LastSeen  time.Time  `json:"last_seen"`
	LastSeq   uint64     `json:"last_seq"`
	LastType  string     `json:"last_type"`
	Msgs      int64      `json:"msgs"`
	Bytes     int64      `json:"bytes"`
	JSONKeys  []string   `json:"json_keys"`
	KeysAt    *time.Time `json:"keys_sampled_at,omitempty"`
}

// Subject returns one indexed subject.
func (e *Engine) Subject(ctx context.Context, stream, subject string) (*SubjectInfo, error) {
	var s SubjectInfo
	var first, last, keysAt int64
	var keys string
	err := e.db.R().QueryRowContext(ctx, `SELECT subject, first_seen, last_seen, last_seq, last_type, msgs, bytes, json_keys, keys_sampled_at
		FROM subjects WHERE stream = ? AND subject = ?`, stream, subject).Scan(&s.Subject, &first, &last, &s.LastSeq, &s.LastType,
		&s.Msgs, &s.Bytes, &keys, &keysAt)
	if err != nil {
		return nil, err
	}
	s.FirstSeen, s.LastSeen = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
	_ = json.Unmarshal([]byte(keys), &s.JSONKeys)
	if s.JSONKeys == nil {
		s.JSONKeys = []string{}
	}
	if keysAt > 0 {
		t := time.Unix(keysAt, 0).UTC()
		s.KeysAt = &t
	}
	return &s, nil
}

// IndexStats reports the subject index size and overflow for a stream.
func (e *Engine) IndexStats(ctx context.Context, stream string) (indexed int, dropped int64, err error) {
	if err = e.db.R().QueryRowContext(ctx, "SELECT COUNT(*) FROM subjects WHERE stream = ?", stream).Scan(&indexed); err != nil {
		return
	}
	err = e.db.R().QueryRowContext(ctx, "SELECT COALESCE((SELECT dropped FROM subject_overflow WHERE stream = ?), 0)", stream).Scan(&dropped)
	return
}

// SubjectCap is the configured subject index cap.
func (e *Engine) SubjectCap() int { return e.o.SubjectCap }
