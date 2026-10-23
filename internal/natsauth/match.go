package natsauth

import "strings"

// SubjectMatches reports whether a literal subject matches a pattern with * and > wildcards.
func SubjectMatches(pattern, subject string) bool {
	return Subset(subject, pattern)
}

// Subset reports whether every subject matched by filter is also matched by pattern. Literal subjects are a special
// case of filters, so this also answers "does pattern allow this subject".
func Subset(filter, pattern string) bool {
	ft, pt := strings.Split(filter, "."), strings.Split(pattern, ".")
	for i, p := range pt {
		if p == ">" {
			return len(ft) > i // pattern '>' needs at least one more token in the filter (a filter '>' also qualifies)
		}
		if i >= len(ft) {
			return false
		}
		f := ft[i]
		switch {
		case f == ">":
			return false // filter is wider than a non-'>' pattern token
		case p == "*":
			// '*' covers any single token, including a filter '*'
		case f == "*" || f != p:
			return false
		}
	}
	return len(ft) == len(pt)
}

// Allows reports whether grants permit action on subject (a literal subject, or a filter for subscribe). Each action
// stands alone, except that a durable grant also permits replaying its filter.
func Allows(grants []Grant, action Action, subject string) bool {
	for _, g := range grants {
		ok := g.Action == action || (action == ActSubscribe && g.Action == ActDurable)
		if ok && g.Subject != "" && Subset(subject, g.Subject) {
			return true
		}
	}
	return false
}
