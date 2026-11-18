package repos

import (
	"bytes"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// gitlabFormatMaxEdits bounds the line diff preserveGitLabFormatting runs
// between the original file and the re-encoded output. The diff's memory
// grows with the square of the number of differing lines, so files whose
// re-encoding differs on more lines than this fall back to the plain
// encoder output rather than spending unbounded time and memory.
const gitlabFormatMaxEdits = 2000

// renderGitLabDocuments encodes docs, which were decoded from original and
// then edited, while keeping original's formatting where possible.
// yaml.v3 keeps comments across a decode/encode round trip but drops
// blank lines and may normalize indentation and quoting, which used to turn
// every rewrite of a user-owned .gitlab-ci.yml into a reformat of the
// whole file (#8688). See preserveGitLabFormatting.
func renderGitLabDocuments(original []byte, docs []*yaml.Node) ([]byte, error) {
	encoded, err := marshalGitLabDocuments(docs)
	if err != nil {
		return nil, err
	}
	return preserveGitLabFormatting(original, encoded), nil
}

// preserveGitLabFormatting reconciles encoded (the yaml.v3 encoding of an
// edited copy of original) with original's formatting:
//
//   - When encoded is semantically identical to original (same values and
//     comments), original is returned byte-for-byte, so a merge that adds
//     nothing never produces a whitespace-only change.
//   - Otherwise, original's blank lines are re-inserted into encoded at the
//     positions a line diff between the two files places them. The result
//     is used only when it still decodes to exactly the same documents as
//     encoded; if re-inserting a blank line would change meaning (for
//     example inside a block scalar), encoded is returned unchanged.
func preserveGitLabFormatting(original, encoded []byte) []byte {
	if gitlabYAMLEquivalent(original, encoded) {
		return original
	}
	restored, ok := restoreBlankLines(original, encoded)
	if !ok || !gitlabYAMLEquivalent(restored, encoded) {
		return encoded
	}
	return restored
}

// gitlabYAMLEquivalent reports whether a and b decode to documents that
// yaml.v3 encodes identically, i.e. they differ at most in formatting the
// encoder normalizes (blank lines, quoting, indentation, line endings).
func gitlabYAMLEquivalent(a, b []byte) bool {
	docsA, err := decodeGitLabDocuments(a)
	if err != nil {
		return false
	}
	docsB, err := decodeGitLabDocuments(b)
	if err != nil {
		return false
	}
	encA, err := marshalGitLabDocuments(docsA)
	if err != nil {
		return false
	}
	encB, err := marshalGitLabDocuments(docsB)
	if err != nil {
		return false
	}
	return bytes.Equal(encA, encB)
}

// splitYAMLLines splits content into lines without their terminators,
// accepting both LF and CRLF line endings.
func splitYAMLLines(content []byte) []string {
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func isBlankYAMLLine(line string) bool {
	return strings.TrimSpace(line) == ""
}

// restoreBlankLines returns encoded with the blank lines of original that
// the encoder dropped put back. Lines common to both files (ignoring
// indentation) are aligned with a longest-common-subsequence diff; each run of differing lines between
// two aligned lines is a "gap" of original-only lines and encoded-only
// lines. The encoded-only lines are all kept, the original-only non-blank
// lines are dropped (they were rewritten or removed by the edit), and the
// original-only blank lines are placed among the encoded-only lines:
//
//   - a blank line with no rewritten original line after it in its gap
//     goes after all of the gap's encoded-only lines, so a blank line that
//     separated a block from the next one still does when the edit
//     appended to the block;
//   - any other blank line keeps its position relative to the rewritten
//     lines around it, so a re-indented block keeps its internal spacing.
//
// ok is false when the files differ on too many lines to diff cheaply.
func restoreBlankLines(original, encoded []byte) (result []byte, ok bool) {
	origLines := splitYAMLLines(original)
	encLines := splitYAMLLines(encoded)
	matches, ok := diffLineMatches(origLines, encLines, gitlabFormatMaxEdits)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(origLines)+len(encLines))
	nextOrig, nextEnc := 0, 0
	matches = append(matches, [2]int{len(origLines), len(encLines)})
	for _, m := range matches {
		out = appendGapLines(out, origLines[nextOrig:m[0]], encLines[nextEnc:m[1]])
		if m[1] < len(encLines) {
			out = append(out, encLines[m[1]])
		}
		nextOrig, nextEnc = m[0]+1, m[1]+1
	}
	return []byte(strings.Join(out, "\n") + "\n"), true
}

// appendGapLines appends the lines of one diff gap to out: every
// encoded-only line in order, with original-only blank lines interleaved as
// described on restoreBlankLines.
func appendGapLines(out, origGap, encGap []string) []string {
	// before[i] counts the blank lines to emit before encGap[i];
	// before[len(encGap)] those to emit after the last one.
	before := make([]int, len(encGap)+1)
	nonBlankSeen := 0
	for i, line := range origGap {
		if !isBlankYAMLLine(line) {
			nonBlankSeen++
			continue
		}
		pos := len(encGap)
		if hasNonBlankYAMLLine(origGap[i+1:]) {
			pos = min(nonBlankSeen, len(encGap))
		}
		before[pos]++
	}
	for i, line := range encGap {
		for range before[i] {
			out = append(out, "")
		}
		out = append(out, line)
	}
	for range before[len(encGap)] {
		out = append(out, "")
	}
	return out
}

func hasNonBlankYAMLLine(lines []string) bool {
	for _, line := range lines {
		if !isBlankYAMLLine(line) {
			return true
		}
	}
	return false
}

// diffLineMatches returns the index pairs (i, j) of a longest common
// subsequence of a and b, in increasing order, using Myers' O(ND) diff
// algorithm. Lines are compared without leading and trailing whitespace,
// so a line the encoder only re-indented still aligns with its original;
// callers always emit the encoded side of a matched pair. ok is false when more than maxEdits
// insertions and deletions are needed.
func diffLineMatches(a, b []string, maxEdits int) (matches [][2]int, ok bool) {
	ids := make(map[string]int)
	intern := func(lines []string) []int {
		out := make([]int, len(lines))
		for i, line := range lines {
			line = strings.TrimSpace(line)
			id, seen := ids[line]
			if !seen {
				id = len(ids)
				ids[line] = id
			}
			out[i] = id
		}
		return out
	}
	x, y := intern(a), intern(b)
	n, m := len(x), len(y)

	limit := min(n+m, maxEdits)
	off := limit + 1
	v := make([]int, 2*limit+3)
	// trace[d] is the window v[-d-1 .. d+1] as it stood before round d.
	var trace [][]int
	found := false
	for d := 0; d <= limit && !found; d++ {
		trace = append(trace, append([]int(nil), v[off-d-1:off+d+2]...))
		for k := -d; k <= d; k += 2 {
			var px int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				px = v[off+k+1]
			} else {
				px = v[off+k-1] + 1
			}
			py := px - k
			for px < n && py < m && x[px] == y[py] {
				px++
				py++
			}
			v[off+k] = px
			if px >= n && py >= m {
				found = true
				break
			}
		}
	}
	if !found {
		return nil, false
	}

	// Walk the trace backwards from (n, m), collecting diagonal moves.
	cx, cy := n, m
	for d := len(trace) - 1; d >= 0; d-- {
		window := trace[d]
		at := func(k int) int { return window[k+d+1] }
		k := cx - cy
		var prevK int
		if k == -d || (k != d && at(k-1) < at(k+1)) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := at(prevK)
		prevY := prevX - prevK
		for cx > prevX && cy > prevY {
			cx--
			cy--
			matches = append(matches, [2]int{cx, cy})
		}
		if d > 0 {
			cx, cy = prevX, prevY
		}
	}
	slices.Reverse(matches)
	return matches, true
}
