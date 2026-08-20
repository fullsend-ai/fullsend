package telemetry

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSpanLimits_ContentCaptureLiftsDefaultCap(t *testing.T) {
	cases := []struct {
		name      string
		gate      string
		limitEnv  string
		wantLimit int
	}{
		// Gate off: the #5944 default cap stands.
		{"gate off keeps default", "", "", MaxSpanAttrValueLen},
		// Gate on, no operator limit: unlimited — the SDK cap would cut the
		// content JSON mid-value, producing invalid JSON; the collector's
		// byte budget is the size bound instead.
		{"gate on lifts cap", "true", "", -1},
		// An operator's explicit limit always wins, gate or no gate.
		{"operator limit wins over gate", "true", "512", 512},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pinOTELEnv(t)
			t.Setenv(ContentCaptureEnvVar, tc.gate)
			t.Setenv("OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT", tc.limitEnv)
			assert.Equal(t, tc.wantLimit, spanLimits().AttributeValueLengthLimit)
		})
	}
}

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
