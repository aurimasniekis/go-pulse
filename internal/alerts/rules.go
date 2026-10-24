// Package alerts evaluates built-in and custom alert rules, keeps their state and history in SQLite, publishes
// `_sys.alert.<rule>` events and hands transitions to notifiers (PL-OBS-004/005, PL-AUTH-012, ADR-0033).
package alerts

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aurimasniekis/go-pulse/internal/notify"
	"github.com/aurimasniekis/go-pulse/internal/rollup"
)

// Rule kinds.
const (
	KindDiskForecast  = "disk_forecast"
	KindStreamUsage   = "stream_usage"
	KindCertExpiry    = "cert_expiry"
	KindBackupStale   = "backup_stale"
	KindClientExpiry  = "client_expiry"
	KindBanStorm      = "ban_storm"
	KindSlowConsumers = "slow_consumers"
	KindDeadLetters   = "dead_letters"
	KindNATSDown      = "nats_down"
	KindThreshold     = "threshold" // custom rules on rollups
)

// RuleParams are the tunables of a rule; each kind uses a subset (see builtins and Validate).
type RuleParams struct {
	Days          float64 `json:"days,omitempty" doc:"disk_forecast, cert_expiry, client_expiry: fire when less than this many days are left"`
	Ratio         float64 `json:"ratio,omitempty" doc:"stream_usage: fire above this fraction of max_bytes (0–1)"`
	Hours         float64 `json:"hours,omitempty" doc:"backup_stale: no successful backup for this long; dead_letters: look-back window"`
	Count         int     `json:"count,omitempty" doc:"ban_storm: bans within the window"`
	WindowMinutes int     `json:"window_minutes,omitempty" doc:"ban_storm, slow_consumers, threshold: evaluation window"`

	// threshold rules
	Stream    string  `json:"stream,omitempty" doc:"threshold: stream (default EVENTS)"`
	Dimension string  `json:"dimension,omitempty" enum:"total,type,prefix" doc:"threshold: what to count"`
	Key       string  `json:"key,omitempty" doc:"threshold: subject prefix (1–4 tokens) or ce-type; empty for total"`
	Metric    string  `json:"metric,omitempty" enum:"msgs,bytes" doc:"threshold: events or payload bytes"`
	Op        string  `json:"op,omitempty" enum:"above,below" doc:"threshold: fire when the count is above or below the threshold"`
	Threshold float64 `json:"threshold,omitempty"`
}

type builtin struct {
	kind, name, desc, severity string
	params                     RuleParams
	forSeconds                 int
}

var builtins = []builtin{
	{KindDiskForecast, "Disk forecast", "A stream reaches max_bytes within the given days at the current rate; after that its oldest events are discarded.",
		notify.Warning, RuleParams{Days: 30}, 0},
	{KindStreamUsage, "Stream usage", "A stream uses more than the given fraction of max_bytes, or JetStream more than that fraction of its storage limit. " +
		"For EVENTS (discard old) this stays firing once history fills max_bytes.", notify.Warning, RuleParams{Ratio: 0.85}, 0},
	{KindCertExpiry, "Certificate expiry", "A TLS certificate (internal leaf, Let's Encrypt, origin, AOP CA) expires within the given days.",
		notify.Critical, RuleParams{Days: 14}, 0},
	{KindBackupStale, "Backup stale", "The last backup failed or no backup succeeded within the given hours.", notify.Critical, RuleParams{Hours: 36}, 0},
	{KindClientExpiry, "Client credential expiry", "An active client's credentials expire within the given days.",
		notify.Warning, RuleParams{Days: 30}, 0},
	{KindBanStorm, "Ban storm", "At least the given number of bans within the window.", notify.Warning, RuleParams{Count: 10, WindowMinutes: 10}, 0},
	{KindSlowConsumers, "NATS slow consumers", "The NATS server reported new slow consumers within the window.", notify.Warning,
		RuleParams{WindowMinutes: 10}, 0},
	{KindDeadLetters, "Outbound dead letters", "An outbound webhook gave up on an event within the look-back window.", notify.Warning,
		RuleParams{Hours: 24}, 0},
	{KindNATSDown, "NATS connection lost", "pulsed has no connection to NATS.", notify.Critical, RuleParams{}, 60},
}

