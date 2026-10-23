// Package rollup counts events per minute, hour and day by subject prefix and ce-type, from headers-only ordered
// consumers, and keeps the subject index (PL-ROLL-001..004, ADR-0020).
package rollup

import (
	"strconv"
	"strings"
)

// Dimensions.
const (
	DimTotal = "total"
	DimType  = "type"
)

// Untyped is the type key for events without ce-type.
const Untyped = "(untyped)"

// Resolution is a rollup table and the dimensions it keeps.
type Resolution struct {
	Table    string
	Step     int64 // seconds
	MaxDepth int   // deepest prefix dimension stored
}

// Resolutions, finest first. Finer tables keep fewer prefix depths to bound row counts.
var (
	Minute = Resolution{Table: "rollup_1m", Step: 60, MaxDepth: 2}
	Hour   = Resolution{Table: "rollup_1h", Step: 3600, MaxDepth: 3}
	Day    = Resolution{Table: "rollup_1d", Step: 86400, MaxDepth: 4}
	All    = []Resolution{Minute, Hour, Day}
)

// PrefixDim names the prefix dimension of a depth (1..4).
func PrefixDim(depth int) string { return "p" + strconv.Itoa(depth) }

// prefixes returns the subject prefixes of depth 1..max (fewer for short subjects).
func prefixes(subject string, max int) []string {
	out := make([]string, 0, max)
	for i, n := 0, 0; i <= len(subject) && n < max; i++ {
		if i == len(subject) || subject[i] == '.' {
			out = append(out, subject[:i])
			n++
		}
	}
	return out
}

// Depth returns the number of tokens in a subject.
func Depth(subject string) int { return strings.Count(subject, ".") + 1 }
