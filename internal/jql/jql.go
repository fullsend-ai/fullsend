// Package jql holds helpers for reading Jira Query Language strings.
package jql

import (
	"regexp"
	"strings"
)

// orderByRe finds a trailing ORDER BY clause, case-insensitively.
var orderByRe = regexp.MustCompile(`(?i)\border\s+by\b`)

// SplitOrderBy splits query at its last ORDER BY that is outside a
// quoted string, returning the predicate and the ORDER BY clause.
func SplitOrderBy(query string) (string, string) {
	query = strings.TrimSpace(query)
	quoted := QuotedRanges(query)
	matches := orderByRe.FindAllStringIndex(query, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		start := matches[i][0]
		if InRanges(start, quoted) {
			continue
		}
		return query[:start], strings.TrimSpace(query[start:])
	}
	return query, ""
}

// QuotedRanges returns the [start, end) byte ranges of single- or
// double-quoted strings in s, honoring backslash escapes.
func QuotedRanges(s string) [][2]int {
	var out [][2]int
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0 && c == '\\':
			i++
		case quote != 0 && c == quote:
			out = append(out, [2]int{start, i + 1})
			quote = 0
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
			start = i
		}
	}
	if quote != 0 {
		out = append(out, [2]int{start, len(s)})
	}
	return out
}

// InRanges reports whether pos lies inside one of ranges.
func InRanges(pos int, ranges [][2]int) bool {
	for _, r := range ranges {
		if pos >= r[0] && pos < r[1] {
			return true
		}
	}
	return false
}