func builtinOf(kind string) (builtin, bool) {
	for _, b := range builtins {
		if b.kind == kind {
			return b, true
		}
	}
	return builtin{}, false
}

// Validate checks params for a kind and fills defaults for threshold rules.
func Validate(kind string, p *RuleParams, forSeconds int) error {
	if forSeconds < 0 || forSeconds > 7*24*3600 {
		return errors.New("for_seconds must be between 0 and 604800")
	}
	pos := func(name string, v float64) error {
		if v <= 0 {
			return fmt.Errorf("%s must be positive", name)
		}
		return nil
	}
	switch kind {
	case KindDiskForecast, KindCertExpiry, KindClientExpiry:
		return pos("days", p.Days)
	case KindStreamUsage:
		if p.Ratio <= 0 || p.Ratio >= 1 {
			return errors.New("ratio must be between 0 and 1")
		}
	case KindBackupStale, KindDeadLetters:
		return pos("hours", p.Hours)
	case KindBanStorm:
		if p.Count < 1 || p.WindowMinutes < 1 {
			return errors.New("count and window_minutes must be at least 1")
		}
	case KindSlowConsumers:
		if p.WindowMinutes < 1 {
			return errors.New("window_minutes must be at least 1")
		}
	case KindNATSDown:
	case KindThreshold:
		return validateThreshold(p)
	default:
		return fmt.Errorf("unknown rule kind %q", kind)
	}
	return nil
}

func validateThreshold(p *RuleParams) error {
	if p.Stream == "" {
		p.Stream = "EVENTS"
	}
	if p.Metric == "" {
		p.Metric = "msgs"
	}
	if p.Dimension == "" {
		p.Dimension = "total"
	}
	switch p.Metric {
	case "msgs", "bytes":
	default:
		return errors.New("metric must be msgs or bytes")
	}
	switch p.Op {
	case "above", "below":
	default:
		return errors.New("op must be above or below")
	}
	if p.Threshold < 0 {
		return errors.New("threshold must not be negative")
	}
	if p.WindowMinutes < 1 || p.WindowMinutes > 30*24*60 {
		return errors.New("window_minutes must be between 1 and 43200")
	}
	dim, _, err := thresholdSeries(*p)
	if err != nil {
		return err
	}
	_, err = resolutionFor(dim, time.Duration(p.WindowMinutes)*time.Minute)
	return err
}

// thresholdSeries resolves a threshold rule to the rollup dimension/key and the finest resolution that stores it
// and fits the window: prefixes of depth ≤ 2 are kept per minute, depth 3 per hour, depth 4 per day.
func thresholdSeries(p RuleParams) (dim, key string, err error) {
	switch p.Dimension {
	case "total":
		return rollup.DimTotal, "", nil
	case "type":
		if p.Key == "" {
			return "", "", errors.New("a type rule needs a key (ce-type or (untyped))")
		}
		return rollup.DimType, p.Key, nil
	case "prefix":
		k := strings.TrimSuffix(strings.TrimSuffix(p.Key, ".>"), ".")
		if k == "" || strings.ContainsAny(k, "*> ") {
			return "", "", errors.New("a prefix rule needs a literal subject prefix such as evt.home or evt.home.door")
		}
		depth := strings.Count(k, ".") + 1
		if depth > 4 {
			return "", "", errors.New("prefix rules support at most 4 subject tokens")
		}
		return rollup.PrefixDim(depth), k, nil
	default:
		return "", "", errors.New("dimension must be total, type or prefix")
	}
}

// resolutionFor picks the rollup table for a dimension and window.
func resolutionFor(dim string, window time.Duration) (rollup.Resolution, error) {
	depth := 0
	if strings.HasPrefix(dim, "p") {
		_, _ = fmt.Sscanf(dim, "p%d", &depth)
	}
	for _, r := range rollup.All {
		if depth > r.MaxDepth {
			continue
		}
		step := time.Duration(r.Step) * time.Second
		if window < step || window%step != 0 {
			return rollup.Resolution{}, fmt.Errorf("a %d-token prefix is counted per %s: the window must be a multiple of it", depth, step)
		}
		return r, nil
	}
	return rollup.Resolution{}, errors.New("no rollup resolution stores this dimension")
}
