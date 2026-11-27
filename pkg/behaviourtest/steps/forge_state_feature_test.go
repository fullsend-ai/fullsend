package steps

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTriageReRunAppendFeature_StepsMatchPatterns checks that every
// comment assertion of the triage re-run scenario matches a pattern
// registerForgeStateSteps registers (the test compiles the same
// package-level consts the registration binds), so a typo in the
// feature or a reworded step text fails here rather than after a
// lease: the scenario's behaviour job skips on fork PRs, so its
// wiring is otherwise only checked live.
func TestTriageReRunAppendFeature_StepsMatchPatterns(t *testing.T) {
	t.Parallel()

	f, err := os.Open(filepath.Join("..", "..", "..", "e2e", "behaviour", "features", "triage", "triage-re-run-append.feature"))
	require.NoError(t, err)
	defer f.Close()

	var steps []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "Given ") || strings.HasPrefix(line, "When ") ||
			strings.HasPrefix(line, "Then ") || strings.HasPrefix(line, "And ") {
			steps = append(steps, line[strings.Index(line, " ")+1:])
		}
	}
	require.NoError(t, scanner.Err())

	patterns := []*regexp.Regexp{
		regexp.MustCompile(stepIssueCommentCountContains),
		regexp.MustCompile(stepLastIssueCommentContains),
		regexp.MustCompile(stepLastIssueCommentContainingContains),
		regexp.MustCompile(stepIssueCommentOtherThanLastContains),
		regexp.MustCompile(stepNoTargetCommentContains),
	}
	var commentAssertions []string
	for _, step := range steps {
		for _, p := range patterns {
			if p.MatchString(step) {
				commentAssertions = append(commentAssertions, step)
			}
		}
	}
	require.Len(t, commentAssertions, 5, "the scenario's five comment assertions, each matching exactly one pattern")

	// The marker-scoped assertion matches its own pattern, never the
	// any-kind last-comment one.
	markerScoped := 0
	for _, step := range steps {
		if !strings.HasPrefix(step, `the last comment on the issue containing "`) {
			continue
		}
		markerScoped++
		assert.True(t, patterns[2].MatchString(step), "step does not match the registered pattern: %s", step)
		assert.False(t, patterns[1].MatchString(step), "step also matches the any-kind pattern: %s", step)
	}
	assert.Equal(t, 1, markerScoped, "exactly one marker-scoped last-comment assertion")
}
