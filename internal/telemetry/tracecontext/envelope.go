package tracecontext

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

// Domain distinguishes the two v1 message domains. The domain byte is
// covered by the signature, so a pending marker cannot be replayed as a
// bound envelope.
type Domain byte

const (
	// DomainBound is a signed envelope bound to a destination entity.
	DomainBound Domain = 0x01
	// DomainPending is a signed creation-intent marker. It carries no
	// entity ID and grants neither context, parent, nor source link.
	DomainPending Domain = 0x02
)

// Valid reports whether d is a defined message domain.
func (d Domain) Valid() bool {
	return d == DomainBound || d == DomainPending
}

// ProvenanceKind selects the authenticated provenance section.
type ProvenanceKind byte

const (
	// ProvenanceNone means the envelope carries no creation provenance.
	ProvenanceNone ProvenanceKind = 0x00
	// ProvenanceSourceLink is an ordinary creation relationship: the
	// source operation's span context, recorded as a link only. The
	// destination keeps its own TraceID.
	ProvenanceSourceLink ProvenanceKind = 0x01
	// ProvenanceScenario is behaviour-test scenario ownership: the entity
	// inherits the scenario TraceID.
	ProvenanceScenario ProvenanceKind = 0x02
)

// Valid reports whether k is a defined provenance kind.
func (k ProvenanceKind) Valid() bool {
	return k == ProvenanceNone || k == ProvenanceSourceLink || k == ProvenanceScenario
}

// Provenance carries the authenticated creation provenance of an envelope.
// Only the fields selected by Kind are meaningful.
type Provenance struct {
	Kind ProvenanceKind
	// SourceTraceID and SourceSpanID identify the creating operation for
	// ProvenanceSourceLink. Valid when Kind == ProvenanceSourceLink.
	SourceTraceID trace.TraceID
	SourceSpanID  trace.SpanID
	// ScenarioTraceID and ScenarioInvocationID identify the scenario for
	// ProvenanceScenario. Valid when Kind == ProvenanceScenario.
	ScenarioTraceID      trace.TraceID
	ScenarioInvocationID [16]byte
}

// Envelope is a decoded v1 envelope or pending marker. The wire layout,
// exact sizes, and strict limits are frozen in
// docs/reference/trace-context.md; Marshal is its only legal encoder.
type Envelope struct {
	Domain       Domain
	KeyID        [8]byte
	Installation [16]byte
	Instance     string
	Container    int64
	Kind         EntityKind
	// Entity is the immutable destination database ID. It must be 0 in
	// pending markers; the destination does not exist yet.
	Entity       int64
	TraceID      trace.TraceID
	ParentSpanID trace.SpanID
	Provenance   Provenance
	Nonce        [16]byte
	Signature    [64]byte
}

// MaxEnvelopeLen is the maximum legal wire size in bytes: a bound envelope
// with scenario provenance and a 253-byte instance (439 bytes). Anything
// longer is definitionally invalid.
const MaxEnvelopeLen = 439

// MaxCarrierLen is the maximum legal base64 carrier length in characters:
// base64.StdEncoding of MaxEnvelopeLen bytes without line breaks.
const MaxCarrierLen = 588

// Wire constants of the frozen v1 format.
var magic = [4]byte{'F', 'S', 'T', 'C'}

const versionV1 = 0x01

// Parse error sentinels; Admit maps them to reason codes.
var (
	errOversized      = errors.New("tracecontext: envelope exceeds maximum length")
	errUnknownVersion = errors.New("tracecontext: unknown envelope version")
	errMalformed      = errors.New("tracecontext: malformed envelope")
)

// minEnvelopeLen is the structural lower bound: pending domain, no
// provenance, one instance byte. Canonical URL validation is stricter.
const minEnvelopeLen = 147

// KeyID returns the v1 key selector for a verification key: the first 8
// bytes of SHA-256(public key). Envelopes carry only this selector; keys
// are resolved exclusively against trusted configuration.
func KeyID(pub ed25519.PublicKey) [8]byte {
	sum := sha256.Sum256(pub)
	var id [8]byte
	copy(id[:], sum[:8])
	return id
}

