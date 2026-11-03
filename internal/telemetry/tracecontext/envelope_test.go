package tracecontext

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

// The tests in this file are V02/V04 for AISDLC-576: exact signed-byte
// fixtures, tamper-every-field, cross-binding replay, strict parser limits,
// duplicate/oversized/zero-ID rejection, retained keys, and pending markers
// granting nothing. Golden hex constants pin the frozen v1 wire format for
// every later PR.

// goldenKey is a deterministic Ed25519 key for reproducible fixtures.
func goldenKey() ed25519.PrivateKey {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	return ed25519.NewKeyFromSeed(seed)
}

func goldenKeys(t *testing.T) (ed25519.PrivateKey, map[[8]byte]ed25519.PublicKey) {
	t.Helper()
	key := goldenKey()
	pub := key.Public().(ed25519.PublicKey)
	return key, map[[8]byte]ed25519.PublicKey{KeyID(pub): pub}
}

func hexTrace(s string) trace.TraceID {
	tid, err := trace.TraceIDFromHex(s)
	if err != nil {
		panic(err)
	}
	return tid
}

func hexSpan(s string) trace.SpanID {
	sid, err := trace.SpanIDFromHex(s)
	if err != nil {
		panic(err)
	}
	return sid
}

// goldenIdentity matches the identity bound into every golden envelope.
func goldenIdentity() Identity {
	return Identity{
		Installation: [16]byte{0xa0, 0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7,
			0xa8, 0xa9, 0xaa, 0xab, 0xac, 0xad, 0xae, 0xaf},
		Instance:  "https://github.com",
		Container: 274390431,
		Kind:      KindWorkItem,
		Entity:    2468913570,
	}
}

// goldenEnvelope builds the fixture envelope for a domain and provenance
// kind. Scenario envelopes keep TraceID == ScenarioTraceID per the
// provenance consistency rule.
func goldenEnvelope(domain Domain, prov ProvenanceKind) *Envelope {
	id := goldenIdentity()
	env := &Envelope{
		Domain:       domain,
		Installation: id.Installation,
		Instance:     id.Instance,
		Container:    id.Container,
		Kind:         id.Kind,
		TraceID:      hexTrace("4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d"),
		ParentSpanID: hexSpan("a1b2c3d4e5f60718"),
		Nonce: [16]byte{0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
			0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f},
	}
	if domain == DomainBound {
		env.Entity = id.Entity
	}
	switch prov {
	case ProvenanceSourceLink:
		env.Provenance = Provenance{
			Kind:          ProvenanceSourceLink,
			SourceTraceID: hexTrace("5c4b3a29180f6e5d4c3b2a1908f7e6d5"),
			SourceSpanID:  hexSpan("0f1e2d3c4b5a6978"),
		}
	case ProvenanceScenario:
		env.Provenance = Provenance{
			Kind:            ProvenanceScenario,
			ScenarioTraceID: env.TraceID, // consistency: inherits scenario trace
			ScenarioInvocationID: [16]byte{0xf0, 0xf1, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7,
				0xf8, 0xf9, 0xfa, 0xfb, 0xfc, 0xfd, 0xfe, 0xff},
		}
	}
	return env
}

// signedGolden returns precomputed signed bytes so parser/admission tests
// do not depend on Sign or Marshal succeeding during fixture setup.
func signedGolden(t *testing.T, domain Domain, prov ProvenanceKind) ([]byte, map[[8]byte]ed25519.PublicKey) {
	t.Helper()
	_, keys := goldenKeys(t)
	vectors := map[Domain]map[ProvenanceKind]string{
		DomainBound:   {ProvenanceNone: goldenBoundNoneHex, ProvenanceSourceLink: goldenBoundSourceLinkHex, ProvenanceScenario: goldenBoundScenarioHex},
		DomainPending: {ProvenanceNone: goldenPendingNoneHex, ProvenanceSourceLink: goldenPendingSourceLinkHex, ProvenanceScenario: goldenPendingScenarioHex},
	}
	wire := vectors[domain][prov]
	require.NotEmpty(t, wire)
	return mustHex(t, wire), keys
}

// specLayout mirrors the frozen v1 wire layout from
// docs/reference/trace-context.md so tests locate fields independently of
// the implementation, pinning the format from the spec side.
type specLayout struct {
	instance, container, kind, entity int
	trace, span, prov, provFields     int
	nonce, sig, total                 int
}

