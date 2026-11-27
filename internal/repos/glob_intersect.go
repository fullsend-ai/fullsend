package repos

import (
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// globsMayIntersect reports whether two glob patterns can select a common
// path. It models the syntax of filepath.Match on Unix (`*` and `?` match
// any run of non-`/` characters, `[...]` classes with ranges and `^`
// negation, and `\` escapes) and decides intersection exactly by searching
// the product of the two patterns. A pattern it cannot parse is treated as
// possibly intersecting, so the answer errs toward reporting ambiguity,
// never toward hiding it.
func globsMayIntersect(a, b string) bool {
	ta, okA := parseGlob(strings.ToLower(a))
	tb, okB := parseGlob(strings.ToLower(b))
	if !okA || !okB {
		return true
	}

	// Splitting the rune space at every range boundary yields intervals
	// the tokens cannot tell apart, so one representative per interval is
	// enough to decide whether two tokens share a character.
	candidates := globCandidates(ta, tb)

	type state struct{ i, j int }
	seen := map[state]bool{}
	var walk func(i, j int) bool
	walk = func(i, j int) bool {
		s := state{i, j}
		if seen[s] {
			return false
		}
		seen[s] = true
		if i == len(ta) && j == len(tb) {
			return true
		}
		if i < len(ta) && ta[i].star && walk(i+1, j) {
			return true
		}
		if j < len(tb) && tb[j].star && walk(i, j+1) {
			return true
		}
		if i == len(ta) || j == len(tb) {
			return false
		}
		if !tokensShareRune(ta[i], tb[j], candidates) {
			return false
		}
		ni, nj := i+1, j+1
		if ta[i].star {
			ni = i
		}
		if tb[j].star {
			nj = j
		}
		return walk(ni, nj)
	}
	return walk(0, 0)
}

// globToken is one position of a parsed glob: a single character drawn from
// a set, or `*`, which repeats such a character any number of times.
type globToken struct {
	star    bool
	negated bool
	noSlash bool
	ranges  [][2]rune // sorted, non-overlapping
}

func (t globToken) matches(r rune) bool {
	if t.noSlash && r == '/' {
		return false
	}
	// ranges is sorted and non-overlapping (see mergeGlobRanges).
	i := sort.Search(len(t.ranges), func(i int) bool { return t.ranges[i][1] >= r })
	in := i < len(t.ranges) && t.ranges[i][0] <= r
	return in != t.negated
}

func tokensShareRune(a, b globToken, candidates []rune) bool {
	for _, r := range candidates {
		if a.matches(r) && b.matches(r) {
			return true
		}
	}
	return false
}

func globCandidates(sets ...[]globToken) []rune {
	out := []rune{0, '/', '/' + 1}
	for _, tokens := range sets {
		for _, t := range tokens {
			for _, rg := range t.ranges {
				out = append(out, rg[0])
				if rg[1] < unicode.MaxRune {
					out = append(out, rg[1]+1)
				}
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// parseGlob tokenizes a lowercased pattern. It reports false for a pattern
// filepath.Match would reject as malformed.
func parseGlob(p string) ([]globToken, bool) {
	var tokens []globToken
	for len(p) > 0 {
		switch p[0] {
		case '*':
			// Consecutive stars are equivalent to one.
			if n := len(tokens); n == 0 || !tokens[n-1].star {
				tokens = append(tokens, globToken{star: true, negated: true, noSlash: true})
			}
			p = p[1:]
		case '?':
			tokens = append(tokens, globToken{negated: true, noSlash: true})
			p = p[1:]
		case '[':
			tok, rest, ok := parseGlobClass(p[1:])
			if !ok {
				return nil, false
			}
			tokens = append(tokens, tok)
			p = rest
		default:
			r, rest, ok := globRune(p)
			if !ok {
				return nil, false
			}
			tokens = append(tokens, globToken{ranges: [][2]rune{{r, r}}})
			p = rest
		}
	}
	return tokens, true
}

// parseGlobClass parses the text after `[` up to and including `]`.
func parseGlobClass(p string) (globToken, string, bool) {
	tok := globToken{}
	var members [][2]rune
	if strings.HasPrefix(p, "^") {
		tok.negated = true
		p = p[1:]
	}
	for {
		if len(p) > 0 && p[0] == ']' && len(members) > 0 {
			tok.ranges = mergeGlobRanges(members)
			return tok, p[1:], true
		}
		lo, rest, ok := globClassRune(p)
		if !ok {
			return globToken{}, "", false
		}
		hi := lo
		if strings.HasPrefix(rest, "-") {
			hi, rest, ok = globClassRune(rest[1:])
			if !ok {
				return globToken{}, "", false
			}
		}
		members = append(members, [2]rune{lo, hi})
		p = rest
	}
}

// mergeGlobRanges sorts class members and merges overlapping or adjacent
// ranges, so a class with many repeated ranges stays cheap to evaluate. A
// reversed range such as z-a matches nothing and is dropped.
func mergeGlobRanges(members [][2]rune) [][2]rune {
	sorted := make([][2]rune, 0, len(members))
	for _, rg := range members {
		if rg[0] <= rg[1] {
			sorted = append(sorted, rg)
		}
	}
	slices.SortFunc(sorted, func(a, b [2]rune) int { return int(a[0]) - int(b[0]) })
	var out [][2]rune
	for _, rg := range sorted {
		if n := len(out); n > 0 && rg[0] <= out[n-1][1]+1 {
			out[n-1][1] = max(out[n-1][1], rg[1])
			continue
		}
		out = append(out, rg)
	}
	return out
}

// globClassRune reads one class member, which may be a `\` escape but not a
// bare `-` or `]`.
func globClassRune(p string) (rune, string, bool) {
	if len(p) == 0 || p[0] == '-' || p[0] == ']' {
		return 0, "", false
	}
	return globRune(p)
}

// globRune reads one literal character, resolving a `\` escape.
func globRune(p string) (rune, string, bool) {
	if p[0] == '\\' {
		p = p[1:]
		if len(p) == 0 {
			return 0, "", false
		}
	}
	r, n := utf8.DecodeRuneInString(p)
	if r == utf8.RuneError && n <= 1 {
		return 0, "", false
	}
	return r, p[n:], true
}
