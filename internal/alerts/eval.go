package alerts

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/rollup"
	"github.com/aurimasniekis/go-pulse/internal/store"
)

type slowSample struct {
	at    time.Time
	count int64
}

// eval returns the findings of one rule. noData explains why a rule cannot run (its state is then kept).
func (e *Engine) eval(ctx context.Context, r *AlertRule, now time.Time) (findings []Finding, noData string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	p := r.Params
	switch r.Kind {
	case KindDiskForecast, KindStreamUsage:
		if e.p.JS == nil {
			return nil, "JetStream is not available", nil
		}
		return e.evalStreams(ctx, r.Kind, p, now)
	case KindCertExpiry:
		if e.p.Certs == nil {
			return nil, "no certificates are known", nil
		}
		for _, c := range e.p.Certs() {
			left := c.NotAfter.Sub(now)
			if left < time.Duration(p.Days*24*float64(time.Hour)) {
				days := left.Hours() / 24
				s := fmt.Sprintf("%s expires in %.1f days (%s)", c.Name, days, c.NotAfter.UTC().Format(time.RFC3339))
				if left <= 0 {
					s = fmt.Sprintf("%s expired at %s", c.Name, c.NotAfter.UTC().Format(time.RFC3339))
				}
				findings = append(findings, Finding{Key: c.Name, Value: math.Round(days*10) / 10, Summary: s})
			}
		}
		return findings, "", nil
	case KindBackupStale:
		if e.p.Backup == nil {
			return nil, "backups are not configured", nil
		}
		st, ok := e.p.Backup(ctx)
		if !ok {
			return nil, "backups are not configured", nil
		}
		limit := time.Duration(p.Hours * float64(time.Hour))
		age := now.Sub(st.LastSuccess)
		switch {
		case st.LastError != "":
			findings = append(findings, Finding{Value: age.Hours(), Summary: "last backup failed: " + st.LastError})
		case st.LastSuccess.IsZero():
			findings = append(findings, Finding{Summary: "no backup has succeeded yet"})
		case age > limit:
			findings = append(findings, Finding{Value: math.Round(age.Hours()*10) / 10,
				Summary: fmt.Sprintf("last successful backup was %.1f hours ago", age.Hours())})
		}
		return findings, "", nil
	case KindClientExpiry:
		return e.evalClients(ctx, p, now)
	case KindBanStorm:
		var n int
		if err := e.db.R().QueryRowContext(ctx, `SELECT COUNT(*) FROM bans WHERE created_at >= ?`,
			store.Ms(now.Add(-time.Duration(p.WindowMinutes)*time.Minute))).Scan(&n); err != nil {
			return nil, "", err
		}
		if n >= p.Count {
			findings = append(findings, Finding{Value: float64(n), Summary: fmt.Sprintf("%d bans in the last %d minutes", n, p.WindowMinutes)})
		}
		return findings, "", nil
	case KindSlowConsumers:
		return e.evalSlow(ctx, p, now)
	case KindDeadLetters:
		return e.evalDeadLetters(ctx, p, now)
	case KindNATSDown:
		if e.p.NATSConnected == nil {
			return nil, "no NATS connection to watch", nil
		}
		if !e.p.NATSConnected() {
			findings = append(findings, Finding{Value: 1, Summary: "pulsed is not connected to NATS"})
		}
		return findings, "", nil
	case KindThreshold:
		if e.p.Rollup == nil {
			return nil, "rollups are not available", nil
		}
		return e.evalThreshold(ctx, p, now)
	}
	return nil, "", fmt.Errorf("unknown kind %q", r.Kind)
}

func (e *Engine) evalStreams(ctx context.Context, kind string, p RuleParams, now time.Time) ([]Finding, string, error) {
	var out []Finding
	l := e.p.JS.ListStreams(ctx)
	for si := range l.Info() {
		maxB := si.Config.MaxBytes
		if maxB <= 0 {
			continue
		}
		cur := int64(si.State.Bytes) //nolint:gosec // stream bytes fit int64
		name := si.Config.Name
		switch kind {
		case KindStreamUsage:
			ratio := float64(cur) / float64(maxB)
			if ratio > p.Ratio {
				out = append(out, Finding{Key: name, Value: math.Round(ratio*1000) / 1000,
					Summary: fmt.Sprintf("stream %s uses %.1f%% of max_bytes", name, ratio*100)})
			}
		case KindDiskForecast:
			if e.p.Rollup == nil {
				continue
			}
			avg, days, err := e.p.Rollup.AverageDaily(ctx, name)
			if err != nil {
				return nil, "", err
			}
			f := rollup.ComputeForecast(cur, maxB, avg, days, now)
			if f.FullAt == nil {
				continue
			}
			// Compare in days: a large p.Days would overflow a time.Duration.
			if left := f.FullAt.Sub(now).Hours() / 24; left < p.Days {
				out = append(out, Finding{Key: name, Value: math.Round(left*10) / 10,
					Summary: fmt.Sprintf("stream %s reaches max_bytes in %.1f days at the current rate; older events will then be discarded", name, left)})
			}
		}
	}
	if err := l.Err(); err != nil {
		return nil, "", err
	}
	if kind == KindStreamUsage && e.p.Sys != nil {
		if jsz, err := e.p.Sys.Jsz(ctx); err == nil && jsz.Config.MaxStore > 0 {
			ratio := float64(jsz.Store) / float64(jsz.Config.MaxStore)
			if ratio > p.Ratio {
				out = append(out, Finding{Key: "jetstream", Value: math.Round(ratio*1000) / 1000,
					Summary: fmt.Sprintf("JetStream uses %.1f%% of its storage limit", ratio*100)})
			}
		}
	}
	return out, "", nil
}

