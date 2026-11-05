package repos

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// userGitLabCIWithBlankLines is a user-owned root .gitlab-ci.yml with
// blank lines between sections, comments next to blank lines, quoting
// yaml.v3 would normalize, and a block scalar holding a blank line.
const userGitLabCIWithBlankLines = `# Top comment

stages:
  - build
  - test

variables:
  FOO: "bar"

include:
  - local: other.yml

workflow:
  rules:
    - if: $CI_COMMIT_BRANCH

# Build job
build:
  stage: build
  script:
    - echo "hi"

    - echo 'there'

test:
  stage: test
  script: |
    line1

    line2
`

// assertLinesKeptInOrder asserts every line of original appears in out in
// the same order, i.e. a line diff from original to out has no deletions.
func assertLinesKeptInOrder(t *testing.T, original, out string) {
	t.Helper()
	outLines := strings.Split(out, "\n")
	next := 0
	for line := range strings.SplitSeq(original, "\n") {
		for next < len(outLines) && outLines[next] != line {
			next++
		}
		if !assert.Less(t, next, len(outLines), "original line %q missing or out of order in:\n%s", line, out) {
			return
		}
		next++
	}
}

func TestMergeGitLabCI_PreservesBlankLinesAndFormatting(t *testing.T) {
	result, err := MergeGitLabCI([]byte(userGitLabCIWithBlankLines))
	require.NoError(t, err)
	out := string(result)

	assert.Contains(t, out, fullsendPipelineInclude)
	assert.Contains(t, out, "  - dispatch\n")
	assertLinesKeptInOrder(t, userGitLabCIWithBlankLines, out)
	// Blank lines separating sections the merge appended to stay after
	// the appended entries.
	assert.Contains(t, out, "  - agent\n\nvariables:\n")
	assert.Contains(t, out, "  STAGE: $[[ inputs.stage ]]\n\ninclude:\n")
	assert.Contains(t, out, "on_new_commit: none\n\n# Build job\nbuild:\n")
	assert.Contains(t, out, "    line1\n\n    line2\n")

	again, err := MergeGitLabCI(result)
	require.NoError(t, err)
	assert.Equal(t, out, string(again), "merging an already-merged file must be byte-identical")
}

func TestMergeGitLabCI_NoChangeReturnsOriginalBytes(t *testing.T) {
	merged, err := MergeGitLabCI([]byte(userGitLabCIWithBlankLines))
	require.NoError(t, err)

	// Formatting the encoder would normalize — CRLF line endings and
	// double-quoted strings — must survive a merge that adds nothing.
	crlf := strings.ReplaceAll(strings.ReplaceAll(string(merged),
		"'.gitlab/ci/fullsend-pipeline.yml'", `".gitlab/ci/fullsend-pipeline.yml"`),
		"\n", "\r\n")
	again, err := MergeGitLabCI([]byte(crlf))
	require.NoError(t, err)
	assert.Equal(t, crlf, string(again))
}

func TestMergeGitLabCI_PreservesBlankLinesWithCRLF(t *testing.T) {
	crlf := strings.ReplaceAll(userGitLabCIWithBlankLines, "\n", "\r\n")
	result, err := MergeGitLabCI([]byte(crlf))
	require.NoError(t, err)
	assert.Contains(t, string(result), fullsendPipelineInclude)
	assertLinesKeptInOrder(t, userGitLabCIWithBlankLines, string(result))
}

func TestMergeGitLabCI_PreservesBlankLinesInSpecHeader(t *testing.T) {
	existing := `spec:
  inputs:
    stage:
      default: ''
      regex: '^[A-Za-z0-9_-]*$'

---

stages:
  - build

build:
  script: echo
`
	result, err := MergeGitLabCI([]byte(existing))
	require.NoError(t, err)
	out := string(result)
	assert.Contains(t, out, "    event_type:\n")
	assertLinesKeptInOrder(t, existing, out)

	again, err := MergeGitLabCI(result)
	require.NoError(t, err)
	assert.Equal(t, out, string(again))
}

func TestMergeGitLabCI_ReindentedFileKeepsBlankLines(t *testing.T) {
	existing := "include:\n    - local: other.yml\n\nworkflow:\n    rules:\n        - if: $X\n\n          when: always\n\nbuild:\n    script: echo\n"
	result, err := MergeGitLabCI([]byte(existing))
	require.NoError(t, err)
	out := string(result)
	assert.Contains(t, out, "    - if: $X\n\n      when: always\n")
	assert.Contains(t, out, "on_new_commit: none\n\nbuild:\n")
}

func TestUnmergeGitLabCI_PreservesBlankLines(t *testing.T) {
	merged, err := MergeGitLabCI([]byte(userGitLabCIWithBlankLines))
	require.NoError(t, err)
	result, err := UnmergeGitLabCI(merged)
	require.NoError(t, err)
	out := string(result)
	assert.NotContains(t, out, fullsendPipelineInclude)
	assert.Contains(t, out, "  - test\n\nvariables:\n")
	assert.Contains(t, out, "    - echo \"hi\"\n\n    - echo 'there'\n\ntest:\n")
}

func TestPreserveGitLabFormatting_FallsBackWhenBlankLineChangesMeaning(t *testing.T) {
	// The blank line trails its diff gap, so it would be placed after
	// the keep-chomped block scalar's content and become part of the
	// value; the encoded output must be returned instead.
	original := []byte("a: 1\n\nb: 2\n")
	encoded := []byte("a: |+\n  x\nb: 2\n")
	assert.Equal(t, string(encoded), string(preserveGitLabFormatting(original, encoded)))
}

func TestPreserveGitLabFormatting_InvalidOriginalUsesEncoded(t *testing.T) {
	encoded := []byte("a: 1\n")
	assert.Equal(t, string(encoded), string(preserveGitLabFormatting([]byte("a: [\n"), encoded)))
}

func TestDiffLineMatches_TooManyEdits(t *testing.T) {
	matches, ok := diffLineMatches([]string{"a", "", "b"}, []string{"c", "d"}, 2)
	assert.False(t, ok)
	assert.Nil(t, matches)
}

func TestDiffLineMatches(t *testing.T) {
	matches, ok := diffLineMatches(
		[]string{"a:", "  - x", "", "b: 1", "c: 2"},
		[]string{"a:", "    - x", "    - y", "b: 1", "d: 3"},
		100)
	require.True(t, ok)
	assert.Equal(t, [][2]int{{0, 0}, {1, 1}, {3, 3}}, matches)

	matches, ok = diffLineMatches(nil, []string{"a"}, 100)
	require.True(t, ok)
	assert.Empty(t, matches)
}

func TestRestoreBlankLines_GapPlacement(t *testing.T) {
	// A blank line between rewritten lines keeps its relative position;
	// one trailing its gap goes after every added line; a matched blank
	// line is emitted once.
	original := "a:\n  x: \"1\"\n\n  y: \"2\"\n\nb: |\n  p\n\n  q\nc: 3\n"
	encoded := "a:\n  x: '1'\n  y: '2'\n  z: '3'\nb: |\n  p\n\n  q\nc: 3\n"
	restored, ok := restoreBlankLines([]byte(original), []byte(encoded))
	require.True(t, ok)
	assert.Equal(t, "a:\n  x: '1'\n\n  y: '2'\n  z: '3'\n\nb: |\n  p\n\n  q\nc: 3\n", string(restored))

	restored, ok = restoreBlankLines(nil, []byte("a: 1\n"))
	require.True(t, ok)
	assert.Equal(t, "a: 1\n", string(restored))
}
