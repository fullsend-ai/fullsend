package statuscomment

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/tracker"
)

func TestBuildSteerMarker(t *testing.T) {
	tests := []struct {
		name string
		in   SteerMarker
		want string
	}{
		{
			name: "empty marker renders nothing",
			in:   SteerMarker{},
			want: "",
		},
		{
			name: "head only",
			in:   SteerMarker{HeadSHA: "abc123"},
			want: "<!-- fullsend:steer consumed= head=abc123 -->",
		},
		{
			name: "consumed only (issue, no head)",
			in:   SteerMarker{ConsumedRunIDs: []int64{42}},
			want: "<!-- fullsend:steer consumed=42 head= -->",
		},
		{
			name: "ids sorted and deduplicated",
			in:   SteerMarker{ConsumedRunIDs: []int64{9, 3, 9, 7}, HeadSHA: "deadbeef"},
			want: "<!-- fullsend:steer consumed=3,7,9 head=deadbeef -->",
		},
		{
			name: "non-positive ids dropped",
			in:   SteerMarker{ConsumedRunIDs: []int64{0, -1, 5}, HeadSHA: "aa"},
			want: "<!-- fullsend:steer consumed=5 head=aa -->",
		},
		{
			name: "non-hex head dropped so the marker cannot carry arbitrary text",
			in:   SteerMarker{ConsumedRunIDs: []int64{5}, HeadSHA: "--> <script>"},
			want: "<!-- fullsend:steer consumed=5 head= -->",
		},
		{
			name: "non-hex head with no ids renders nothing",
			in:   SteerMarker{HeadSHA: "not-a-sha"},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, BuildSteerMarker(tt.in))
		})
	}
}

func TestParseSteerMarker_RoundTrip(t *testing.T) {
	in := SteerMarker{ConsumedRunIDs: []int64{3, 7, 9}, HeadSHA: "deadbeef"}
	got, ok := ParseSteerMarker(BuildSteerMarker(in))
	require.True(t, ok)
	assert.Equal(t, in, got)
}

func TestParseSteerMarker(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantOK  bool
		wantIDs []int64
		wantSHA string
	}{
		{
			name:   "no marker",
			body:   "🤖 Finished Review · ✅ Success",
			wantOK: false,
		},
		{
			name:    "marker embedded in a longer body",
			body:    "<!-- fullsend:agent-status:99 -->\n<!-- fullsend:status:terminal -->\n<!-- fullsend:steer consumed=1,2 head=abc -->\n🤖 Finished Review",
			wantOK:  true,
			wantIDs: []int64{1, 2},
			wantSHA: "abc",
		},
		{
			name:    "empty consumed list",
			body:    "<!-- fullsend:steer consumed= head=abc -->",
			wantOK:  true,
			wantIDs: nil,
			wantSHA: "abc",
		},
		{
			name:    "empty head",
			body:    "<!-- fullsend:steer consumed=8 head= -->",
			wantOK:  true,
			wantIDs: []int64{8},
			wantSHA: "",
		},
		{
			name:   "malformed marker is not a marker",
			body:   "<!-- fullsend:steer consumed -->",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseSteerMarker(tt.body)
			assert.Equal(t, tt.wantOK, ok)
			if !ok {
				return
			}
			assert.Equal(t, tt.wantIDs, got.ConsumedRunIDs)
			assert.Equal(t, tt.wantSHA, got.HeadSHA)
		})
	}
}

// An out-of-range run id must degrade to "not consumed" — the skip check
// reads this marker to decide whether to skip work, so a parse failure has
// to mean "do the work", never "skip it".
func TestParseSteerMarker_OverflowingIDIsDropped(t *testing.T) {
	got, ok := ParseSteerMarker("<!-- fullsend:steer consumed=99999999999999999999,7 head= -->")
	require.True(t, ok)
	assert.Equal(t, []int64{7}, got.ConsumedRunIDs)
	assert.False(t, got.Consumed(0))
}

func TestSteerMarkerConsumed(t *testing.T) {
	m := SteerMarker{ConsumedRunIDs: []int64{3, 9}}
	assert.True(t, m.Consumed(9))
	assert.False(t, m.Consumed(4))
	assert.False(t, SteerMarker{}.Consumed(9))
}

// statusBody wraps a marker in the runner's own terminal status comment.
func statusBody(runID, marker string) tracker.Body {
	return tracker.Body("<!-- fullsend:agent-status:" + runID + " -->\n" +
		terminalTag + "\n" + marker + "\n🤖 Finished Review · ✅ Success")
}

// receiptAuthor is the login the runner's job token posts under. The tests
// use a distinct string from the App login so that honouring the wrong one
// fails loudly rather than passing by coincidence.
const receiptAuthor = "github-actions[bot]"