func layout(domain Domain, prov ProvenanceKind, instanceLen int) specLayout {
	var o specLayout
	o.instance = 32
	o.container = o.instance + instanceLen
	o.kind = o.container + 8
	o.entity = o.kind + 1
	if domain == DomainBound {
		o.trace = o.entity + 8
	} else {
		o.trace = o.entity // pending markers omit the entity bytes
	}
	o.span = o.trace + 16
	o.prov = o.span + 8
	o.provFields = o.prov + 1
	fields := 0
	switch prov {
	case ProvenanceSourceLink:
		fields = 24
	case ProvenanceScenario:
		fields = 32
	}
	o.nonce = o.provFields + fields
	o.sig = o.nonce + 16
	o.total = o.sig + 64
	return o
}

// resign re-signs mutated wire bytes so field-validity failures are not
// masked by signature failures: the parser must reject the field, not the
// signature.
func resign(t *testing.T, key ed25519.PrivateKey, raw []byte) []byte {
	t.Helper()
	require.Greater(t, len(raw), 64)
	out := bytes.Clone(raw)
	sig := ed25519.Sign(key, out[:len(out)-64])
	copy(out[len(out)-64:], sig)
	return out
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	return b
}

// Golden hex constants: frozen from the first green run; any later change
// to the wire format is a compatibility break. Independently reconstructed
// with Python struct + cryptography Ed25519 during corrective review; the
// pending provenance vectors were first generated by that independent code.
var (
	goldenBoundNoneHex         = "46535443010165b60673d6ed884ba0a1a2a3a4a5a6a7a8a9aaabacadaeaf001268747470733a2f2f6769746875622e636f6d00000000105add9f01000000009328a1a24f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6da1b2c3d4e5f6071800101112131415161718191a1b1c1d1e1fe337c39001d56da6ba8862e01893f5d63d41fee3cab1d35f925aacd2e4a7165b30cf38403fcd2f809f50df67a62ebe705eb25a16e028df6345d8924b01bd2c0e"
	goldenBoundSourceLinkHex   = "46535443010165b60673d6ed884ba0a1a2a3a4a5a6a7a8a9aaabacadaeaf001268747470733a2f2f6769746875622e636f6d00000000105add9f01000000009328a1a24f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6da1b2c3d4e5f60718015c4b3a29180f6e5d4c3b2a1908f7e6d50f1e2d3c4b5a6978101112131415161718191a1b1c1d1e1f96451fbecf439bee72e636691b09f313df7b94bb017a63b7cdc0ea57775f3ca5a240c6f592f59bf33a25f463049e389345cf4d1354bd929198dc18803163a604"
	goldenBoundScenarioHex     = "46535443010165b60673d6ed884ba0a1a2a3a4a5a6a7a8a9aaabacadaeaf001268747470733a2f2f6769746875622e636f6d00000000105add9f01000000009328a1a24f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6da1b2c3d4e5f60718024f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6df0f1f2f3f4f5f6f7f8f9fafbfcfdfeff101112131415161718191a1b1c1d1e1fa59c45c20117895374e9ea5631083900ea081913ec21aa7d4604a23a75f2dd0f2ace2c01dda59c399f24bbdb409bc64081d898ba1ee73c292ca3d716e0860001"
	goldenPendingNoneHex       = "46535443010265b60673d6ed884ba0a1a2a3a4a5a6a7a8a9aaabacadaeaf001268747470733a2f2f6769746875622e636f6d00000000105add9f014f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6da1b2c3d4e5f6071800101112131415161718191a1b1c1d1e1f51fcd462f5bf6dd75e28f906fe0f4cf72d2c9fe9d3f66f95e1e650588442fdf2167890b17bc34775e27a74cc58ec22603df915fbe5396239d3f2b1ebf965c703"
	goldenPendingSourceLinkHex = "46535443010265b60673d6ed884ba0a1a2a3a4a5a6a7a8a9aaabacadaeaf001268747470733a2f2f6769746875622e636f6d00000000105add9f014f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6da1b2c3d4e5f60718015c4b3a29180f6e5d4c3b2a1908f7e6d50f1e2d3c4b5a6978101112131415161718191a1b1c1d1e1fa35099c98587f7dc2ba94734f2afe542cd1581bcd0dc9320e436de8ca57c0a3dcd61bcdfad9a933e3baec5a324659286eb164aa745c3f62079ea466513aa6703"
	goldenPendingScenarioHex   = "46535443010265b60673d6ed884ba0a1a2a3a4a5a6a7a8a9aaabacadaeaf001268747470733a2f2f6769746875622e636f6d00000000105add9f014f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6da1b2c3d4e5f60718024f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6df0f1f2f3f4f5f6f7f8f9fafbfcfdfeff101112131415161718191a1b1c1d1e1f0bb31c983e5206e3592f6e437117cbdc06a6991213ee65c57ff3cff4cdd35a26f571fa350d2046117c695892521bf393df228718b96cb5fdbb65c244b3de820c"
)

