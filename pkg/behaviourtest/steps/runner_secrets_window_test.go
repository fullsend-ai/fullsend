package steps

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHarnessLogPollWindowCoversLiveRunMeasurement locks AISDLC-654's
// ac_3: the time until a live run's logs were usable is measured and
// recorded, and harnessLogPollWindow covers that measurement with
// margin. The comment above the variable must state the measurement as
// `measured <duration>` (e.g. `measured 8s`) and say it comes from a
// live run of the runner-secret overlay guard scenarios; the window
// must be at least twice the measured time, so it covers the
// measurement with margin. Whether the recorded measurement matches the
// live run's logs is checked by a person on the PR; this test keeps the
// record and the margin from regressing.
func TestHarnessLogPollWindowCoversLiveRunMeasurement(t *testing.T) {
	src, err := os.ReadFile("runner_secrets.go")
	require.NoError(t, err)
	comment := docCommentAboveDecl(string(src), "var harnessLogPollWindow")
	require.NotEmpty(t, comment,
		"harnessLogPollWindow needs its doc comment, stating the measurement behind the window")
	assert.Contains(t, strings.ToLower(comment), "live",
		"the comment must say the measurement comes from a live run")
	m := regexp.MustCompile(`\bmeasured\s+(\d+(?:\.\d+)?(?:ms|s|m|h))\b`).FindStringSubmatch(comment)
	require.NotNil(t, m,
		"the comment must state the measured time until the run's logs were usable as `measured <duration>`")
	measured, parseErr := time.ParseDuration(m[1])
	require.NoError(t, parseErr)
	assert.Positive(t, measured, "a zero measurement records nothing")
	assert.GreaterOrEqual(t, harnessLogPollWindow, 2*measured,
		"harnessLogPollWindow must cover the measured time until the logs were usable with margin (at least twice the measurement)")
}

// docCommentAboveDecl returns the contiguous // comment block directly
// above the first line holding decl in src.
func docCommentAboveDecl(src, decl string) string {
	idx := strings.Index(src, decl)
	if idx < 0 {
		return ""
	}
	lines := strings.Split(src[:idx], "\n")
	var block []string
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" && len(block) == 0 {
			continue
		}
		if strings.HasPrefix(trimmed, "//") {
			block = append([]string{lines[i]}, block...)
			continue
		}
		break
	}
	return strings.Join(block, "\n")
}