func (e *Engine) evalClients(ctx context.Context, p RuleParams, now time.Time) ([]Finding, string, error) {
	rows, err := e.db.R().QueryContext(ctx, `SELECT name, expires_at FROM clients WHERE status = 'active' AND expires_at < ?`,
		store.Ms(now.Add(time.Duration(p.Days*24*float64(time.Hour)))))
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []Finding
	for rows.Next() {
		var name string
		var exp int64
		if err := rows.Scan(&name, &exp); err != nil {
			return nil, "", err
		}
		left := store.FromMs(exp).Sub(now).Hours() / 24
		s := fmt.Sprintf("client %s credentials expire in %.1f days; rotate them", name, left)
		if left <= 0 {
			s = fmt.Sprintf("client %s credentials have expired; rotate them", name)
		}
		out = append(out, Finding{Key: name, Value: math.Round(left*10) / 10, Summary: s})
	}
	return out, "", rows.Err()
}

func (e *Engine) evalSlow(ctx context.Context, p RuleParams, now time.Time) ([]Finding, string, error) {
	if e.p.Sys == nil {
		return nil, "NATS monitoring is not available", nil
	}
	v, err := e.p.Sys.Varz(ctx)
	if err != nil {
		return nil, "", err
	}
	window := time.Duration(p.WindowMinutes) * time.Minute
	e.mu.Lock()
	defer e.mu.Unlock()
	e.slow = append(e.slow, slowSample{at: now, count: v.SlowConsumers})
	// Keep samples back to the window start (plus the one just before it as the baseline).
	for len(e.slow) > 2 && now.Sub(e.slow[1].at) > window {
		e.slow = e.slow[1:]
	}
	base := e.slow[0]
	if base.count > v.SlowConsumers { // server restarted: counters reset
		e.slow = []slowSample{{at: now, count: v.SlowConsumers}}
		return nil, "", nil
	}
	if d := v.SlowConsumers - base.count; d > 0 {
		return []Finding{{Value: float64(d), Summary: fmt.Sprintf("%d new slow consumers in the last %d minutes", d, p.WindowMinutes)}}, "", nil
	}
	return nil, "", nil
}

func (e *Engine) evalDeadLetters(ctx context.Context, p RuleParams, now time.Time) ([]Finding, string, error) {
	rows, err := e.db.R().QueryContext(ctx, `SELECT COALESCE(h.name, d.hook_id), COUNT(*) FROM deliveries d LEFT JOIN hooks_out h ON h.id = d.hook_id
		WHERE d.outcome = 'dead' AND d.ts >= ? GROUP BY d.hook_id`, store.Ms(now.Add(-time.Duration(p.Hours*float64(time.Hour)))))
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []Finding
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, "", err
		}
		out = append(out, Finding{Key: name, Value: float64(n),
			Summary: fmt.Sprintf("outbound webhook %s gave up on %d event(s) in the last %g hours", name, n, p.Hours)})
	}
	return out, "", rows.Err()
}

func (e *Engine) evalThreshold(ctx context.Context, p RuleParams, now time.Time) ([]Finding, string, error) {
	dim, key, err := thresholdSeries(p)
	if err != nil {
		return nil, "", err
	}
	window := time.Duration(p.WindowMinutes) * time.Minute
	res, err := resolutionFor(dim, window)
	if err != nil {
		return nil, "", err
	}
	// Completed buckets only: the current bucket is still filling and would make "below" rules fire early.
	to := now.UTC().Unix()
	to -= to % res.Step
	from := to - int64(window/time.Second)
	kc, err := e.p.Rollup.Sum(ctx, p.Stream, res, dim, key, from, to)
	if err != nil {
		return nil, "", err
	}
	v := float64(kc.Msgs)
	unit := "events"
	if p.Metric == "bytes" {
		v, unit = float64(kc.Bytes), "bytes"
	}
	hit := (p.Op == "above" && v > p.Threshold) || (p.Op == "below" && v < p.Threshold)
	if !hit {
		return nil, "", nil
	}
	return []Finding{{Value: v, Summary: fmt.Sprintf("%g %s in the last %d minutes (%s %g)", v, unit, p.WindowMinutes, p.Op, p.Threshold)}}, "", nil
}