func TestGoldenVectors(t *testing.T) {
	cases := []struct {
		name string
		dom  Domain
		prov ProvenanceKind
		hex  *string
	}{
		{"bound none", DomainBound, ProvenanceNone, &goldenBoundNoneHex},
		{"bound source link", DomainBound, ProvenanceSourceLink, &goldenBoundSourceLinkHex},
		{"bound scenario", DomainBound, ProvenanceScenario, &goldenBoundScenarioHex},
		{"pending none", DomainPending, ProvenanceNone, &goldenPendingNoneHex},
		{"pending source link", DomainPending, ProvenanceSourceLink, &goldenPendingSourceLinkHex},
		{"pending scenario", DomainPending, ProvenanceScenario, &goldenPendingScenarioHex},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, keys := goldenKeys(t)
			env := goldenEnvelope(tc.dom, tc.prov)
			require.NoError(t, env.Sign(goldenKey()))
			raw := env.Marshal()
			want := mustHex(t, *tc.hex)
			if !bytes.Equal(raw, want) {
				t.Fatalf("golden wire bytes changed or not yet frozen:\n got %x\nwant %x", raw, want)
			}

			// Layout is exactly the frozen spec's size.
			assert.Equal(t, layout(tc.dom, tc.prov, len(goldenIdentity().Instance)).total, len(raw))

			// Parse round-trips byte-for-byte.
			parsed, err := Parse(want)
			require.NoError(t, err)
			assert.Equal(t, env, parsed)

			// Admission admits exactly the bound context.
			res := Admit(goldenIdentity(), keys, want)
			if tc.dom == DomainBound {
				assert.Equal(t, OutcomeAdmitted, res.Outcome, "reason: %s", res.Reason)
				assert.Equal(t, env.TraceID, res.TraceID)
				assert.Equal(t, env.ParentSpanID, res.ParentSpanID)
				assert.Equal(t, env.Provenance, res.Provenance)
			} else {
				assert.Equal(t, Result{Outcome: OutcomePending, Reason: ReasonPending}, res)
			}
		})
	}
}

// TestTamperEverySignedByte flips each byte of a maximal envelope — header,
// binding, context, provenance, nonce, and signature — and requires
// rejection every time.
func TestTamperEverySignedByte(t *testing.T) {
	raw, keys := signedGolden(t, DomainBound, ProvenanceScenario)
	for i := 0; i < len(raw); i++ {
		tampered := bytes.Clone(raw)
		tampered[i] ^= 0x01
		res := Admit(goldenIdentity(), keys, tampered)
		assert.NotEqual(t, OutcomeAdmitted, res.Outcome, "flipping byte %d must not admit", i)
	}
}

// TestCrossBindingReplay signs for one identity and admits against another:
// every binding component mismatch must reject a copied envelope.
func TestCrossBindingReplay(t *testing.T) {
	raw, keys := signedGolden(t, DomainBound, ProvenanceNone)

	mutations := map[string]func(*Identity){
		"installation": func(i *Identity) { i.Installation = [16]byte{0xbb} },
		"instance":     func(i *Identity) { i.Instance = "https://ghe.example.com" },
		"container":    func(i *Identity) { i.Container++ },
		"kind":         func(i *Identity) { i.Kind = KindChangeProposal },
		"entity":       func(i *Identity) { i.Entity++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			expected := goldenIdentity()
			mutate(&expected)
			res := Admit(expected, keys, raw)
			assert.Equal(t, OutcomeFallback, res.Outcome)
			assert.Equal(t, ReasonBindingMismatch, res.Reason)
		})
	}
}

func TestAdmit_Absent(t *testing.T) {
	_, keys := goldenKeys(t)
	res := Admit(goldenIdentity(), keys)
	assert.Equal(t, OutcomeFallback, res.Outcome)
	assert.Equal(t, ReasonAbsent, res.Reason)
}

