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
