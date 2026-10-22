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

// liveMarkerRe matches a fullsend marker opener that would still parse as
// a marker (case-insensitive, any whitespace).
var liveMarkerRe = regexp.MustCompile(`(?i)<!--\s*fullsend\s*:`)

// TestPost_NeutralizesMarkersInBody_AcrossUpdates posts agent output that
// carries marker syntax, then updates the same comment repeatedly. The
// runner's own marker must be the only live marker after every update,
// with exactly one copy, and agent-written marker text must come out
// inert both in the current body and in history.
func TestPost_NeutralizesMarkersInBody_AcrossUpdates(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "bot"
	printer := ui.New(io.Discard)
	cfg := Config{Marker: "<!-- fullsend:review-agent -->", KeepHistory: true}

	agentBody := func(run string) string {
		return "Run " + run + " <!-- fullsend:agent-status:999 --> " +
			"<!--  FullSend :triage-agent --> already &lt;!-- fullsend:x -->"
	}

	for i, run := range []string{"1", "2", "3", "4"} {
		_, err := Post(context.Background(), client, "o", "r", 1, agentBody(run), cfg, printer)
		require.NoError(t, err)

		comments := client.IssueComments["o/r/1"]
		require.Len(t, comments, 1, "run %s must update the same comment", run)
		body := comments[0].Body

		assert.True(t, strings.HasPrefix(body, cfg.Marker+"\n"), "run %s: marker must stay at the start", run)
		assert.Equal(t, 1, strings.Count(body, cfg.Marker), "run %s: exactly one runner marker", run)
		assert.Equal(t, 1, strings.Count(body, "fullsend:review-agent"),
			"run %s: no escaped copy of the runner's marker lands in history", run)
		assert.Len(t, liveMarkerRe.FindAllString(body, -1), 1, "run %s: only the runner's marker is live", run)
		assert.Equal(t, i+1, strings.Count(body, "&lt;!-- fullsend:agent-status:999 -->"),
			"run %s: agent marker text is escaped once per run, not re-escaped", run)
		assert.NotContains(t, body, "&amp;lt;", "run %s: pre-escaped text must be unchanged", run)
		assert.NotContains(t, body, "&lt;&lt;", "run %s: escaping must not stack", run)
		assert.Contains(t, body, "Run "+run+" ")
	}

	// The comment cannot be confused with a sibling's sticky comment.
	comments := client.IssueComments["o/r/1"]
	assert.Nil(t, FindMarkedComment(comments, "<!-- fullsend:agent-status:999 -->", "bot"))
	assert.Nil(t, FindMarkedComment(comments, "<!-- fullsend:triage-agent -->", "bot"))
	assert.NotNil(t, FindMarkedComment(comments, cfg.Marker, "bot"))
}

// Marker text already present in an old body (for example one posted
// before escaping existed) is escaped as it moves into history, while the
// runner's own marker and footer are removed first and kept live.
func TestBuildUpdatedBody_NeutralizesMarkersInHistory(t *testing.T) {
	oldBody := "<!-- fullsend:test -->\nOld review <!-- fullsend:sibling --> text." +
		"\n\n<!-- fullsend:test-footer -->\n_footer_"
	newBody := "<!-- fullsend:test -->\nNew review."

	result := BuildUpdatedBody(oldBody, newBody, testCfg)

	assert.True(t, strings.HasPrefix(result, newBody))
	assert.Contains(t, result, "Old review &lt;!-- fullsend:sibling --> text.")
	assert.NotContains(t, result, "<!-- fullsend:sibling")
	assert.Equal(t, 1, strings.Count(result, "<!-- fullsend:test -->"))
	assert.Equal(t, 1, strings.Count(result, "<!-- fullsend:test-footer -->"))
	assert.True(t, strings.HasSuffix(result, "<!-- fullsend:test-footer -->\n_footer_"))
}

// Cutting a <details> block out of the old body joins the text on either
// side of it. A marker opener split around a block must still come out
// inert, in both the legacy and the sentinel history formats.
func TestBuildUpdatedBody_NeutralizesMarkerJoinedByHistoryRemoval(t *testing.T) {
	cfg := Config{Marker: "<!-- fullsend:review-agent -->", KeepHistory: true}
	newBody := cfg.Marker + "\nNew review."

	for name, block := range map[string]string{
		"legacy":   "<details><summary>Previous run</summary>x</details>",
		"sentinel": "<details>\n<summary>Previous run</summary>\n\n" + historyStart + "\nx\n" + historyEnd + "\n\n</details>",
	} {
		t.Run(name, func(t *testing.T) {
			oldBody := cfg.Marker + "\n<!" + block + "-- fullsend:triage-agent -->"

			result := BuildUpdatedBody(oldBody, newBody, cfg)

			assert.Len(t, liveMarkerRe.FindAllString(result, -1), 1, "only the runner's marker is live")
			assert.Contains(t, result, "&lt;!-- fullsend:triage-agent -->")
			assert.Nil(t, FindMarkedComment([]forge.IssueComment{{Author: "bot", Body: result}},
				"<!-- fullsend:triage-agent -->", "bot"))
		})
	}
}

