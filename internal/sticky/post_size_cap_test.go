package sticky

import (
	"context"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// The trusted leading line carries the projection payload the next run's
// prior-review fetch reads back (it selects the newest marked comment and
// copies its body), so the create-path size cap must never cut it: Post
// refuses rather than creating a comment whose marker line is partial.

func TestPost_TrustedLeadingLineTooLargeForSizeCapIsRefused(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "bot"
	printer := ui.New(io.Discard)

	// A cap with room for the runner marker and the truncation note but
	// not the whole trusted line cuts inside that line: the marker stays
	// intact while the projection line is partial — the thread's defect
	// shape, so the refusal cannot be a marker-only guard.
	cfg := Config{
		Marker:             "<!-- fullsend:review-agent -->",
		MaxSize:            len("<!-- fullsend:review-agent -->\n"+trustedLine) + 20,
		TrustedLeadingLine: trustedLineRe,
	}
	body := trustedLine + "\n\n" + strings.Repeat("x", 200)

	_, err := Post(context.Background(), client, "o", "r", 1, body, cfg, printer)

	require.Error(t, err, "Post must refuse rather than post a partial trusted line")
	assert.Contains(t, err.Error(), "size cap")
	assert.Empty(t, client.IssueComments["o/r/1"], "no comment is created")
	assert.Empty(t, client.UpdatedComments, "no comment is edited in place")
}

func TestPost_TrustedLeadingLineWithinSizeCapPostsIntact(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "bot"
	printer := ui.New(io.Discard)

	// The cap leaves room for the runner marker and the trusted line
	// (plus the truncation note), so an oversized rest of the body is
	// truncated after them, not through them.
	cfg := Config{
		Marker:             "<!-- fullsend:review-agent -->",
		MaxSize:            len("<!-- fullsend:review-agent -->\n"+trustedLine) + 80,
		TrustedLeadingLine: trustedLineRe,
	}
	body := trustedLine + "\n\n" + strings.Repeat("x", 500)

	_, err := Post(context.Background(), client, "o", "r", 1, body, cfg, printer)
	require.NoError(t, err)

	comments := client.IssueComments["o/r/1"]
	require.Len(t, comments, 1, "the comment is created")
	got := comments[0].Body
	assert.True(t, strings.HasPrefix(got, cfg.Marker+"\n"+trustedLine),
		"the runner marker and the trusted leading line stay intact")
	assert.Contains(t, got, "*Comment truncated due to comment size limits.*",
		"the oversized rest is truncated after the trusted line")
	assert.LessOrEqual(t, len(got), cfg.MaxSize, "the posted body respects the cap")
}

// The review door's approve update keeps three trusted leading lines
// (diff hash, head, then the findings projection), so the guard must
// cover all of them: a cut that lands inside the third line leaves a
// partial projection even though the first line is intact — the exact
// shape the first-line-only guard missed.
func TestPost_TrustedLeadingLinesThirdLineCutBySizeCapIsRefused(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "bot"
	printer := ui.New(io.Discard)

	hashLine := "<!-- fullsend:review-diff-hash:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef -->"
	headLine := "<!-- fullsend:review-head:0123456789abcdef0123456789abcdef01234567 -->"
	trusted := regexp.MustCompile(`^(?:<!-- fullsend:review-findings-v2:[A-Za-z0-9+/]+={0,2} -->|<!-- fullsend:review-diff-hash:[0-9a-f]{64} -->|<!-- fullsend:review-head:[0-9a-fA-F]{40,64} -->)$`)
	cfg := Config{
		Marker:                 "<!-- fullsend:review-agent -->",
		TrustedLeadingLine:     trusted,
		MaxTrustedLeadingLines: 3,
		// Room for the runner marker, the truncation note and the first
		// two trusted lines plus most of the third: the cut lands inside
		// the projection line, past the first line the old guard checked.
		MaxSize: len("<!-- fullsend:review-agent -->\n"+hashLine+"\n"+headLine+"\n"+trustedLine) + 20,
	}
	body := hashLine + "\n" + headLine + "\n" + trustedLine + "\n\n" + strings.Repeat("x", 200)

	_, err := Post(context.Background(), client, "o", "r", 1, body, cfg, printer)

	require.Error(t, err, "Post must refuse rather than post a partial third trusted line")
	assert.Contains(t, err.Error(), "size cap")
	assert.Empty(t, client.IssueComments["o/r/1"], "no comment is created")
	assert.Empty(t, client.UpdatedComments, "no comment is edited in place")
}

// The same three-line shape with room for all of them posts them
// intact and truncates only the untrusted rest.
func TestPost_TrustedLeadingLinesWithinSizeCapPostsIntact(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "bot"
	printer := ui.New(io.Discard)

	hashLine := "<!-- fullsend:review-diff-hash:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef -->"
	headLine := "<!-- fullsend:review-head:0123456789abcdef0123456789abcdef01234567 -->"
	trusted := regexp.MustCompile(`^(?:<!-- fullsend:review-findings-v2:[A-Za-z0-9+/]+={0,2} -->|<!-- fullsend:review-diff-hash:[0-9a-f]{64} -->|<!-- fullsend:review-head:[0-9a-fA-F]{40,64} -->)$`)
	cfg := Config{
		Marker:                 "<!-- fullsend:review-agent -->",
		TrustedLeadingLine:     trusted,
		MaxTrustedLeadingLines: 3,
		MaxSize:                len("<!-- fullsend:review-agent -->\n"+hashLine+"\n"+headLine+"\n"+trustedLine) + 80,
	}
	body := hashLine + "\n" + headLine + "\n" + trustedLine + "\n\n" + strings.Repeat("x", 500)

	_, err := Post(context.Background(), client, "o", "r", 1, body, cfg, printer)
	require.NoError(t, err)

	comments := client.IssueComments["o/r/1"]
	require.Len(t, comments, 1, "the comment is created")
	got := comments[0].Body
	assert.True(t, strings.HasPrefix(got, cfg.Marker+"\n"+hashLine+"\n"+headLine+"\n"+trustedLine),
		"the runner marker and all three trusted leading lines stay intact")
	assert.Contains(t, got, "*Comment truncated due to comment size limits.*",
		"the oversized rest is truncated after the trusted lines")
	assert.LessOrEqual(t, len(got), cfg.MaxSize, "the posted body respects the cap")
}