func TestAdmit_UnknownKey(t *testing.T) {
	raw, _ := signedGolden(t, DomainBound, ProvenanceNone)

	// A genuinely different key: goldenKey is deterministic, so derive
	// from a distinct seed.
	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	otherPub := other.Public().(ed25519.PublicKey)
	res := Admit(goldenIdentity(), map[[8]byte]ed25519.PublicKey{KeyID(otherPub): otherPub}, raw)
	assert.Equal(t, OutcomeFallback, res.Outcome)
	assert.Equal(t, ReasonUnknownKey, res.Reason)
}

func TestAdmit_MalformedVerificationKeys(t *testing.T) {
	raw := mustHex(t, goldenBoundSourceLinkHex)
	selector := [8]byte{0x65, 0xb6, 0x06, 0x73, 0xd6, 0xed, 0x88, 0x4b}
	for _, key := range []ed25519.PublicKey{nil, {}, make([]byte, 31), make([]byte, 33)} {
		var result Result
		assert.NotPanics(t, func() {
			result = Admit(goldenIdentity(), map[[8]byte]ed25519.PublicKey{selector: key}, raw)
		}, "key length %d", len(key))
		assert.Equal(t, Result{Outcome: OutcomeFallback, Reason: ReasonUnknownKey}, result)
	}
}

func TestCanonicalInstanceAtEveryBoundary(t *testing.T) {
	for _, instance := range []string{
		"not-a-url", "https://GitHub.com", "HTTPS://github.com", "https://github.com/",
		"https://github.com/api", "https://github.com?", "https://github.com#",
		"https://user@github.com", "ftp://github.com", "https://:443",
		"https://" + strings.Repeat("a", 246), "https://\xff.example",
	} {
		t.Run(instance, func(t *testing.T) {
			id := goldenIdentity()
			id.Instance = instance
			assert.False(t, id.Resolved())
			fb, ok := DeriveFallback(id)
			assert.False(t, ok)
			assert.Equal(t, Fallback{}, fb)

			env := goldenEnvelope(DomainBound, ProvenanceNone)
			env.Instance = instance
			assert.Error(t, env.Sign(goldenKey()))

			// Replace only the instance in precomputed wire bytes, then sign
			// directly so Sign validation cannot mask Parse/Admit failures.
			raw := mustHex(t, goldenBoundNoneHex)
			mutated := append(bytes.Clone(raw[:32]), []byte(instance)...)
			mutated = append(mutated, raw[50:]...)
			binary.BigEndian.PutUint16(mutated[30:32], uint16(len(instance)))
			mutated = resign(t, goldenKey(), mutated)
			_, err := Parse(mutated)
			assert.Error(t, err)
			_, keys := goldenKeys(t)
			assert.Equal(t, Result{Outcome: OutcomeFallback, Reason: ReasonMalformed}, Admit(id, keys, mutated))
		})
	}
}

// TestAdmit_RetainedKey covers rotation: metadata signed by an older
// retained key stays verifiable while the active key signs new metadata.
func TestAdmit_RetainedKey(t *testing.T) {
	oldKey, _ := goldenKeys(t)
	env := goldenEnvelope(DomainBound, ProvenanceNone)
	require.NoError(t, env.Sign(oldKey))
	raw := env.Marshal()

	// Rotation: generate the new active key, keep the old public key.
	newSeed := make([]byte, ed25519.SeedSize)
	for i := range newSeed {
		newSeed[i] = byte(200 - i)
	}
	newKey := ed25519.NewKeyFromSeed(newSeed)
	newPub := newKey.Public().(ed25519.PublicKey)
	oldPub := oldKey.Public().(ed25519.PublicKey)
	keys := map[[8]byte]ed25519.PublicKey{KeyID(newPub): newPub, KeyID(oldPub): oldPub}

	res := Admit(goldenIdentity(), keys, raw)
	assert.Equal(t, OutcomeAdmitted, res.Outcome, "old metadata must verify with a retained key")
}