// The footer is found by its last occurrence, so a copy of the footer
// marker inside the content does not cut the old body short.
func TestBuildUpdatedBody_FooterFoundByLastOccurrence(t *testing.T) {
	oldBody := "<!-- fullsend:test -->\nOld start <!-- fullsend:test-footer --> old end." +
		"\n\n<!-- fullsend:test-footer -->\n_footer_"
	newBody := "<!-- fullsend:test -->\nNew review."

	result := BuildUpdatedBody(oldBody, newBody, testCfg)

	assert.Contains(t, result, "Old start &lt;!-- fullsend:test-footer --> old end.")
	assert.Equal(t, 1, strings.Count(result, "<!-- fullsend:test-footer -->"))
	assert.True(t, strings.HasSuffix(result, "\n\n<!-- fullsend:test-footer -->\n_footer_"))
}

func TestBuildUpdatedBody_NoHistory_FooterFoundByLastOccurrence(t *testing.T) {
	cfg := testCfg
	cfg.KeepHistory = false
	oldBody := "<!-- fullsend:test -->\nOld <!-- fullsend:test-footer --> copy." +
		"\n\n<!-- fullsend:test-footer -->\n_footer_"
	newBody := "<!-- fullsend:test -->\nNew review."

	result := BuildUpdatedBody(oldBody, newBody, cfg)

	assert.Equal(t, newBody+"\n\n<!-- fullsend:test-footer -->\n_footer_", result)
}

// trustedLineRe stands in for a caller's anchored trusted marker pattern.
var trustedLineRe = regexp.MustCompile(`^<!-- fullsend:review-findings-v2:[A-Za-z0-9+/]+={0,2} -->$`)

const trustedLine = "<!-- fullsend:review-findings-v2:eyJhIjoxfQ== -->"

func TestNeutralizeBody_TrustedLeadingLine(t *testing.T) {
	cfg := Config{TrustedLeadingLine: trustedLineRe}
	escaped := "&lt;!-- fullsend:review-findings-v2:eyJhIjoxfQ== -->"

	tests := []struct {
		name string
		cfg  Config
		body string
		want string
	}{
		{
			name: "leading trusted line is kept and the rest escaped",
			cfg:  cfg,
			body: trustedLine + "\n\nText <!-- fullsend:agent-status:1 -->",
			want: trustedLine + "\n\nText &lt;!-- fullsend:agent-status:1 -->",
		},
		{
			name: "trusted line alone",
			cfg:  cfg,
			body: trustedLine,
			want: trustedLine,
		},
		{
			name: "duplicate after the leading line is escaped",
			cfg:  cfg,
			body: trustedLine + "\n" + trustedLine,
			want: trustedLine + "\n" + escaped,
		},
		{
			name: "trusted line mid-body is escaped",
			cfg:  cfg,
			body: "Text\n" + trustedLine,
			want: "Text\n" + escaped,
		},
		{
			name: "leading line with bad payload is escaped",
			cfg:  cfg,
			body: "<!-- fullsend:review-findings-v2:abc --> <!-- fullsend:x -->\nText",
			want: "&lt;!-- fullsend:review-findings-v2:abc --> &lt;!-- fullsend:x -->\nText",
		},
		{
			name: "leading line with trailing CR is escaped",
			cfg:  cfg,
			body: trustedLine + "\r\nText",
			want: escaped + "\r\nText",
		},
		{
			name: "no trusted pattern escapes everything",
			cfg:  Config{},
			body: trustedLine + "\nText",
			want: escaped + "\nText",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, neutralizeBody(tt.body, tt.cfg))
		})
	}
}

// A trusted leading line stays live in the current body across updates,
// while each earlier copy is escaped as it moves into history.
func TestPost_TrustedLeadingLine_AcrossUpdates(t *testing.T) {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "bot"
	printer := ui.New(io.Discard)
	cfg := Config{Marker: "<!-- fullsend:review-agent -->", KeepHistory: true, TrustedLeadingLine: trustedLineRe}
	body := trustedLine + "\n\nReview <!-- fullsend:triage-agent -->"

	for i := 1; i <= 3; i++ {
		_, err := Post(context.Background(), client, "o", "r", 1, body, cfg, printer)
		require.NoError(t, err)

		comments := client.IssueComments["o/r/1"]
		require.Len(t, comments, 1)
		got := comments[0].Body

		assert.True(t, strings.HasPrefix(got, cfg.Marker+"\n"+trustedLine+"\n\nReview &lt;!-- fullsend:triage-agent -->"),
			"run %d: trusted line follows the runner marker unescaped", i)
		assert.Len(t, liveMarkerRe.FindAllString(got, -1), 2, "run %d: only the runner marker and trusted line are live", i)
		assert.Equal(t, i-1, strings.Count(got, "&lt;!-- fullsend:review-findings-v2:"),
			"run %d: earlier trusted lines are escaped in history", i)
	}
}
