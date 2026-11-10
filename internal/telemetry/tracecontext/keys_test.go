package tracecontext

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"testing/cryptotest"
	"testing/iotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests in this file cover install-time key provisioning (ADR 0154):
// installation IDs, verification keys, key IDs, the redacting SigningKey
// and the admission key set. A rejected input returns its sentinel with the
// sentinel's fixed text, which never quotes the input.

// RFC 8032 section 7.1, test 1: a seed and its public key, an oracle that
// does not depend on this package.
const (
	rfc8032SeedHex   = "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	rfc8032PublicHex = "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a"
)

// seqBytes returns n bytes counting up from start: deterministic entropy.
func seqBytes(start byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = start + byte(i)
	}
	return b
}

func pubFromSeed(b byte) ed25519.PublicKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{b}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
}

// requireRejected asserts that err is the sentinel with its fixed text and
// never contains the rejected input.
func requireRejected(t *testing.T, err, sentinel error, input string) {
	t.Helper()
	require.ErrorIs(t, err, sentinel)
	assert.Equal(t, sentinel.Error(), err.Error(), "the error text is fixed")
	for _, s := range []string{input, strings.TrimSpace(input)} {
		if s != "" {
			assert.NotContains(t, err.Error(), s, "the error must not contain the input")
		}
	}
}

// nonCanonical sets the unused low bits of the last base64 character: a
// lenient decoder accepts the result, a strict one rejects it.
func nonCanonical(t *testing.T, s string) string {
	t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	data := strings.TrimRight(s, "=")
	last := strings.IndexByte(alphabet, data[len(data)-1])
	out := data[:len(data)-1] + string(alphabet[last|0x01]) + s[len(data):]
	_, lenient := base64.StdEncoding.DecodeString(out)
	require.NoError(t, lenient, "fixture: lenient decoding accepts it")
	_, strict := base64.StdEncoding.Strict().DecodeString(out)
	require.Error(t, strict, "fixture: strict decoding rejects it")
	return out
}

func TestParseInstallationID(t *testing.T) {
	t.Parallel()
	const valid = "5c0f1e2d3c4b5a69788796a5b4c3d2e1"

	t.Run("accepts 32 lowercase hex characters", func(t *testing.T) {
		for _, in := range []string{valid, "00000000000000000000000000000001", "a0a1a2a3a4a5a6a7a8a9aaabacadaeaf"} {
			got, err := ParseInstallationID(in)
			require.NoError(t, err, in)
			assert.Equal(t, in, hex.EncodeToString(got[:]))
		}
	})

	t.Run("format round-trips", func(t *testing.T) {
		for _, id := range [][16]byte{goldenIdentity().Installation, v01Identity().Installation, {15: 0x01}} {
			s := FormatInstallationID(id)
			assert.Equal(t, hex.EncodeToString(id[:]), s, "32 lowercase hex characters")
			back, err := ParseInstallationID(s)
			require.NoError(t, err)
			assert.Equal(t, id, back)
		}
	})

	for _, tc := range []struct{ name, in string }{
		{"uppercase", strings.ToUpper(valid)},
		{"one uppercase letter", valid[:30] + "E1"},
		{"31 characters", valid[:31]},
		{"33 characters", valid + "0"},
		{"64 characters", valid + valid},
		{"non-hex character", valid[:31] + "g"},
		{"all zero", strings.Repeat("0", 32)},
		{"inner space", valid[:16] + " " + valid[17:]},
		{"trailing newline", valid + "\n"},
		{"0x prefix", "0x" + valid},
		{"UUID form", "5c0f1e2d-3c4b-5a69-7887-96a5b4c3d2e1"},
		{"empty", ""},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			got, err := ParseInstallationID(tc.in)
			requireRejected(t, err, ErrInvalidInstallationID, tc.in)
			assert.Equal(t, [16]byte{}, got)
		})
	}
}