// TestAdmit_KeyIDDerivedFromPublicKey pins the selector rule: the key ID is
// the first 8 bytes of SHA-256(public key).
func TestAdmit_KeyIDDerivedFromPublicKey(t *testing.T) {
	key, _ := goldenKeys(t)
	pub := key.Public().(ed25519.PublicKey)

	env := goldenEnvelope(DomainBound, ProvenanceNone)
	require.NoError(t, env.Sign(key))
	raw := env.Marshal()

	// Same public key registered under a wrong key ID: the envelope's
	// selector must not resolve, proving lookup uses the derived ID.
	wrong := map[[8]byte]ed25519.PublicKey{[8]byte{0x99}: pub}
	res := Admit(goldenIdentity(), wrong, raw)
	assert.Equal(t, OutcomeFallback, res.Outcome)
	assert.Equal(t, ReasonUnknownKey, res.Reason)
}

func TestAdmit_DuplicateCarriers(t *testing.T) {
	raw, keys := signedGolden(t, DomainBound, ProvenanceNone)

	res := Admit(goldenIdentity(), keys, raw, bytes.Clone(raw))
	assert.Equal(t, OutcomeAdmitted, res.Outcome, "identical duplicates are idempotent publication")

	// A differing carrier (different nonce) must conflict, never
	// first-match.
	key, _ := goldenKeys(t)
	otherEnv := goldenEnvelope(DomainBound, ProvenanceNone)
	otherEnv.Nonce[0] ^= 0xff
	require.NoError(t, otherEnv.Sign(key))
	res = Admit(goldenIdentity(), keys, raw, otherEnv.Marshal())
	assert.Equal(t, OutcomeFallback, res.Outcome)
	assert.Equal(t, ReasonConflictingCarrier, res.Reason, "conflicting carriers must reject, never first-match")
}

func TestAdmit_Oversized(t *testing.T) {
	_, keys := goldenKeys(t)

	// Pad a valid envelope beyond the maximum legal size.
	padded := make([]byte, MaxEnvelopeLen+1)
	res := Admit(goldenIdentity(), keys, padded)
	assert.Equal(t, OutcomeFallback, res.Outcome)
	assert.Equal(t, ReasonOversized, res.Reason)

	big := make([]byte, 10*1024)
	res = Admit(goldenIdentity(), keys, big)
	assert.Equal(t, OutcomeFallback, res.Outcome)
	assert.Equal(t, ReasonOversized, res.Reason)
}

// TestParse_ZeroAndInvalidFields mutates individual fields in otherwise
// valid, correctly signed envelopes: the parser must reject the field
// itself, not merely the signature.
func TestParse_ZeroAndInvalidFields(t *testing.T) {
	key, keys := goldenKeys(t)
	inst := len(goldenIdentity().Instance)

	cases := []struct {
		name   string
		domain Domain
		prov   ProvenanceKind
		mutate func(raw []byte) []byte
	}{
		{"zero installation", DomainBound, ProvenanceNone, func(r []byte) []byte { zeroAt(r, 14, 16); return r }},
		{"zero container", DomainBound, ProvenanceNone, func(r []byte) []byte { zeroAt(r, layout(DomainBound, ProvenanceNone, inst).container, 8); return r }},
		{"zero entity", DomainBound, ProvenanceNone, func(r []byte) []byte { zeroAt(r, layout(DomainBound, ProvenanceNone, inst).entity, 8); return r }},
		{"zero trace", DomainBound, ProvenanceNone, func(r []byte) []byte { zeroAt(r, layout(DomainBound, ProvenanceNone, inst).trace, 16); return r }},
		{"zero span", DomainBound, ProvenanceNone, func(r []byte) []byte { zeroAt(r, layout(DomainBound, ProvenanceNone, inst).span, 8); return r }},
		{"zero nonce", DomainBound, ProvenanceNone, func(r []byte) []byte { zeroAt(r, layout(DomainBound, ProvenanceNone, inst).nonce, 16); return r }},
		{"invalid kind", DomainBound, ProvenanceNone, func(r []byte) []byte { r[layout(DomainBound, ProvenanceNone, inst).kind] = 0x7f; return r }},
		{"invalid provenance kind", DomainBound, ProvenanceNone, func(r []byte) []byte { r[layout(DomainBound, ProvenanceNone, inst).prov] = 0x03; return r }},
		{"zero scenario trace", DomainBound, ProvenanceScenario, func(r []byte) []byte {
			zeroAt(r, layout(DomainBound, ProvenanceScenario, inst).provFields, 16)
			return r
		}},
		{"zero invocation id", DomainBound, ProvenanceScenario, func(r []byte) []byte {
			zeroAt(r, layout(DomainBound, ProvenanceScenario, inst).provFields+16, 16)
			return r
		}},
		{"zero source trace", DomainBound, ProvenanceSourceLink, func(r []byte) []byte {
			zeroAt(r, layout(DomainBound, ProvenanceSourceLink, inst).provFields, 16)
			return r
		}},
		{"unknown version", DomainBound, ProvenanceNone, func(r []byte) []byte { r[4] = 0x02; return r }},
		{"unknown domain", DomainBound, ProvenanceNone, func(r []byte) []byte { r[5] = 0x03; return r }},
		{"bad magic", DomainBound, ProvenanceNone, func(r []byte) []byte { r[0] = 'X'; return r }},
		{"trailing byte", DomainBound, ProvenanceNone, func(r []byte) []byte { return append(r[:len(r):len(r)], 0) }},
		{"truncated", DomainBound, ProvenanceNone, func(r []byte) []byte { return r[:len(r)-1] }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := signedGolden(t, tc.domain, tc.prov)
			raw = tc.mutate(raw)
			raw = resign(t, key, raw)

			_, err := Parse(raw)
			require.Error(t, err, "parser must reject invalid field values")

			res := Admit(goldenIdentity(), keys, raw)
			assert.Equal(t, OutcomeFallback, res.Outcome)
			if tc.name == "unknown version" {
				assert.Equal(t, ReasonUnknownVersion, res.Reason)
			}
		})
	}
}

