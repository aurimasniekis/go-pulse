package rollup

import (
	"context"
	"sync"
	"time"
)

// minuteCache keeps per-stream minute totals for the last 48 h in RAM (PL-ROLL-003). It is rebuilt from rollup_1m at
// start and fed by the workers as messages arrive, so it can run slightly ahead of the committed rollups.
type minuteCache struct {
	mu sync.RWMutex
	m  map[string]map[int64]*counts
}

func newMinuteCache() *minuteCache { return &minuteCache{m: map[string]map[int64]*counts{}} }

func (c *minuteCache) add(stream string, bucket, size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.m[stream]
	if s == nil {
		s = map[int64]*counts{}
		c.m[stream] = s
	}
	b := s[bucket]
	if b == nil {
		b = &counts{}
		s[bucket] = b
		cut := bucket - int64(cacheWindow/time.Second)
		for k := range s {
			if k < cut {
				delete(s, k)
			}
		}
	}
	b.msgs++
	b.bytes += size
}

// Point is one time-series value.
type Point struct {
	T     int64 `json:"t"` // bucket start, unix seconds
	Msgs  int64 `json:"msgs"`
	Bytes int64 `json:"bytes"`
}

// Series returns minute points in [from, to) with zero-filled gaps.
func (c *minuteCache) Series(stream string, from, to int64) []Point {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := c.m[stream]
	from -= from % 60
	out := make([]Point, 0, (to-from)/60+1)
	for b := from; b < to; b += 60 {
		p := Point{T: b}
		if v := s[b]; v != nil {
			p.Msgs, p.Bytes = v.msgs, v.bytes
		}
		out = append(out, p)
	}
	return out
}

func (e *Engine) loadCache(ctx context.Context) error {
	since := e.o.Now().Add(-cacheWindow).Unix()
	rows, err := e.db.R().QueryContext(ctx, `SELECT stream, bucket, msgs, bytes FROM rollup_1m WHERE dim = 'total' AND bucket >= ?`, since)
	if err != nil {
		return err
	}
	defer rows.Close()
	e.cache.mu.Lock()
	defer e.cache.mu.Unlock()
	for rows.Next() {
		var st string
		var b, m, by int64
		if err := rows.Scan(&st, &b, &m, &by); err != nil {
			return err
		}
		s := e.cache.m[st]
		if s == nil {
			s = map[int64]*counts{}
			e.cache.m[st] = s
		}
		s[b] = &counts{msgs: m, bytes: by}
	}
	return rows.Err()
}