// validate enforces the structural field constraints shared by Sign and
// Parse. Provenance cross-field consistency is separate (checkConsistency)
// because it is an admission-layer rule, not wire validity.
func (e *Envelope) validate() error {
	if !e.Domain.Valid() {
		return fmt.Errorf("%w: domain %#02x", errMalformed, byte(e.Domain))
	}
	if e.Installation == ([16]byte{}) {
		return fmt.Errorf("%w: zero installation", errMalformed)
	}
	if !validInstance(e.Instance) {
		return fmt.Errorf("%w: invalid instance", errMalformed)
	}
	if e.Container < 1 {
		return fmt.Errorf("%w: container %d", errMalformed, e.Container)
	}
	if !e.Kind.Valid() {
		return fmt.Errorf("%w: kind %#02x", errMalformed, byte(e.Kind))
	}
	if e.Domain == DomainBound {
		if e.Entity < 1 {
			return fmt.Errorf("%w: entity %d", errMalformed, e.Entity)
		}
	} else if e.Entity != 0 {
		return fmt.Errorf("%w: pending marker carries entity %d", errMalformed, e.Entity)
	}
	if !e.TraceID.IsValid() {
		return fmt.Errorf("%w: zero trace id", errMalformed)
	}
	if !e.ParentSpanID.IsValid() {
		return fmt.Errorf("%w: zero span id", errMalformed)
	}
	if !e.Provenance.Kind.Valid() {
		return fmt.Errorf("%w: provenance kind %#02x", errMalformed, byte(e.Provenance.Kind))
	}
	switch e.Provenance.Kind {
	case ProvenanceSourceLink:
		if !e.Provenance.SourceTraceID.IsValid() || !e.Provenance.SourceSpanID.IsValid() {
			return fmt.Errorf("%w: invalid source link", errMalformed)
		}
	case ProvenanceScenario:
		if !e.Provenance.ScenarioTraceID.IsValid() {
			return fmt.Errorf("%w: zero scenario trace id", errMalformed)
		}
		if e.Provenance.ScenarioInvocationID == ([16]byte{}) {
			return fmt.Errorf("%w: zero scenario invocation id", errMalformed)
		}
	}
	if e.Nonce == ([16]byte{}) {
		return fmt.Errorf("%w: zero nonce", errMalformed)
	}
	return nil
}

// checkConsistency enforces the D02 provenance consistency rules: an
// ordinary source link keeps separate traces; scenario provenance inherits
// the scenario trace.
func (e *Envelope) checkConsistency() error {
	switch e.Provenance.Kind {
	case ProvenanceSourceLink:
		if e.TraceID == e.Provenance.SourceTraceID {
			return errors.New("tracecontext: source-link provenance must not merge the source and destination traces")
		}
	case ProvenanceScenario:
		if e.TraceID != e.Provenance.ScenarioTraceID {
			return errors.New("tracecontext: scenario provenance must inherit the scenario trace")
		}
	}
	return nil
}

// payload returns the signed prefix of the wire bytes: everything except
// the 64-byte signature. The magic, version, and domain bytes are included,
// so signatures are domain-separated and cannot be replayed across
// message domains.
func (e *Envelope) payload() []byte {
	buf := make([]byte, 0, MaxEnvelopeLen)
	buf = append(buf, magic[:]...)
	buf = append(buf, versionV1, byte(e.Domain))
	buf = append(buf, e.KeyID[:]...)
	buf = append(buf, e.Installation[:]...)
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(e.Instance)))
	buf = append(buf, l[:]...)
	buf = append(buf, e.Instance...)
	var i64 [8]byte
	binary.BigEndian.PutUint64(i64[:], uint64(e.Container))
	buf = append(buf, i64[:]...)
	buf = append(buf, byte(e.Kind))
	if e.Domain == DomainBound {
		binary.BigEndian.PutUint64(i64[:], uint64(e.Entity))
		buf = append(buf, i64[:]...)
	}
	buf = append(buf, e.TraceID[:]...)
	buf = append(buf, e.ParentSpanID[:]...)
	buf = append(buf, byte(e.Provenance.Kind))
	switch e.Provenance.Kind {
	case ProvenanceSourceLink:
		buf = append(buf, e.Provenance.SourceTraceID[:]...)
		buf = append(buf, e.Provenance.SourceSpanID[:]...)
	case ProvenanceScenario:
		buf = append(buf, e.Provenance.ScenarioTraceID[:]...)
		buf = append(buf, e.Provenance.ScenarioInvocationID[:]...)
	}
	buf = append(buf, e.Nonce[:]...)
	return buf
}