func zeroAt(b []byte, off, n int) {
	for i := off; i < off+n; i++ {
		b[i] = 0
	}
}

// TestAdmit_PlainTraceparentIsNotAnEnvelope is V04: a plain W3C
// traceparent never substitutes for a signed envelope.
func TestAdmit_PlainTraceparentIsNotAnEnvelope(t *testing.T) {
	_, keys := goldenKeys(t)
	res := Admit(goldenIdentity(), keys, []byte("00-4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d-a1b2c3d4e5f60718-01"))
	assert.Equal(t, OutcomeFallback, res.Outcome)
	assert.Equal(t, ReasonMalformed, res.Reason)
	assert.Equal(t, trace.TraceID{}, res.TraceID, "no unverified trace may be adopted")
	assert.Equal(t, Provenance{}, res.Provenance, "no unverified link may be adopted")
}

// TestAdmit_PendingGrantsNothing is V04: a verified pending marker matching
// the expected scope yields the pending outcome with no context and no
// links; a scope mismatch is a binding mismatch.
func TestAdmit_PendingGrantsNothing(t *testing.T) {
	raw, keys := signedGolden(t, DomainPending, ProvenanceScenario)

	res := Admit(goldenIdentity(), keys, raw)
	assert.Equal(t, OutcomePending, res.Outcome)
	assert.Equal(t, ReasonPending, res.Reason)
	assert.Equal(t, trace.TraceID{}, res.TraceID, "pending must not grant context")
	assert.Equal(t, trace.SpanID{}, res.ParentSpanID, "pending must not grant a parent")
	assert.Equal(t, Provenance{}, res.Provenance, "pending must not grant links or scenario membership")

	expected := goldenIdentity()
	expected.Container++
	res = Admit(expected, keys, raw)
	assert.Equal(t, OutcomeFallback, res.Outcome)
	assert.Equal(t, ReasonBindingMismatch, res.Reason)
}

// TestAdmit_ProvenanceConsistency enforces the D02 rules: an ordinary
// source link keeps the destination's own trace; scenario provenance
// inherits the scenario trace.
func TestAdmit_ProvenanceConsistency(t *testing.T) {
	key, keys := goldenKeys(t)

	// Source link whose destination trace equals the source trace: the
	// traces would merge, which ordinary provenance forbids.
	raw := mustHex(t, goldenBoundSourceLinkHex)
	lay := layout(DomainBound, ProvenanceSourceLink, len(goldenIdentity().Instance))
	copy(raw[lay.trace:lay.trace+16], raw[lay.provFields:lay.provFields+16])
	res := Admit(goldenIdentity(), keys, resign(t, key, raw))
	assert.Equal(t, OutcomeFallback, res.Outcome)
	assert.Equal(t, ReasonProvenance, res.Reason)

	// Scenario provenance whose trace differs from the scenario trace.
	raw = mustHex(t, goldenBoundScenarioHex)
	lay = layout(DomainBound, ProvenanceScenario, len(goldenIdentity().Instance))
	raw[lay.trace] ^= 0x01
	res = Admit(goldenIdentity(), keys, resign(t, key, raw))
	assert.Equal(t, OutcomeFallback, res.Outcome)
	assert.Equal(t, ReasonProvenance, res.Reason)
}

