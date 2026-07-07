package telemetry

import (
	"errors"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	reTraceID     = regexp.MustCompile(`^[0-9a-f]{32}$`)
	reSpanID      = regexp.MustCompile(`^[0-9a-f]{16}$`)
	reTraceparent = regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`)
)

const allZeroTraceID = "00000000000000000000000000000000"

func TestTraceIDFromUUID_StripsDashes(t *testing.T) {
	got := TraceIDFromUUID("4f3a9c1b-2d8e-4a7c-9f0b-1e2d3c4a5b6d")
	assert.Equal(t, "4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d", got)
	assert.Regexp(t, reTraceID, got)
}

func TestTraceIDFromUUID_GeneratorFormatIsValidW3C(t *testing.T) {
	// Mirrors the shape of security.GenerateTraceID(): lowercase dashed UUIDv4.
	got := TraceIDFromUUID("12ab34cd-56ef-4a7c-89b0-1e2d3c4a5b6d")
	assert.Regexp(t, reTraceID, got, "must be 32 lowercase hex chars")
	assert.NotEqual(t, allZeroTraceID, got)
}

func TestTraceIDFromUUID_AllZeroIsRegenerated(t *testing.T) {
	// W3C forbids an all-zero trace-id; the helper must not return one.
	got := TraceIDFromUUID("00000000-0000-0000-0000-000000000000")
	assert.Regexp(t, reTraceID, got)
	assert.NotEqual(t, allZeroTraceID, got)
}

func TestTraceIDFromUUID_MalformedIsReplaced(t *testing.T) {
	got := TraceIDFromUUID("not-a-uuid")
	assert.Regexp(t, reTraceID, got, "malformed input must still yield a valid 32-hex trace-id")
}

func TestNewSpanID(t *testing.T) {
	a := NewSpanID()
	b := NewSpanID()
	assert.Regexp(t, reSpanID, a)
	assert.Regexp(t, reSpanID, b)
	assert.NotEqual(t, "0000000000000000", a, "span-id must be non-zero")
	assert.NotEqual(t, a, b, "span-ids must be unique across calls")
}

func TestRandomHexFallsBackWhenRNGFails(t *testing.T) {
	orig := randRead
	randRead = func([]byte) (int, error) { return 0, errors.New("rng unavailable") }
	defer func() { randRead = orig }()

	// 8 bytes of the 0x11 fallback pattern -> 16 "1" hex chars, never all-zero.
	got := NewSpanID()
	assert.Equal(t, "1111111111111111", got)
	assert.Regexp(t, reSpanID, got)
	assert.NotEqual(t, "0000000000000000", got)
}

func TestRandomHexGuardsAllZeroFromRNG(t *testing.T) {
	orig := randRead
	// crypto/rand "succeeds" but yields all-zero bytes (astronomically rare):
	// the result must still never be an all-zero id.
	randRead = func(b []byte) (int, error) {
		for i := range b {
			b[i] = 0
		}
		return len(b), nil
	}
	defer func() { randRead = orig }()

	assert.NotEqual(t, "0000000000000000", NewSpanID(), "all-zero RNG output must be replaced")
	assert.Regexp(t, reSpanID, NewSpanID())
}

func TestTraceParent(t *testing.T) {
	got := TraceParent("4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d", "a1b2c3d4e5f60718")
	require.Equal(t, "00-4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d-a1b2c3d4e5f60718-01", got)
	assert.Regexp(t, reTraceparent, got)
}

func TestTraceParentWithFlags(t *testing.T) {
	got := TraceParentWithFlags("4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d", "a1b2c3d4e5f60718", "00")
	assert.Equal(t, "00-4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d-a1b2c3d4e5f60718-00", got, "unsampled flag preserved")

	got = TraceParentWithFlags("4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d", "a1b2c3d4e5f60718", "01")
	assert.Equal(t, TraceParent("4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d", "a1b2c3d4e5f60718"), got,
		"sampled flags must match the TraceParent default")

	got = TraceParentWithFlags("4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d", "a1b2c3d4e5f60718", "")
	assert.Equal(t, "00-4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d-a1b2c3d4e5f60718-01", got,
		"empty flags default to sampled")
}

func TestParseTraceParent(t *testing.T) {
	const (
		tid = "4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d"
		sid = "a1b2c3d4e5f60718"
	)
	tests := []struct {
		name    string
		input   string
		wantTID string
		wantSID string
		wantF   string
		wantOK  bool
	}{
		{name: "valid sampled", input: "00-" + tid + "-" + sid + "-01", wantTID: tid, wantSID: sid, wantF: "01", wantOK: true},
		{name: "valid unsampled", input: "00-" + tid + "-" + sid + "-00", wantTID: tid, wantSID: sid, wantF: "00", wantOK: true},
		// W3C forward compatibility: a higher version with the version-00
		// fields intact must parse (future versions may append fields).
		{name: "future version exact fields", input: "cc-" + tid + "-" + sid + "-01", wantTID: tid, wantSID: sid, wantF: "01", wantOK: true},
		{name: "future version extra field", input: "cc-" + tid + "-" + sid + "-01-extradata", wantTID: tid, wantSID: sid, wantF: "01", wantOK: true},
		// W3C: version 00 has exactly four fields; trailing data is invalid.
		{name: "version 00 with extra field", input: "00-" + tid + "-" + sid + "-01-extradata"},
		// W3C: version ff is forbidden.
		{name: "version ff", input: "ff-" + tid + "-" + sid + "-01"},
		{name: "non-hex version", input: "zz-" + tid + "-" + sid + "-01"},
		{name: "empty", input: ""},
		{name: "too few parts", input: "00-" + tid + "-" + sid},
		{name: "all-zero trace-id", input: "00-00000000000000000000000000000000-" + sid + "-01"},
		{name: "all-zero span-id", input: "00-" + tid + "-0000000000000000-01"},
		{name: "uppercase hex", input: "00-4F3A9C1B2D8E4A7C9F0B1E2D3C4A5B6D-" + sid + "-01"},
		{name: "short trace-id", input: "00-4f3a9c1b2d8e4a7c-" + sid + "-01"},
		{name: "short span-id", input: "00-" + tid + "-a1b2c3d4-01"},
		{name: "short flags", input: "00-" + tid + "-" + sid + "-1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotTID, gotSID, gotF, ok := ParseTraceParent(tc.input)
			assert.Equal(t, tc.wantOK, ok, "ok mismatch")
			if tc.wantOK {
				assert.Equal(t, tc.wantTID, gotTID)
				assert.Equal(t, tc.wantSID, gotSID)
				assert.Equal(t, tc.wantF, gotF)
			} else {
				assert.Empty(t, gotTID)
				assert.Empty(t, gotSID)
				assert.Empty(t, gotF)
			}
		})
	}
}

func TestUUIDFromTraceID(t *testing.T) {
	got := UUIDFromTraceID("4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d")
	assert.Equal(t, "4f3a9c1b-2d8e-4a7c-9f0b-1e2d3c4a5b6d", got)

	// Round-trip with TraceIDFromUUID must be lossless.
	assert.Equal(t, "4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d", TraceIDFromUUID(got))

	assert.Empty(t, UUIDFromTraceID("tooshort"))
	assert.Empty(t, UUIDFromTraceID("4F3A9C1B2D8E4A7C9F0B1E2D3C4A5B6D"), "uppercase is not valid W3C")
	assert.Empty(t, UUIDFromTraceID("4f3a9c1b2d8e4a7c9f0b1e2d3c4a5bzz"), "non-hex rejected")
}
