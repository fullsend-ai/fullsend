package telemetry

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestContentCaptureEnabled_ValueContract(t *testing.T) {
	cases := []struct {
		name  string
		value string
		set   bool
		want  bool
	}{
		{"unset", "", false, false},
		{"set but empty", "", true, false},
		{"true", "true", true, true},
		{"TRUE uppercase", "TRUE", true, true},
		{"padded true", "  true  ", true, true},
		{"span_only", "span_only", true, true},
		{"SPAN_ONLY uppercase", "SPAN_ONLY", true, true},
		{"span_and_event", "span_and_event", true, true},
		{"SPAN_AND_EVENT uppercase", "SPAN_AND_EVENT", true, true},
		// fullsend records content on span attributes only. event_only asks
		// for capture exclusively on events, which fullsend cannot honor —
		// recording on spans would contradict the operator's "only".
		{"event_only is off", "event_only", true, false},
		{"EVENT_ONLY uppercase is off", "EVENT_ONLY", true, false},
		{"NO_CONTENT is off", "NO_CONTENT", true, false},
		{"no_content lowercase is off", "no_content", true, false},
		{"false is off", "false", true, false},
		{"unrecognized is off", "yes-please", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(ContentCaptureEnvVar, tc.value)
			}
			assert.Equal(t, tc.want, ContentCaptureEnabled())
		})
	}
}