// TestAdmit_FallbackAdoptsNothing asserts every non-admitted result keeps
// zero context and provenance (FR-005).
func TestAdmit_FallbackAdoptsNothing(t *testing.T) {
	raw, keys := signedGolden(t, DomainBound, ProvenanceSourceLink)

	tampered := bytes.Clone(raw)
	tampered[len(tampered)-1] ^= 0x01 // break the signature only
	res := Admit(goldenIdentity(), keys, tampered)
	assert.Equal(t, OutcomeFallback, res.Outcome)
	assert.Equal(t, ReasonBadSignature, res.Reason)
	assert.Equal(t, trace.TraceID{}, res.TraceID)
	assert.Equal(t, trace.SpanID{}, res.ParentSpanID)
	assert.Equal(t, Provenance{}, res.Provenance)
}

// TestResultCarriesNoSamplingStateOrBaggage is FR-007/V02: the admission
// result structurally cannot carry sampling flags, tracestate, or baggage.
func TestResultCarriesNoSamplingStateOrBaggage(t *testing.T) {
	rt := reflect.TypeOf(Result{})
	var fields []string
	for i := 0; i < rt.NumField(); i++ {
		fields = append(fields, rt.Field(i).Name)
	}
	assert.ElementsMatch(t,
		[]string{"Outcome", "Reason", "TraceID", "ParentSpanID", "Provenance"},
		fields)
}

// TestSign_RejectsInvalidEnvelopes keeps malformed envelopes from ever
// being signed: signing validates exactly what the parser validates.
func TestSign_RejectsInvalidEnvelopes(t *testing.T) {
	key, _ := goldenKeys(t)
	inst := goldenIdentity().Instance

	cases := map[string]func(*Envelope){"zero installation": func(e *Envelope) { e.Installation = [16]byte{} },
		"empty instance":     func(e *Envelope) { e.Instance = "" },
		"nul in instance":    func(e *Envelope) { e.Instance = "https://git\x00hub.com" },
		"instance too long":  func(e *Envelope) { e.Instance = "https://" + strings.Repeat("a", 300) + ".com" },
		"zero container":     func(e *Envelope) { e.Container = 0 },
		"zero entity":        func(e *Envelope) { e.Entity = 0 },
		"zero trace":         func(e *Envelope) { e.TraceID = trace.TraceID{} },
		"zero span":          func(e *Envelope) { e.ParentSpanID = trace.SpanID{} },
		"zero nonce":         func(e *Envelope) { e.Nonce = [16]byte{} },
		"invalid kind":       func(e *Envelope) { e.Kind = EntityKind(0x7f) },
		"invalid domain":     func(e *Envelope) { e.Domain = Domain(0x03) },
		"invalid provenance": func(e *Envelope) { e.Provenance = Provenance{Kind: ProvenanceKind(0x03)} },
		"zero source span": func(e *Envelope) {
			e.Provenance = Provenance{Kind: ProvenanceSourceLink, SourceTraceID: hexTrace("4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d")}
		},
		"zero invocation id": func(e *Envelope) {
			e.Provenance = Provenance{Kind: ProvenanceScenario, ScenarioTraceID: hexTrace("4f3a9c1b2d8e4a7c9f0b1e2d3c4a5b6d")}
		},
		"pending with entity": func(e *Envelope) {
			e.Domain = DomainPending
			e.Entity = 1
		},
		"bound scenario trace mismatch": func(e *Envelope) {
			e.Provenance = Provenance{
				Kind:                 ProvenanceScenario,
				ScenarioTraceID:      hexTrace("11111111111111111111111111111111"),
				ScenarioInvocationID: [16]byte{1},
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			env := goldenEnvelope(DomainBound, ProvenanceNone)
			env.Instance = inst
			mutate(env)
			err := env.Sign(key)
			assert.Error(t, err, "Sign must refuse to sign an invalid envelope")
		})
	}
}