// Sign validates the envelope, derives KeyID from the signing key, and
// signs the canonical bytes in place. Signing is a trusted host-side
// operation; keys never enter sandboxes or argv.
func (e *Envelope) Sign(key ed25519.PrivateKey) error {
	if len(key) != ed25519.PrivateKeySize {
		return errors.New("tracecontext: invalid signing key size")
	}
	if err := e.validate(); err != nil {
		return err
	}
	if err := e.checkConsistency(); err != nil {
		return err
	}
	pub := key.Public().(ed25519.PublicKey)
	e.KeyID = KeyID(pub)
	copy(e.Signature[:], ed25519.Sign(key, e.payload()))
	return nil
}

// Marshal returns the canonical wire bytes including the signature. Sign
// must have completed; marshalling an unsigned envelope yields bytes no
// verifier accepts.
func (e *Envelope) Marshal() []byte {
	return append(e.payload(), e.Signature[:]...)
}

// Parse strictly decodes and validates wire bytes. It accepts only the
// exact structural length for the parsed domain and provenance kind;
// duplicate and unknown fields are structurally impossible in the fixed
// layout. Provenance cross-field consistency is checked at admission, not
// here.
func Parse(b []byte) (*Envelope, error) {
	if len(b) > MaxEnvelopeLen {
		return nil, errOversized
	}
	if len(b) < minEnvelopeLen {
		return nil, fmt.Errorf("%w: %d bytes", errMalformed, len(b))
	}
	if !bytes.Equal(b[0:4], magic[:]) {
		return nil, fmt.Errorf("%w: bad magic", errMalformed)
	}
	if b[4] != versionV1 {
		return nil, fmt.Errorf("%w: %#02x", errUnknownVersion, b[4])
	}
	domain := Domain(b[5])
	if !domain.Valid() {
		return nil, fmt.Errorf("%w: domain %#02x", errMalformed, b[5])
	}

	off := 32
	grab := func(n int) ([]byte, error) {
		if off+n > len(b) {
			return nil, fmt.Errorf("%w: truncated at %d", errMalformed, off)
		}
		p := b[off : off+n]
		off += n
		return p, nil
	}

	e := &Envelope{Domain: domain}
	// The minimum-length check covers the fixed 32-byte header.
	copy(e.KeyID[:], b[6:14])
	copy(e.Installation[:], b[14:30])
	instLen := int(binary.BigEndian.Uint16(b[30:32]))
	if instLen < 1 || instLen > 253 {
		return nil, fmt.Errorf("%w: instance length %d", errMalformed, instLen)
	}
	p, err := grab(instLen)
	if err != nil {
		return nil, err
	}
	e.Instance = string(p)
	p, err = grab(8)
	if err != nil {
		return nil, err
	}
	e.Container = int64(binary.BigEndian.Uint64(p))
	p, err = grab(1)
	if err != nil {
		return nil, err
	}
	e.Kind = EntityKind(p[0])
	if domain == DomainBound {
		p, err = grab(8)
		if err != nil {
			return nil, err
		}
		e.Entity = int64(binary.BigEndian.Uint64(p))
	}
	p, err = grab(16)
	if err != nil {
		return nil, err
	}
	copy(e.TraceID[:], p)
	p, err = grab(8)
	if err != nil {
		return nil, err
	}
	copy(e.ParentSpanID[:], p)
	p, err = grab(1)
	if err != nil {
		return nil, err
	}
	e.Provenance.Kind = ProvenanceKind(p[0])
	switch e.Provenance.Kind {
	case ProvenanceSourceLink:
		p, err = grab(16)
		if err != nil {
			return nil, err
		}
		copy(e.Provenance.SourceTraceID[:], p)
		p, err = grab(8)
		if err != nil {
			return nil, err
		}
		copy(e.Provenance.SourceSpanID[:], p)
	case ProvenanceScenario:
		p, err = grab(16)
		if err != nil {
			return nil, err
		}
		copy(e.Provenance.ScenarioTraceID[:], p)
		p, err = grab(16)
		if err != nil {
			return nil, err
		}
		copy(e.Provenance.ScenarioInvocationID[:], p)
	case ProvenanceNone:
	default:
		return nil, fmt.Errorf("%w: provenance kind %#02x", errMalformed, p[0])
	}
	p, err = grab(16)
	if err != nil {
		return nil, err
	}
	copy(e.Nonce[:], p)
	if off != len(b)-64 {
		return nil, fmt.Errorf("%w: %d trailing bytes", errMalformed, len(b)-64-off)
	}
	copy(e.Signature[:], b[off:])

	if err := e.validate(); err != nil {
		return nil, err
	}
	return e, nil
}