func TestNewInstallationID(t *testing.T) {
	t.Parallel()
	t.Run("reads 16 bytes from the reader", func(t *testing.T) {
		src := seqBytes(0x01, 32)
		r := bytes.NewReader(src)
		first, err := NewInstallationID(r)
		require.NoError(t, err)
		second, err := NewInstallationID(r)
		require.NoError(t, err)
		assert.Equal(t, [16]byte(src[:16]), first)
		assert.Equal(t, [16]byte(src[16:]), second)
	})
	t.Run("reads through short reads", func(t *testing.T) {
		src := seqBytes(0x01, 16)
		id, err := NewInstallationID(iotest.OneByteReader(bytes.NewReader(src)))
		require.NoError(t, err)
		assert.Equal(t, [16]byte(src), id)
	})
	t.Run("rejects an all-zero reader", func(t *testing.T) {
		id, err := NewInstallationID(bytes.NewReader(make([]byte, 16)))
		require.Error(t, err)
		assert.Equal(t, [16]byte{}, id)
	})
	t.Run("fails on a short reader", func(t *testing.T) {
		id, err := NewInstallationID(bytes.NewReader(seqBytes(0x01, 15)))
		require.Error(t, err)
		assert.Equal(t, [16]byte{}, id)
	})
}

// TestNewKeys_NilReaderUsesCryptoRand pins the nil-reader default without
// real entropy: under a fixed crypto/rand stream a nil reader repeats its
// draw, and another stream changes it.
func TestNewKeys_NilReaderUsesCryptoRand(t *testing.T) {
	// Not parallel: SetGlobalRandom affects the whole process.
	t.Run("installation ID", func(t *testing.T) {
		draw := func(seed uint64) [16]byte {
			cryptotest.SetGlobalRandom(t, seed)
			id, err := NewInstallationID(nil)
			require.NoError(t, err)
			return id
		}
		first := draw(1)
		assert.NotEqual(t, [16]byte{}, first)
		assert.Equal(t, first, draw(1))
		assert.NotEqual(t, first, draw(2))
	})
	t.Run("signing key", func(t *testing.T) {
		draw := func(seed uint64) string {
			cryptotest.SetGlobalRandom(t, seed)
			k, err := NewSigningKey(nil)
			require.NoError(t, err)
			return k.EncodeSeed()
		}
		first := draw(1)
		assert.NotEmpty(t, first)
		assert.Equal(t, first, draw(1))
		assert.NotEqual(t, first, draw(2))
	})
}

func TestParseVerificationKey(t *testing.T) {
	t.Parallel()
	pub := ed25519.PublicKey(mustHex(t, rfc8032PublicHex))
	std := base64.StdEncoding.EncodeToString(pub)
	urlSafe := base64.URLEncoding.EncodeToString(pub)
	require.NotEqual(t, std, urlSafe, "fixture: the key's base64 uses + or /")
	pkix, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)

	t.Run("accepts strict standard base64 of 32 bytes", func(t *testing.T) {
		got, err := ParseVerificationKey(std)
		require.NoError(t, err)
		assert.Equal(t, pub, got)
	})

	t.Run("format round-trips", func(t *testing.T) {
		for _, k := range []ed25519.PublicKey{pub, pubFromSeed(0x07), pubFromSeed(0xfe)} {
			s := FormatVerificationKey(k)
			assert.Equal(t, base64.StdEncoding.EncodeToString(k), s)
			back, err := ParseVerificationKey(s)
			require.NoError(t, err)
			assert.Equal(t, k, back)
		}
	})

	for _, tc := range []struct{ name, in string }{
		{"31 bytes", base64.StdEncoding.EncodeToString(pub[:31])},
		{"33 bytes", base64.StdEncoding.EncodeToString(append(bytes.Clone(pub), 0x01))},
		{"64-byte private key", base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(mustHex(t, rfc8032SeedHex)))},
		{"URL-safe alphabet", urlSafe},
		{"missing padding", base64.RawStdEncoding.EncodeToString(pub)},
		{"non-canonical trailing bits", nonCanonical(t, std)},
		{"trailing newline", std + "\n"},
		{"embedded line break", std[:20] + "\r\n" + std[20:]},
		{"surrounding spaces", " " + std + " "},
		{"hex", hex.EncodeToString(pub)},
		{"PEM", string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pkix}))},
		{"empty", ""},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			got, err := ParseVerificationKey(tc.in)
			requireRejected(t, err, ErrInvalidVerificationKey, tc.in)
			assert.Empty(t, got)
		})
	}
}

