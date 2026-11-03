// Package tracecontext implements the versioned immutable work-item
// identity and signed trace-context envelope contract from ADR 0154.
//
// The package is pure: it performs no network I/O, reads no configuration,
// and never grants dispatch permission. It must stay importable from
// pkg/behaviourtest, so it must not depend on internal/cli, internal/repos,
// internal/layers, or the nested mintcore module.
package tracecontext

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"go.opentelemetry.io/otel/trace"
)

// EntityKind distinguishes the entity kinds that can share a forge
// container. GitLab issue and merge-request database IDs come from
// independent spaces, so the kind is part of every identity.
type EntityKind byte

const (
	// KindWorkItem is an issue on GitHub, GitLab, or Jira.
	KindWorkItem EntityKind = 0x01
	// KindChangeProposal is a GitHub pull request or a GitLab merge
	// request. Jira has no change-proposal entity in this contract.
	KindChangeProposal EntityKind = 0x02
)

// Valid reports whether k is a defined entity kind.
func (k EntityKind) Valid() bool {
	return k == KindWorkItem || k == KindChangeProposal
}

// Identity is the immutable binding of a forge entity (ADR 0154, D01):
// installation, forge instance, container, entity kind, and entity
// database ID. Display names, URLs, GitHub numbers, GitLab IIDs, and Jira
// keys are not identity components; docs/reference/trace-context.md holds
// the per-forge field mapping.
type Identity struct {
	// Installation is the 128-bit installation identifier provisioned
	// with the signing key pair. It is authenticated in envelopes but
	// deliberately excluded from fallback derivation, per ADR 0154.
	Installation [16]byte
	// Instance is the normalized forge web base URL: lowercase
	// scheme://host with no path and no trailing slash. Use
	// NormalizeInstance to build it.
	Instance string
	// Container is the immutable repository or project database ID.
	Container int64
	// Kind is the entity kind within the container.
	Kind EntityKind
	// Entity is the immutable entity database ID.
	Entity int64
}

// Resolved reports whether the forge-side identity components are present
// and valid: a canonical instance, a container ID >= 1, a valid kind, and
// an entity ID >= 1. Installation is local configuration rather than event
// identity, so it is not part of resolution; admission checks it against
// the envelope separately. An unresolved identity must postpone dispatch
// (FR-002); it never derives a fallback.
func (id Identity) Resolved() bool {
	return validInstance(id.Instance) && id.Container >= 1 && id.Kind.Valid() && id.Entity >= 1
}

// Fallback is the deterministic entity-derived context: a stable TraceID
// and a synthetic anchor SpanID. By contract, the anchor exists only as a
// parent reference, is never exported as a span, and is never repointed.
type Fallback struct {
	TraceID      trace.TraceID
	AnchorSpanID trace.SpanID
}

// identityDomain separates fallback derivation from every other use of
// SHA-256 in this package and in the envelope format.
const identityDomain = "fullsend.tracecontext.identity.v1"

// DeriveFallback derives the v1 deterministic fallback context. The
// derivation covers the forge instance, container, kind, and entity —
// never the installation, per ADR 0154. It returns ok=false for an
// unresolved identity. The derivation is pure, so independent and
// concurrent first events converge on identical nonzero IDs.
func DeriveFallback(id Identity) (fb Fallback, ok bool) {
	if !id.Resolved() {
		return Fallback{}, false
	}
	h := sha256.New()
	writeField := func(b []byte) {
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(len(b)))
		h.Write(l[:])
		h.Write(b)
	}
	writeField([]byte(identityDomain))
	writeField([]byte(id.Instance))
	var i64 [8]byte
	binary.BigEndian.PutUint64(i64[:], uint64(id.Container))
	h.Write(i64[:])
	h.Write([]byte{byte(id.Kind)})
	binary.BigEndian.PutUint64(i64[:], uint64(id.Entity))
	h.Write(i64[:])

	sum := h.Sum(nil)
	copy(fb.TraceID[:], sum[0:16])
	copy(fb.AnchorSpanID[:], sum[16:24])
	return nonzeroFallback(fb), true
}

// All-zero digest slices use the documented v1 substitute so IDs stay valid.
func nonzeroFallback(fb Fallback) Fallback {
	if fb.TraceID == (trace.TraceID{}) {
		fb.TraceID[15] = 0x01
	}
	if fb.AnchorSpanID == (trace.SpanID{}) {
		fb.AnchorSpanID[7] = 0x01
	}
	return fb
}

// NormalizeInstance canonicalizes a forge web base URL to the D01 instance
// form: lowercase scheme://host with no path, query, fragment, or user
// info. Only http and https are accepted; a bare "/" path is tolerated and
// stripped. All identity and envelope construction must use the normalized
// form so independent launches converge.
func NormalizeInstance(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", errors.New("tracecontext: instance must be valid UTF-8")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("tracecontext: parse instance URL: %w", err)
	}
	if u.Scheme == "" || u.Hostname() == "" {
		return "", errors.New("tracecontext: instance URL needs a scheme and host")
	}
	if u.User != nil || strings.ContainsAny(raw, "?#") {
		return "", errors.New("tracecontext: instance URL must be a bare scheme://host")
	}
	if u.Path != "" && u.Path != "/" {
		return "", errors.New("tracecontext: instance URL must not carry a path")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("tracecontext: unsupported instance scheme %q", u.Scheme)
	}
	normalized := scheme + "://" + strings.ToLower(u.Host)
	if len(normalized) > 253 {
		return "", errors.New("tracecontext: instance must be at most 253 bytes")
	}
	return normalized, nil
}

func validInstance(instance string) bool {
	normalized, err := NormalizeInstance(instance)
	return err == nil && instance == normalized
}