// DecodeCarrier strictly decodes a base64 carrier string to raw envelope
// bytes. It rejects line breaks, non-canonical encodings, and carriers
// longer than MaxCarrierLen.
func DecodeCarrier(s string) ([]byte, error) {
	if strings.ContainsAny(s, "\r\n") {
		return nil, errors.New("tracecontext: carrier contains line breaks")
	}
	if len(s) > MaxCarrierLen {
		return nil, errors.New("tracecontext: carrier exceeds maximum length")
	}
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("tracecontext: decode carrier: %w", err)
	}
	return b, nil
}

// Outcome is the admission outcome. It never encodes dispatch permission.
type Outcome int

const (
	// OutcomeAdmitted means a verified bound envelope matched the
	// expected binding; the admitted IDs are available.
	OutcomeAdmitted Outcome = iota
	// OutcomePending means a verified pending marker matched the expected
	// scope. It grants neither context nor links; bounded rereads are
	// caller policy.
	OutcomePending
	// OutcomeFallback means no carrier was admissible; the caller uses
	// the deterministic entity-derived fallback.
	OutcomeFallback
)

// Reason codes for non-admitted outcomes. They are redacted diagnostics:
// no key material, no raw bytes.
const (
	ReasonAbsent             = "absent"
	ReasonMalformed          = "malformed"
	ReasonOversized          = "oversized"
	ReasonUnknownVersion     = "unknown-version"
	ReasonUnknownKey         = "unknown-key"
	ReasonBadSignature       = "bad-signature"
	ReasonBindingMismatch    = "binding-mismatch"
	ReasonConflictingCarrier = "conflicting-carrier"
	ReasonProvenance         = "provenance"
	ReasonPending            = "pending"
)

// Result is the admission outcome. It carries admitted IDs and
// authenticated provenance only — never sampling flags, tracestate,
// baggage, or dispatch permission (FR-007, FR-008).
type Result struct {
	Outcome Outcome
	// Reason is a redacted reason code when the outcome is not admitted.
	Reason string
	// TraceID and ParentSpanID are the admitted context, valid when
	// Outcome == OutcomeAdmitted.
	TraceID      trace.TraceID
	ParentSpanID trace.SpanID
	// Provenance is the authenticated provenance, valid when
	// Outcome == OutcomeAdmitted.
	Provenance Provenance
}

func fallback(reason string) Result {
	return Result{Outcome: OutcomeFallback, Reason: reason}
}

func reasonForParse(err error) string {
	switch {
	case errors.Is(err, errOversized):
		return ReasonOversized
	case errors.Is(err, errUnknownVersion):
		return ReasonUnknownVersion
	default:
		return ReasonMalformed
	}
}

// Admit parses, verifies, and binds candidate carrier wire bytes against
// the expected identity and the configured eligible verification keys
// (active plus retained). Identical duplicate candidates are acceptable;
// any differing candidates reject. Keys are resolved only from the
// supplied trusted set. Admission is independent of dispatch
// authorization: this function never returns permission.
func Admit(expected Identity, keys map[[8]byte]ed25519.PublicKey, candidates ...[]byte) Result {
	if len(candidates) == 0 {
		return fallback(ReasonAbsent)
	}
	parsed := make([]*Envelope, 0, len(candidates))
	for _, c := range candidates {
		env, err := Parse(c)
		if err != nil {
			return fallback(reasonForParse(err))
		}
		parsed = append(parsed, env)
	}
	for i := 1; i < len(parsed); i++ {
		if !bytes.Equal(candidates[i], candidates[0]) {
			return fallback(ReasonConflictingCarrier)
		}
	}

	env := parsed[0]
	pub, ok := keys[env.KeyID]
	if !ok || len(pub) != ed25519.PublicKeySize {
		return fallback(ReasonUnknownKey)
	}
	payload := candidates[0][:len(candidates[0])-64]
	if !ed25519.Verify(pub, payload, env.Signature[:]) {
		return fallback(ReasonBadSignature)
	}
	if err := env.checkConsistency(); err != nil {
		return fallback(ReasonProvenance)
	}
	if env.Installation != expected.Installation ||
		env.Instance != expected.Instance ||
		env.Container != expected.Container ||
		env.Kind != expected.Kind {
		return fallback(ReasonBindingMismatch)
	}
	if env.Domain == DomainBound {
		if env.Entity != expected.Entity {
			return fallback(ReasonBindingMismatch)
		}
		return Result{
			Outcome:      OutcomeAdmitted,
			TraceID:      env.TraceID,
			ParentSpanID: env.ParentSpanID,
			Provenance:   env.Provenance,
		}
	}
	return Result{Outcome: OutcomePending, Reason: ReasonPending}
}