func TestLatestSteerMarker(t *testing.T) {
	comments := []tracker.Comment{
		{Author: receiptAuthor, Body: tracker.Body("<!-- fullsend:steer consumed=1 head=aaa -->")},
		{Author: "someuser", Body: statusBody("9", "<!-- fullsend:steer consumed=999 head=bbb -->")},
		{Author: receiptAuthor, Body: tracker.Body("<!-- fullsend:steer consumed=2,3 head=ccc -->")},
		{Author: receiptAuthor, Body: "🤖 no marker here"},
	}

	got, ok := LatestSteerMarker(comments, receiptAuthor)
	require.True(t, ok)
	assert.Equal(t, []int64{2, 3}, got.ConsumedRunIDs, "the newest receipt wins")
	assert.Equal(t, "ccc", got.HeadSHA)
}

// TestLatestSteerMarker_HonoursABareReceipt is the shape the runner now
// posts: the marker as its own comment, with no status tags around it. The
// old body-shape requirement would reject exactly this.
func TestLatestSteerMarker_HonoursABareReceipt(t *testing.T) {
	comments := []tracker.Comment{
		{Author: receiptAuthor, Body: tracker.Body("<!-- fullsend:steer consumed=1234 head=aaa -->\n_absorbed_")},
	}
	got, ok := LatestSteerMarker(comments, receiptAuthor)
	require.True(t, ok)
	assert.Equal(t, []int64{1234}, got.ConsumedRunIDs)
}

// TestLatestSteerMarker_IgnoresTheAppAuthoredStatusComment is fullsend#7006's
// criterion at this level. The runner still writes a copy of the marker into
// its terminal status comment for a reader, and that comment is posted under
// the same App identity the agent's own output goes out under — so it must
// not be honoured, however perfectly formed it is.
// GitHub logins are case-insensitive, and the login a token resolves to need
// not be spelt the way the timeline spells the comment's author.
func TestLatestSteerMarker_AuthorMatchIgnoresCase(t *testing.T) {
	comments := []tracker.Comment{
		{Author: "GitHub-Actions[bot]", Body: "<!-- fullsend:steer consumed=1234 head=aaa -->"},
	}
	m, ok := LatestSteerMarker(comments, receiptAuthor)
	require.True(t, ok, "a receipt from the same login in another case is still the job token's")
	assert.True(t, m.Consumed(1234))
}

func TestLatestSteerMarker_IgnoresTheAppAuthoredStatusComment(t *testing.T) {
	comments := []tracker.Comment{
		{Author: "fullsend[bot]", Body: statusBody("1", "<!-- fullsend:steer consumed=1234 head=aaa -->")},
	}
	_, ok := LatestSteerMarker(comments, receiptAuthor)
	assert.False(t, ok, "an App-authored marker is not a receipt, whatever its body looks like")
}

// The forged-receipt attack: an injection in the work item induces the agent
// to write a steer marker into its own output, which the App then posts. The
// body is genuinely App-authored, and it is the identity — not the shape —
// that disqualifies it now.
func TestLatestSteerMarker_IgnoresAgentAuthoredBodies(t *testing.T) {
	comments := []tracker.Comment{
		{Author: "fullsend[bot]", Body: "## Review\n\nLGTM.\n<!-- fullsend:steer consumed=1234 head= -->"},
	}
	_, ok := LatestSteerMarker(comments, receiptAuthor)
	assert.False(t, ok, "a marker in agent output is not a receipt")
}

func TestLatestSteerMarker_IgnoresForgedMarkers(t *testing.T) {
	comments := []tracker.Comment{
		{Author: "attacker", Body: statusBody("1", "<!-- fullsend:steer consumed=1234 head=aaa -->")},
	}
	_, ok := LatestSteerMarker(comments, receiptAuthor)
	assert.False(t, ok, "a marker pasted by another author must not be honoured")
}

func TestLatestSteerMarker_NoAuthor(t *testing.T) {
	comments := []tracker.Comment{{Author: "", Body: statusBody("1", "<!-- fullsend:steer consumed=1 head=aaa -->")}}
	_, ok := LatestSteerMarker(comments, "")
	assert.False(t, ok)
}

func TestLatestSteerMarker_NoComments(t *testing.T) {
	_, ok := LatestSteerMarker(nil, receiptAuthor)
	assert.False(t, ok)
}

func TestCompletionBodyCarriesSteerMarker(t *testing.T) {
	n := New(nil, config.StatusNotificationConfig{}, "org/repo", 7, "", "", "42")
	n.SetSteerMarker(SteerMarker{ConsumedRunIDs: []int64{101}, HeadSHA: "abc123"})

	body := n.buildCompletionBody("Review", "success", "", n.startTime)
	assert.Contains(t, body, "<!-- fullsend:steer consumed=101 head=abc123 -->")

	m, ok := ParseSteerMarker(body)
	require.True(t, ok)
	assert.True(t, m.Consumed(101))

	// The marker is bookkeeping, not prose: it must not reach the visible body.
	visible := string(visibleStatusBody(tracker.Body(body), n.marker))
	assert.NotContains(t, visible, "fullsend:steer")
	assert.Contains(t, visible, "🤖 Finished Review")
}