func TestDecodeCarrier(t *testing.T) {
	raw, _ := signedGolden(t, DomainBound, ProvenanceNone)
	carrier := base64.StdEncoding.EncodeToString(raw)

	got, err := DecodeCarrier(carrier)
	require.NoError(t, err)
	assert.Equal(t, raw, got)

	_, err = DecodeCarrier("ab\r\ncd") // line break inside
	assert.Error(t, err, "line breaks must be rejected")

	_, err = DecodeCarrier(carrier[:len(carrier)-2]) // broken padding
	assert.Error(t, err)

	_, err = DecodeCarrier(carrier + "!!!")
	assert.Error(t, err, "invalid alphabet must be rejected")

	_, err = DecodeCarrier(strings.Repeat("A", MaxCarrierLen+1))
	assert.Error(t, err, "carriers longer than the maximum must be rejected")
}

func TestKeyID_Derivation(t *testing.T) {
	key := goldenKey()
	pub := key.Public().(ed25519.PublicKey)
	id := KeyID(pub)
	assert.NotEqual(t, [8]byte{}, id, "key ID must be derived, not zero")

	other := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, 32))
	otherPub := other.Public().(ed25519.PublicKey)
	assert.NotEqual(t, id, KeyID(otherPub), "distinct keys must have distinct IDs")
}

// TestSign_RejectsWrongKeySize guards the trust boundary between caller
// and host-side signing: only a well-formed Ed25519 private key may sign.
func TestSign_RejectsWrongKeySize(t *testing.T) {
	env := goldenEnvelope(DomainBound, ProvenanceNone)
	err := env.Sign(ed25519.PrivateKey(bytes.Repeat([]byte{1}, 10)))
	assert.Error(t, err, "Sign must reject a mis-sized private key")
}

// TestParse_Truncations walks every truncation of a maximal envelope: no
// prefix may parse, and none may panic.
func TestParse_Truncations(t *testing.T) {
	raw, _ := signedGolden(t, DomainBound, ProvenanceScenario)
	for n := 0; n < len(raw); n++ {
		_, err := Parse(raw[:n])
		require.Error(t, err, "truncation to %d bytes must not parse", n)
	}
}

// TestParse_ShortTails exercises mid-structure bounds failures: buffers
// that pass the minimum-length and header checks but end inside a later
// fixed-size field. The instance length is chosen per buffer so each
// truncation point lands in a different grab.
func TestParse_ShortTails(t *testing.T) {
	build := func(instLen, total int) []byte {
		b := make([]byte, total)
		copy(b, magic[:])
		b[4] = versionV1
		b[5] = byte(DomainBound)
		binary.BigEndian.PutUint16(b[30:32], uint16(instLen))
		for i := 32; i < 32+instLen && i < total; i++ {
			b[i] = 'x'
		}
		return b
	}
	// Instance field overruns the buffer.
	_, err := Parse(build(253, 150))
	assert.Error(t, err)
	// Container field overruns.
	_, err = Parse(build(100, 135))
	assert.Error(t, err)
	// Entity field overruns.
	_, err = Parse(build(100, 143))
	assert.Error(t, err)
	// Trace field overruns.
	_, err = Parse(build(100, 151))
	assert.Error(t, err)
	// Span field overruns.
	_, err = Parse(build(100, 159))
	assert.Error(t, err)
	// Provenance kind byte overruns.
	_, err = Parse(build(100, 167))
	assert.Error(t, err)
	// Signature region overruns (nonce fits, 64-byte signature does not).
	_, err = Parse(build(100, 183))
	assert.Error(t, err)
}

func FuzzParse(f *testing.F) {
	// The frozen golden vectors, their truncations, and oversized inputs
	// seed the corpus; CI fuzzing grows it from there.
	for _, h := range []string{goldenBoundNoneHex, goldenBoundSourceLinkHex, goldenBoundScenarioHex, goldenPendingNoneHex, goldenPendingSourceLinkHex, goldenPendingScenarioHex} {
		b, err := hex.DecodeString(h)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
		f.Add(b[:len(b)-1])
		f.Add(b[:len(b)/2])
	}
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{0x41}, MaxEnvelopeLen))
	f.Add(bytes.Repeat([]byte{0x00}, MaxEnvelopeLen+1))
	f.Fuzz(func(t *testing.T, data []byte) {
		env, err := Parse(data)
		if err != nil {
			return
		}
		// Any parsed envelope must round-trip byte-for-byte: the wire
		// format has exactly one encoding.
		if got := env.Marshal(); !bytes.Equal(got, data) {
			t.Fatalf("round-trip mismatch:\n got %x\nwant %x", got, data)
		}
	})
}