func TestParseKeyID(t *testing.T) {
	t.Parallel()
	id := KeyID(mustHex(t, rfc8032PublicHex))
	text := hex.EncodeToString(id[:])
	require.NotEqual(t, text, strings.ToUpper(text), "fixture: the key ID has a hex letter")

	t.Run("format and parse round-trip", func(t *testing.T) {
		assert.Equal(t, text, FormatKeyID(id))
		// A zero key ID is a legal SHA-256 prefix, unlike a zero installation ID.
		for _, want := range [][8]byte{id, {}, {0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8}} {
			s := hex.EncodeToString(want[:])
			got, err := ParseKeyID(s)
			require.NoError(t, err, s)
			assert.Equal(t, want, got)
			assert.Equal(t, s, FormatKeyID(want))
		}
	})

	for _, tc := range []struct{ name, in string }{
		{"uppercase", strings.ToUpper(text)},
		{"15 characters", text[:15]},
		{"17 characters", text + "0"},
		{"installation ID length", text + text},
		{"non-hex character", text[:15] + "g"},
		{"trailing newline", text + "\n"},
		{"0x prefix", "0x" + text},
		{"empty", ""},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			got, err := ParseKeyID(tc.in)
			requireRejected(t, err, ErrInvalidKeyID, tc.in)
			assert.Equal(t, [8]byte{}, got)
		})
	}
}

func TestParseSigningSeed(t *testing.T) {
	t.Parallel()
	seed := mustHex(t, rfc8032SeedHex)
	b64 := base64.StdEncoding.EncodeToString(seed)
	require.False(t, b64 == base64.URLEncoding.EncodeToString(seed), "fixture: the seed's base64 uses + or /")
	priv := ed25519.NewKeyFromSeed(seed)
	pkcs8, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)

	for _, tc := range []struct{ name, in string }{
		{"canonical", b64},
		{"trailing newline", b64 + "\n"},
		{"surrounding ASCII whitespace", " \t" + b64 + "\r\n"},
	} {
		t.Run("accepts "+tc.name, func(t *testing.T) {
			k, err := ParseSigningSeed(tc.in)
			require.NoError(t, err)
			assert.False(t, k.IsZero())
			assert.True(t, b64 == k.EncodeSeed(), "EncodeSeed is the standard base64 of the seed")
			assert.Equal(t, ed25519.PublicKey(mustHex(t, rfc8032PublicHex)), k.Public(), "expanded with ed25519.NewKeyFromSeed")
		})
	}

	for _, tc := range []struct{ name, in string }{
		{"31 bytes", base64.StdEncoding.EncodeToString(seed[:31])},
		{"33 bytes", base64.StdEncoding.EncodeToString(append(bytes.Clone(seed), 0x01))},
		{"64-byte expanded key", base64.StdEncoding.EncodeToString(priv)},
		{"PEM", string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))},
		{"PKCS#8 DER in base64", base64.StdEncoding.EncodeToString(pkcs8)},
		{"URL-safe alphabet", base64.URLEncoding.EncodeToString(seed)},
		{"missing padding", base64.RawStdEncoding.EncodeToString(seed)},
		{"non-canonical trailing bits", nonCanonical(t, b64)},
		{"embedded line break", b64[:20] + "\n" + b64[20:]},
		{"inner space", b64[:20] + " " + b64[20:]},
		{"surrounding no-break space", " " + b64 + " "}, // not ASCII whitespace
		{"hex", hex.EncodeToString(seed)},
		{"whitespace only", " \n"},
		{"empty", ""},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			k, err := ParseSigningSeed(tc.in)
			requireRejected(t, err, ErrInvalidSigningSeed, tc.in)
			assert.True(t, k.IsZero())
		})
	}
}

func TestNewSigningKey(t *testing.T) {
	t.Parallel()
	seed := mustHex(t, rfc8032SeedHex)

	t.Run("expands the 32 bytes read", func(t *testing.T) {
		k, err := NewSigningKey(bytes.NewReader(seed))
		require.NoError(t, err)
		assert.False(t, k.IsZero())
		assert.True(t, base64.StdEncoding.EncodeToString(seed) == k.EncodeSeed(), "EncodeSeed is the standard base64 of the seed")
		assert.Equal(t, ed25519.PublicKey(mustHex(t, rfc8032PublicHex)), k.Public())
		assert.Equal(t, KeyID(k.Public()), k.KeyID())

		parsed, err := ParseSigningSeed(k.EncodeSeed())
		require.NoError(t, err)
		assert.True(t, k.EncodeSeed() == parsed.EncodeSeed(), "EncodeSeed round-trips through ParseSigningSeed")
		assert.Equal(t, k.Public(), parsed.Public())
		assert.Equal(t, k.KeyID(), parsed.KeyID())
	})
	t.Run("reads through short reads", func(t *testing.T) {
		k, err := NewSigningKey(iotest.OneByteReader(bytes.NewReader(seed)))
		require.NoError(t, err)
		assert.True(t, base64.StdEncoding.EncodeToString(seed) == k.EncodeSeed(), "EncodeSeed is the standard base64 of the seed")
	})
	t.Run("fails on a short reader", func(t *testing.T) {
		partial := seed[:31]
		k, err := NewSigningKey(bytes.NewReader(partial))
		require.Error(t, err)
		assert.True(t, k.IsZero())
		for _, s := range []string{
			hex.EncodeToString(partial),
			base64.StdEncoding.EncodeToString(partial),
			strings.Trim(fmt.Sprintf("%d", partial), "[]"),
		} {
			assert.NotContains(t, err.Error(), s, "the error carries no key material")
		}
	})
}

