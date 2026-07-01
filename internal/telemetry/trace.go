package telemetry

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// TraceIDFromUUID converts a dashed UUID (e.g. the output of
// security.GenerateTraceID) into a 32-hex-character W3C trace-id by removing
// the dashes. This deliberately reuses the per-run security trace id so a
// single id correlates security findings, telemetry, and child processes.
//
// W3C forbids an all-zero trace-id; if the input is malformed or strips to all
// zeros, a fresh random trace-id is returned instead so callers never emit an
// invalid value.
func TraceIDFromUUID(uuid string) string {
	id := strings.ReplaceAll(uuid, "-", "")
	if len(id) != 32 || id == "00000000000000000000000000000000" {
		return randomHex(16)
	}
	return id
}

// NewSpanID returns a random 16-hex-character (8-byte) W3C span-id.
func NewSpanID() string {
	return randomHex(8)
}

// TraceParent returns a W3C traceparent header value of the form
// "00-<32-hex trace-id>-<16-hex span-id>-01" (version 00, sampled flag 01).
func TraceParent(traceID, spanID string) string {
	return "00-" + traceID + "-" + spanID + "-01"
}

// TraceParentWithFlags is like TraceParent but uses the given trace-flags
// (2-hex-character string) instead of the default "01". This preserves the
// upstream sampling decision when propagating an inbound traceparent.
func TraceParentWithFlags(traceID, spanID, flags string) string {
	return "00-" + traceID + "-" + spanID + "-" + flags
}

// ParseTraceParent parses a W3C traceparent header (version 00) and returns
// the trace-id, parent span-id, and trace-flags. ok is false if the header
// is malformed, uses an unsupported version, or contains invalid ids.
func ParseTraceParent(tp string) (traceID, spanID, flags string, ok bool) {
	parts := strings.Split(tp, "-")
	if len(parts) != 4 || parts[0] != "00" {
		return "", "", "", false
	}
	traceID, spanID, flags = parts[1], parts[2], parts[3]
	if len(traceID) != 32 || traceID == "00000000000000000000000000000000" {
		return "", "", "", false
	}
	if len(spanID) != 16 || spanID == "0000000000000000" {
		return "", "", "", false
	}
	if len(flags) != 2 {
		return "", "", "", false
	}
	// Validate all components are lowercase hex.
	for _, s := range []string{traceID, spanID, flags} {
		for _, c := range s {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return "", "", "", false
			}
		}
	}
	return traceID, spanID, flags, true
}

// UUIDFromTraceID converts a 32-hex-character W3C trace-id into dashed UUID
// format (8-4-4-4-12). Returns empty string if the input is not exactly 32
// lowercase hex characters.
func UUIDFromTraceID(traceID string) string {
	if len(traceID) != 32 {
		return ""
	}
	for _, c := range traceID {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return ""
		}
	}
	return traceID[0:8] + "-" + traceID[8:12] + "-" + traceID[12:16] + "-" + traceID[16:20] + "-" + traceID[20:32]
}

// randRead is a seam over crypto/rand.Read so the RNG-failure fallback in
// randomHex is testable.
var randRead = rand.Read

// randomHex returns 2n lowercase hex characters from crypto/rand. If the RNG
// fails or (astronomically rarely) yields all-zero bytes, it falls back to a
// fixed non-zero pattern so the result is never an all-zero id — honoring the
// W3C invariant that trace/span ids must be non-zero.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := randRead(b); err != nil || allZero(b) {
		for i := range b {
			b[i] = 0x11
		}
	}
	return hex.EncodeToString(b)
}

// allZero reports whether every byte is zero.
func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
