package query

import "strings"

// SubjectMatches reports whether a literal subject matches a NATS filter with * and > wildcards.
func SubjectMatches(filter, subject string) bool {
	ft := strings.Split(filter, ".")
	st := strings.Split(subject, ".")
	for i, f := range ft {
		if f == ">" {
			return len(st) > i
		}
		if i >= len(st) {
			return false
		}
		if f != "*" && f != st[i] {
			return false
		}
	}
	return len(ft) == len(st)
}

func matchesAny(filters []string, subject string) bool {
	if len(filters) == 0 {
		return true
	}
	for _, f := range filters {
		if SubjectMatches(f, subject) {
			return true
		}
	}
	return false
}