func TestSigningKey_ZeroValue(t *testing.T) {
	t.Parallel()
	var k SigningKey
	env := goldenEnvelope(DomainBound, ProvenanceNone)
	var err error
	assert.NotPanics(t, func() {
		assert.True(t, k.IsZero())
		assert.Empty(t, k.Public())
		assert.Zero(t, len(k.EncodeSeed()), "the zero key encodes no seed")
		_ = k.KeyID()
		err = k.SignEnvelope(env)
	})
	assert.Error(t, err, "a zero key must not sign")
	assert.Equal(t, [64]byte{}, env.Signature)
}

func TestSigningKey_SignEnvelope(t *testing.T) {
	t.Parallel()
	seed := mustHex(t, rfc8032SeedHex)
	wantID := KeyID(mustHex(t, rfc8032PublicHex))
	k, err := ParseSigningSeed(base64.StdEncoding.EncodeToString(seed))
	require.NoError(t, err)

	env := goldenEnvelope(DomainBound, ProvenanceSourceLink)
	require.NoError(t, k.SignEnvelope(env))
	assert.Equal(t, wantID, k.KeyID())
	assert.Equal(t, wantID, env.KeyID)

	// Ed25519 is deterministic: SignEnvelope is Envelope.Sign with the
	// expanded seed, byte for byte.
	want := goldenEnvelope(DomainBound, ProvenanceSourceLink)
	require.NoError(t, want.Sign(ed25519.NewKeyFromSeed(seed)))
	assert.Equal(t, want.Marshal(), env.Marshal())

	keys, err := KeySet(k.Public(), nil)
	require.NoError(t, err)
	res := Admit(goldenIdentity(), keys, env.Marshal())
	assert.Equal(t, OutcomeAdmitted, res.Outcome, "reason: %s", res.Reason)
	assert.Equal(t, env.TraceID, res.TraceID)

	invalid := goldenEnvelope(DomainBound, ProvenanceNone)
	invalid.Installation = [16]byte{}
	assert.Error(t, k.SignEnvelope(invalid), "Sign's envelope validation still applies")
}

func TestKeySet(t *testing.T) {
	t.Parallel()
	a, b, c := pubFromSeed(0x01), pubFromSeed(0x02), pubFromSeed(0x03)

	t.Run("keys active and retained keys by key ID", func(t *testing.T) {
		got, err := KeySet(a, nil)
		require.NoError(t, err)
		assert.Equal(t, map[[8]byte]ed25519.PublicKey{KeyID(a): a}, got)

		got, err = KeySet(a, []ed25519.PublicKey{b, c})
		require.NoError(t, err)
		assert.Equal(t, map[[8]byte]ed25519.PublicKey{KeyID(a): a, KeyID(b): b, KeyID(c): c}, got)
	})

	for _, tc := range []struct {
		name     string
		active   ed25519.PublicKey
		retained []ed25519.PublicKey
		want     error
	}{
		{"duplicate retained key", a, []ed25519.PublicKey{b, b}, ErrDuplicateKeyID},
		{"active key also retained", a, []ed25519.PublicKey{b, a}, ErrDuplicateKeyID},
		{"missing active key", nil, []ed25519.PublicKey{b}, ErrInvalidVerificationKey},
		{"31-byte active key", a[:31], nil, ErrInvalidVerificationKey},
		{"33-byte active key", append(bytes.Clone(a), 0x01), nil, ErrInvalidVerificationKey},
		{"64-byte private key as active", ed25519.PublicKey(ed25519.NewKeyFromSeed(mustHex(t, rfc8032SeedHex))), nil, ErrInvalidVerificationKey},
		{"31-byte retained key", a, []ed25519.PublicKey{b, c[:31]}, ErrInvalidVerificationKey},
		{"empty retained key", a, []ed25519.PublicKey{{}}, ErrInvalidVerificationKey},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			got, err := KeySet(tc.active, tc.retained)
			require.ErrorIs(t, err, tc.want)
			assert.Nil(t, got)
			for _, k := range append([]ed25519.PublicKey{tc.active}, tc.retained...) {
				if len(k) > 0 {
					assert.NotContains(t, err.Error(), base64.StdEncoding.EncodeToString(k))
					assert.NotContains(t, err.Error(), hex.EncodeToString(k))
				}
			}
		})
	}
}

