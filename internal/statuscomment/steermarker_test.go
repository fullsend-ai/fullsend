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

func TestLatestSteerMarker(t *testing.T) {
	comments := []tracker.Comment{
		{Author: "fullsend[bot]", Body: "<!-- fullsend:steer consumed=1 head=aaa -->"},
		{Author: "someuser", Body: "<!-- fullsend:steer consumed=999 head=bbb -->"},
		{Author: "fullsend[bot]", Body: "<!-- fullsend:steer consumed=2,3 head=ccc -->"},
		{Author: "fullsend[bot]", Body: "🤖 no marker here"},
	}

	got, ok := LatestSteerMarker(comments, "fullsend[bot]")
	require.True(t, ok)
	assert.Equal(t, []int64{2, 3}, got.ConsumedRunIDs, "the newest App-authored marker wins")
	assert.Equal(t, "ccc", got.HeadSHA)
}

func TestLatestSteerMarker_IgnoresForgedMarkers(t *testing.T) {
	comments := []tracker.Comment{
		{Author: "attacker", Body: "<!-- fullsend:steer consumed=1234 head=aaa -->"},
	}
	_, ok := LatestSteerMarker(comments, "fullsend[bot]")
	assert.False(t, ok, "a marker pasted by a non-App author must not be honoured")
}

func TestLatestSteerMarker_NoAuthor(t *testing.T) {
	comments := []tracker.Comment{{Author: "", Body: "<!-- fullsend:steer consumed=1 head=aaa -->"}}
	_, ok := LatestSteerMarker(comments, "")
	assert.False(t, ok)
}

func TestLatestSteerMarker_NoComments(t *testing.T) {
	_, ok := LatestSteerMarker(nil, "fullsend[bot]")
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