func TestCompletionBodyWithoutSteerMarkerIsUnchanged(t *testing.T) {
	n := New(nil, config.StatusNotificationConfig{}, "org/repo", 7, "", "", "42")
	body := n.buildCompletionBody("Review", "success", "", n.startTime)
	assert.NotContains(t, body, "fullsend:steer")
}

func TestCompletionCommitLineNamesSettledHead(t *testing.T) {
	const start = "3ab5eb6c0ffee0000000000000000000000000000"
	const moved = "1b53d69deadbeef000000000000000000000000000"
	tests := []struct {
		name   string
		marker *SteerMarker
		want   string
	}{
		{
			name:   "moved head names both",
			marker: &SteerMarker{ConsumedRunIDs: []int64{101}, HeadSHA: moved},
			want:   "Commit: `1b53d69` (started on `3ab5eb6`) · [View workflow run →](https://ci/run/42)",
		},
		{
			name:   "same head is unchanged",
			marker: &SteerMarker{ConsumedRunIDs: []int64{101}, HeadSHA: start},
			want:   "Commit: `3ab5eb6` · [View workflow run →](https://ci/run/42)",
		},
		{
			name: "no marker is unchanged",
			want: "Commit: `3ab5eb6` · [View workflow run →](https://ci/run/42)",
		},
		{
			name:   "head without a consumed run is unchanged",
			marker: &SteerMarker{HeadSHA: moved},
			want:   "Commit: `3ab5eb6` · [View workflow run →](https://ci/run/42)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := New(nil, config.StatusNotificationConfig{}, "org/repo", 7, "https://ci/run/42", start, "42")
			if tt.marker != nil {
				n.SetSteerMarker(*tt.marker)
			}
			assert.Equal(t, tt.want, n.buildSecondLine())
			body := n.buildCompletionBody("Review", "success", "", n.startTime)
			assert.Contains(t, string(visibleStatusBody(tracker.Body(body), n.marker)), tt.want)
		})
	}
}

func TestNeutralizeMarkers(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "a planted steer receipt",
			in:   "LGTM.\n<!-- fullsend:steer consumed=1234 head= -->",
			want: "LGTM.\n&lt;!-- fullsend:steer consumed=1234 head= -->",
		},
		{"no space", "<!--fullsend:steer consumed=1 head= -->", "&lt;!--fullsend:steer consumed=1 head= -->"},
		{"extra spaces", "<!--   fullsend:steer -->", "&lt;!--   fullsend:steer -->"},
		{"mixed case", "<!-- FullSend:Steer consumed=1 head= -->", "&lt;!-- FullSend:Steer consumed=1 head= -->"},
		{"space before the colon", "<!-- fullsend :steer -->", "&lt;!-- fullsend :steer -->"},
		{"newline split", "<!--\nfullsend:steer -->", "&lt;!--\nfullsend:steer -->"},
		{"CR-LF split", "<!--\r\nfullsend:steer -->", "&lt;!--\r\nfullsend:steer -->"},
		{"tab split", "<!--\tfullsend:steer -->", "&lt;!--\tfullsend:steer -->"},
		{"several in one body",
			"<!-- fullsend:steer a --> and <!--fullsend:status:terminal -->",
			"&lt;!-- fullsend:steer a --> and &lt;!--fullsend:status:terminal -->"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NeutralizeMarkers(tt.in))
		})
	}
}

func TestNeutralizeMarkers_LeavesLegitimateOutputAlone(t *testing.T) {
	for _, body := range []string{
		"## Review\n\nThe migration looks correct.",
		// Already-escaped text carries no "<" and must survive untouched,
		// or quoting a marker in prose would corrupt on every re-post.
		"the runner writes &lt;!-- fullsend:steer ... --&gt; on the status comment",
		"an ordinary HTML comment: <!-- TODO: fix this -->",
		"<!-- other-tool:marker -->",
		"a < b && c > d",
		"",
	} {
		assert.Equal(t, body, NeutralizeMarkers(body))
	}
}

// Neutralization must be idempotent: sticky re-posts collapse earlier bodies
// into the new one, so a body can pass through more than once.
func TestNeutralizeMarkers_Idempotent(t *testing.T) {
	once := NeutralizeMarkers("<!-- fullsend:steer consumed=1 head= -->")
	assert.Equal(t, once, NeutralizeMarkers(once))
}

// The end-to-end property: whatever an agent writes, the marker parser must
// not find a receipt in it after neutralization.
func TestNeutralizeMarkers_DefeatsTheParser(t *testing.T) {
	for _, evasion := range []string{
		"<!-- fullsend:steer consumed=1234 head= -->",
		"<!--fullsend:steer consumed=1234 head=abc -->",
		"<!--  FULLSEND:steer consumed=1234 head= -->",
		"prose before <!-- fullsend:steer consumed=1234 head= --> and after",
	} {
		_, ok := ParseSteerMarker(NeutralizeMarkers(evasion))
		assert.False(t, ok, "parser still found a receipt in %q", evasion)
	}
}