// TestKeySet_AdmitsActiveAndRetained covers rotation through the configured
// key set: envelopes signed by the active key and by a retained key both
// admit with the configured installation, and each carries its signer's ID.
func TestKeySet_AdmitsActiveAndRetained(t *testing.T) {
	t.Parallel()
	retired, err := NewSigningKey(bytes.NewReader(seqBytes(0x01, ed25519.SeedSize)))
	require.NoError(t, err)
	active, err := NewSigningKey(bytes.NewReader(seqBytes(0x41, ed25519.SeedSize)))
	require.NoError(t, err)
	keys, err := KeySet(active.Public(), []ed25519.PublicKey{retired.Public()})
	require.NoError(t, err)

	expected := goldenIdentity()
	configured := hex.EncodeToString(expected.Installation[:])
	expected.Installation, err = ParseInstallationID(configured)
	require.NoError(t, err)

	for _, signer := range []struct {
		name string
		key  SigningKey
	}{{"active", active}, {"retained", retired}} {
		t.Run(signer.name, func(t *testing.T) {
			env := goldenEnvelope(DomainBound, ProvenanceNone)
			require.NoError(t, signer.key.SignEnvelope(env))
			assert.Equal(t, KeyID(signer.key.Public()), env.KeyID)
			res := Admit(expected, keys, env.Marshal())
			assert.Equal(t, OutcomeAdmitted, res.Outcome, "reason: %s", res.Reason)
			assert.Equal(t, env.TraceID, res.TraceID)
		})
	}
}

// TestAdmit_EmptyKeySet is the explicit fallback for an install without
// keys: a carrier gives unknown-key, no carrier gives absent. Guard: PR 1
// behaviour that key provisioning relies on.
func TestAdmit_EmptyKeySet(t *testing.T) {
	t.Parallel()
	raw, _ := signedGolden(t, DomainBound, ProvenanceNone)
	for _, keys := range []map[[8]byte]ed25519.PublicKey{nil, {}} {
		assert.Equal(t, Result{Outcome: OutcomeFallback, Reason: ReasonUnknownKey}, Admit(goldenIdentity(), keys, raw))
		assert.Equal(t, Result{Outcome: OutcomeFallback, Reason: ReasonAbsent}, Admit(goldenIdentity(), keys))
	}
}

// canarySigningKey loads the RFC 8032 seed through the public parser and
// returns every rendering of the seed that must never appear in output.
func canarySigningKey(t *testing.T) (SigningKey, []string) {
	t.Helper()
	seed := mustHex(t, rfc8032SeedHex)
	b64 := base64.StdEncoding.EncodeToString(seed)
	k, err := ParseSigningSeed(b64)
	require.NoError(t, err)
	assert.True(t, b64 == k.EncodeSeed(), "precondition: the key holds the canary seed")
	goSyntax := fmt.Sprintf("%#v", seed)
	return k, []string{
		b64,
		b64[:42], // shared with the base64 of the 64-byte private key
		base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed)),
		hex.EncodeToString(seed),
		strings.ToUpper(hex.EncodeToString(seed)),
		strings.Trim(fmt.Sprintf("%d", seed), "[]"), // Go's decimal byte rendering
		goSyntax[len("[]byte{") : len(goSyntax)-1],  // Go-syntax byte rendering
		string(seed),
		strings.Trim(strconv.Quote(string(seed)), `"`),
	}
}

func assertNoCanary(t *testing.T, out string, canaries []string) {
	t.Helper()
	for i, c := range canaries {
		assert.False(t, strings.Contains(out, c), "the output shows seed rendering %d", i)
	}
}

// TestSigningKey_RedactsFmt covers every fmt path. fmt calls Format for the
// key and for exported fields, but not for unexported fields or %p, so the
// key material itself must stay invisible to reflection.
func TestSigningKey_RedactsFmt(t *testing.T) {
	t.Parallel()
	k, canaries := canarySigningKey(t)
	type holder struct {
		Name string
		Key  SigningKey
	}
	type privateHolder struct {
		name string
		key  SigningKey
	}

	assert.True(t, k.String() == "[redacted]", "String redacts the key")
	assert.True(t, k.GoString() == "[redacted]", "GoString redacts the key")
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d"} {
		t.Run(verb, func(t *testing.T) {
			direct := fmt.Sprintf(verb, k)
			assert.True(t, direct == "[redacted]", "the key is redacted")
			pointer := fmt.Sprintf(verb, &k)
			assert.True(t, pointer == "[redacted]", "a pointer to the key is redacted")
			exported := fmt.Sprintf(verb, holder{Name: "trace", Key: k})
			assert.True(t, strings.Contains(exported, "[redacted]"), "an exported field holding the key is redacted")
			unexported := fmt.Sprintf(verb, privateHolder{name: "trace", key: k})
			for _, out := range []string{direct, pointer, exported, unexported} {
				assertNoCanary(t, out, canaries)
			}
		})
	}
	// The other verbs, widths and flags: Contains, so padding is not pinned.
	for _, verb := range []string{"%o", "%b", "%c", "%U", "%e", "%t", "%10v", "%-12s", "%08x", "% x", "%+q"} {
		t.Run(verb, func(t *testing.T) {
			for _, out := range []string{
				fmt.Sprintf(verb, k),
				fmt.Sprintf(verb, &k),
				fmt.Sprintf(verb, holder{Name: "trace", Key: k}),
			} {
				assert.True(t, strings.Contains(out, "[redacted]"), "the key is redacted")
				assertNoCanary(t, out, canaries)
			}
		})
	}
	t.Run("%p", func(t *testing.T) {
		assertNoCanary(t, fmt.Sprintf("%p", k), canaries)
	})
	t.Run("wrapped error", func(t *testing.T) {
		err := fmt.Errorf("load trace signing key: %+v", holder{Name: "trace", Key: k})
		assert.True(t, strings.Contains(err.Error(), "[redacted]"), "the wrapped key is redacted")
		assertNoCanary(t, err.Error(), canaries)
	})
}

func TestSigningKey_RedactsEncoders(t *testing.T) {
	t.Parallel()
	k, canaries := canarySigningKey(t)

	var tm encoding.TextMarshaler = k
	text, err := tm.MarshalText()
	require.NoError(t, err)
	assert.True(t, string(text) == "[redacted]", "MarshalText redacts the key")

	direct, err := json.Marshal(k)
	require.NoError(t, err)
	assert.True(t, string(direct) == `"[redacted]"`, "MarshalJSON redacts the key")

	nested, err := json.Marshal(struct {
		Key SigningKey `json:"key"`
	}{k})
	require.NoError(t, err)
	assert.True(t, string(nested) == `{"key":"[redacted]"}`, "a JSON field holding the key is redacted")

	for _, out := range [][]byte{text, direct, nested} {
		assertNoCanary(t, string(out), canaries)
	}
}

func TestSigningKey_RedactsSlog(t *testing.T) {
	t.Parallel()
	k, canaries := canarySigningKey(t)
	v := k.LogValue()
	assert.Equal(t, slog.KindString, v.Kind())
	assert.True(t, v.String() == "[redacted]", "LogValue redacts the key")

	type holder struct{ Key SigningKey }
	var buf bytes.Buffer
	for _, h := range []slog.Handler{slog.NewJSONHandler(&buf, nil), slog.NewTextHandler(&buf, nil)} {
		slog.New(h).Info("trace signing key loaded", "key", k, "holder", holder{Key: k})
	}
	out := buf.String()
	for _, want := range []string{`"key":"[redacted]"`, `"holder":{"Key":"[redacted]"}`, "key=[redacted]", "holder={Key:[redacted]}"} {
		assert.True(t, strings.Contains(out, want), "slog shows %s", want)
	}
	assertNoCanary(t, out, canaries)
}
