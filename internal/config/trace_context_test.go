package config

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/telemetry/tracecontext"
)

// The tests in this file cover the trace_context block (ADR 0154):
// validation, the admission key map, and how the block moves through
// config.yaml, repos.yaml layers and the layered reader.

// traceTestInstallationID is a valid installation ID: 32 lowercase hex
// characters, not all zero.
const traceTestInstallationID = "5c0f1e2d3c4b5a69788796a5b4c3d2e1"

// traceTestKey returns a deterministic Ed25519 key whose seed is n repeated.
func traceTestKey(n byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{n}, ed25519.SeedSize))
}

// traceTestPub returns traceTestKey(n)'s public key as standard base64, the
// form verification_keys entries take.
func traceTestPub(n byte) string {
	return base64.StdEncoding.EncodeToString(traceTestKey(n).Public().(ed25519.PublicKey))
}

// validTraceContext returns a fresh complete block: active key 1, retained
// key 2.
func validTraceContext() *TraceContextConfig {
	return &TraceContextConfig{
		InstallationID:   traceTestInstallationID,
		SigningKey:       &TraceSigningKeyRef{Secret: TraceSigningKeySecret},
		VerificationKeys: &TraceVerificationKeys{Active: traceTestPub(1), Retained: []string{traceTestPub(2)}},
	}
}

// otherTraceContext returns a complete block that shares nothing with
// validTraceContext: another installation ID, active key 3, retained key 4.
func otherTraceContext() *TraceContextConfig {
	return &TraceContextConfig{
		InstallationID:   strings.Repeat("ab", 16),
		SigningKey:       &TraceSigningKeyRef{Secret: TraceSigningKeySecret},
		VerificationKeys: &TraceVerificationKeys{Active: traceTestPub(3), Retained: []string{traceTestPub(4)}},
	}
}

// traceContextDoc returns prefix followed by tc as the trace_context key of
// a YAML document.
func traceContextDoc(t *testing.T, prefix string, tc *TraceContextConfig) []byte {
	t.Helper()
	body, err := yaml.Marshal(map[string]*TraceContextConfig{"trace_context": tc})
	require.NoError(t, err)
	return append([]byte(prefix), body...)
}

// localTraceBlock returns the block a layer holds itself, read without the
// TraceContext accessor.
func localTraceBlock(r PerRepoConfigReader) *TraceContextConfig {
	c, ok := r.(*perRepoConfig)
	if !ok || c == nil {
		return nil
	}
	return c.TraceContextBlock
}

// mutateTraceBlock overwrites every value of tc, nested ones included.
func mutateTraceBlock(tc *TraceContextConfig) {
	tc.InstallationID = "mutated"
	tc.SigningKey.Secret = "mutated"
	tc.VerificationKeys.Active = "mutated"
	for i := range tc.VerificationKeys.Retained {
		tc.VerificationKeys.Retained[i] = "mutated"
	}
}

// assertNoTraceValues checks that err shows no value of tc, line by line
// for a multi-line value. A truncated echo leaks too, so a line longer than
// 12 characters is checked as every 12-character run of it; PEM armor lines
// carry no key material and are checked whole. The fixed secret name is
// exempt: an error may cite it as the expected value.
func assertNoTraceValues(t *testing.T, err error, tc *TraceContextConfig) {
	t.Helper()
	if tc == nil {
		return
	}
	values := []string{tc.InstallationID}
	if tc.SigningKey != nil && strings.TrimSpace(tc.SigningKey.Secret) != TraceSigningKeySecret {
		values = append(values, tc.SigningKey.Secret)
	}
	if tc.VerificationKeys != nil {
		values = append(values, tc.VerificationKeys.Active)
		values = append(values, tc.VerificationKeys.Retained...)
	}
	const window = 12
	for _, v := range values {
		for _, line := range strings.Split(v, "\n") {
			line = strings.TrimSpace(line)
			parts := []string{line}
			if len(line) > window && !strings.HasPrefix(line, "-----") {
				parts = parts[:0]
				for i := 0; i+window <= len(line); i++ {
					parts = append(parts, line[i:i+window])
				}
			}
			for _, s := range parts {
				if s != "" && !assert.NotContains(t, err.Error(), s, "an error names the field, never the value") {
					break
				}
			}
		}
	}
}

// traceTestIdentity is the identity the test envelopes bind, with the
// installation of validTraceContext.
func traceTestIdentity(t *testing.T) tracecontext.Identity {
	t.Helper()
	raw, err := hex.DecodeString(traceTestInstallationID)
	require.NoError(t, err)
	id := tracecontext.Identity{Instance: "https://github.com", Container: 1, Kind: tracecontext.KindWorkItem, Entity: 2}
	copy(id.Installation[:], raw)
	return id
}

// traceTestEnvelope returns an unsigned bound envelope for traceTestIdentity.
func traceTestEnvelope(t *testing.T) *tracecontext.Envelope {
	t.Helper()
	id := traceTestIdentity(t)
	return &tracecontext.Envelope{
		Domain:       tracecontext.DomainBound,
		Installation: id.Installation,
		Instance:     id.Instance,
		Container:    id.Container,
		Kind:         id.Kind,
		Entity:       id.Entity,
		TraceID:      trace.TraceID{1},
		ParentSpanID: trace.SpanID{1},
		Nonce:        [16]byte{1},
	}
}

func TestTraceContextValidate(t *testing.T) {
	with := func(edit func(*TraceContextConfig)) *TraceContextConfig {
		tc := validTraceContext()
		edit(tc)
		return tc
	}
	std := func(n int) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, n)) }
	seed := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, ed25519.SeedSize))
	der, err := x509.MarshalPKCS8PrivateKey(traceTestKey(9))
	require.NoError(t, err)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	tests := []struct {
		name     string
		tc       *TraceContextConfig
		wantPath string // empty: the block is valid
	}{
		{name: "nil block is valid", tc: nil},
		{name: "complete block is valid", tc: validTraceContext()},
		{name: "block without retained keys is valid", tc: with(func(tc *TraceContextConfig) { tc.VerificationKeys.Retained = nil })},

		{name: "empty block", tc: &TraceContextConfig{}, wantPath: "trace_context."},
		{name: "missing installation_id", tc: with(func(tc *TraceContextConfig) { tc.InstallationID = "" }), wantPath: "trace_context.installation_id"},
		{name: "uppercase installation_id", tc: with(func(tc *TraceContextConfig) { tc.InstallationID = strings.ToUpper(traceTestInstallationID) }), wantPath: "trace_context.installation_id"},
		{name: "short installation_id", tc: with(func(tc *TraceContextConfig) { tc.InstallationID = traceTestInstallationID[:30] }), wantPath: "trace_context.installation_id"},
		{name: "all-zero installation_id", tc: with(func(tc *TraceContextConfig) { tc.InstallationID = strings.Repeat("0", 32) }), wantPath: "trace_context.installation_id"},
		{name: "non-hex installation_id", tc: with(func(tc *TraceContextConfig) { tc.InstallationID = "zz" + traceTestInstallationID[2:] }), wantPath: "trace_context.installation_id"},

		{name: "missing signing_key", tc: with(func(tc *TraceContextConfig) { tc.SigningKey = nil }), wantPath: "trace_context.signing_key"},
		{name: "empty signing_key.secret", tc: with(func(tc *TraceContextConfig) { tc.SigningKey.Secret = "" }), wantPath: "trace_context.signing_key.secret"},
		{name: "lowercase secret name", tc: with(func(tc *TraceContextConfig) { tc.SigningKey.Secret = strings.ToLower(TraceSigningKeySecret) }), wantPath: "trace_context.signing_key.secret"},
		{name: "secret name with a trailing space", tc: with(func(tc *TraceContextConfig) { tc.SigningKey.Secret = TraceSigningKeySecret + " " }), wantPath: "trace_context.signing_key.secret"},
		{name: "PEM private key as secret", tc: with(func(tc *TraceContextConfig) { tc.SigningKey.Secret = pemKey }), wantPath: "trace_context.signing_key.secret"},
		{name: "base64 seed as secret", tc: with(func(tc *TraceContextConfig) { tc.SigningKey.Secret = seed }), wantPath: "trace_context.signing_key.secret"},
		{name: "GH_TOKEN as secret", tc: with(func(tc *TraceContextConfig) { tc.SigningKey.Secret = "GH_TOKEN" }), wantPath: "trace_context.signing_key.secret"},
		{name: "dispatch secret as secret", tc: with(func(tc *TraceContextConfig) { tc.SigningKey.Secret = "FULLSEND_DISPATCH_SECRET" }), wantPath: "trace_context.signing_key.secret"},

		{name: "missing verification_keys", tc: with(func(tc *TraceContextConfig) { tc.VerificationKeys = nil }), wantPath: "trace_context.verification_keys"},
		{name: "missing active key", tc: with(func(tc *TraceContextConfig) { tc.VerificationKeys.Active = "" }), wantPath: "trace_context.verification_keys.active"},
		{name: "unpadded active key", tc: with(func(tc *TraceContextConfig) {
			tc.VerificationKeys.Active = base64.RawStdEncoding.EncodeToString(traceTestKey(1).Public().(ed25519.PublicKey))
		}), wantPath: "trace_context.verification_keys.active"},
		{name: "URL-safe active key", tc: with(func(tc *TraceContextConfig) {
			tc.VerificationKeys.Active = base64.URLEncoding.EncodeToString(bytes.Repeat([]byte{0xff}, ed25519.PublicKeySize))
		}), wantPath: "trace_context.verification_keys.active"},
		{name: "31-byte active key", tc: with(func(tc *TraceContextConfig) { tc.VerificationKeys.Active = std(31) }), wantPath: "trace_context.verification_keys.active"},
		{name: "33-byte active key", tc: with(func(tc *TraceContextConfig) { tc.VerificationKeys.Active = std(33) }), wantPath: "trace_context.verification_keys.active"},
		{name: "non-base64 active key", tc: with(func(tc *TraceContextConfig) { tc.VerificationKeys.Active = "!!canary-active-key!!" }), wantPath: "trace_context.verification_keys.active"},
		{name: "bad retained[1]", tc: with(func(tc *TraceContextConfig) {
			tc.VerificationKeys.Retained = []string{traceTestPub(2), std(31)}
		}), wantPath: "trace_context.verification_keys.retained[1]"},
		{name: "empty retained[0]", tc: with(func(tc *TraceContextConfig) { tc.VerificationKeys.Retained = []string{""} }), wantPath: "trace_context.verification_keys.retained[0]"},
		{name: "retained key equal to the active key", tc: with(func(tc *TraceContextConfig) {
			tc.VerificationKeys.Retained = []string{traceTestPub(1)}
		}), wantPath: "trace_context.verification_keys"},
		{name: "retained key is the active key with a line break", tc: with(func(tc *TraceContextConfig) {
			active := traceTestPub(1)
			tc.VerificationKeys.Retained = []string{active[:20] + "\n" + active[20:]}
		}), wantPath: "trace_context.verification_keys"},
		{name: "retained key listed twice", tc: with(func(tc *TraceContextConfig) {
			tc.VerificationKeys.Retained = []string{traceTestPub(2), traceTestPub(2)}
		}), wantPath: "trace_context.verification_keys"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTraceContext(tt.tc)
			if tt.wantPath == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantPath, "the error names the field path")
			assertNoTraceValues(t, err, tt.tc)
		})
	}
}

// TestPerRepoConfigValidate_TraceContext covers the write path: Validate on
// a config.yaml and ValidateManagedLayer on a repos.yaml config block both
// reject a malformed block.
func TestPerRepoConfigValidate_TraceContext(t *testing.T) {
	bad := validTraceContext()
	bad.VerificationKeys.Retained = []string{traceTestPub(2), base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 31))}
	const wantPath = "trace_context.verification_keys.retained[1]"

	t.Run("config.yaml", func(t *testing.T) {
		good, err := ParsePerRepoConfigWriter(traceContextDoc(t, "version: \"1\"\n", validTraceContext()))
		require.NoError(t, err)
		require.NoError(t, good.Validate(), "a complete block passes")

		w, err := ParsePerRepoConfigWriter(traceContextDoc(t, "version: \"1\"\n", bad))
		require.NoError(t, err)
		err = w.Validate()
		require.Error(t, err, "Validate rejects a malformed trace_context block")
		assert.Contains(t, err.Error(), wantPath)
		assertNoTraceValues(t, err, bad)
	})

	t.Run("repos.yaml config block", func(t *testing.T) {
		var good ManagedConfig
		require.NoError(t, yaml.Unmarshal(traceContextDoc(t, "", validTraceContext()), &good))
		require.NoError(t, ValidateManagedLayer(good.Writer()), "a complete block passes")

		var layer ManagedConfig
		require.NoError(t, yaml.Unmarshal(traceContextDoc(t, "", bad), &layer))
		err := ValidateManagedLayer(layer.Writer())
		require.Error(t, err, "ValidateManagedLayer rejects a malformed trace_context block")
		assert.Contains(t, err.Error(), wantPath)
		assertNoTraceValues(t, err, bad)
	})
}

func TestTraceContextAccessors(t *testing.T) {
	tc := validTraceContext()
	inst, err := tc.Installation()
	require.NoError(t, err)
	assert.Equal(t, traceTestIdentity(t).Installation, inst, "Installation parses installation_id")
	active, err := tc.ActiveKey()
	require.NoError(t, err)
	assert.Equal(t, traceTestKey(1).Public().(ed25519.PublicKey), active, "ActiveKey parses verification_keys.active")

	short := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 31))
	tests := []struct {
		name string
		edit func(*TraceContextConfig)
		call func(*TraceContextConfig) error
	}{
		{
			name: "Installation rejects an uppercase installation_id",
			edit: func(tc *TraceContextConfig) { tc.InstallationID = strings.ToUpper(traceTestInstallationID) },
			call: func(tc *TraceContextConfig) error { _, err := tc.Installation(); return err },
		},
		{
			name: "ActiveKey rejects a 31-byte key",
			edit: func(tc *TraceContextConfig) { tc.VerificationKeys.Active = short },
			call: func(tc *TraceContextConfig) error { _, err := tc.ActiveKey(); return err },
		},
		{
			name: "KeySet rejects a retained key that is not 32 bytes",
			edit: func(tc *TraceContextConfig) { tc.VerificationKeys.Retained = []string{short} },
			call: func(tc *TraceContextConfig) error { _, err := tc.KeySet(); return err },
		},
		{
			name: "KeySet rejects a retained key equal to the active key",
			edit: func(tc *TraceContextConfig) { tc.VerificationKeys.Retained = []string{traceTestPub(1)} },
			call: func(tc *TraceContextConfig) error { _, err := tc.KeySet(); return err },
		},
		{
			name: "KeySet rejects a retained key listed twice",
			edit: func(tc *TraceContextConfig) {
				tc.VerificationKeys.Retained = []string{traceTestPub(2), traceTestPub(2)}
			},
			call: func(tc *TraceContextConfig) error { _, err := tc.KeySet(); return err },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := validTraceContext()
			tt.edit(tc)
			err := tt.call(tc)
			require.Error(t, err)
			assertNoTraceValues(t, err, tc)
		})
	}
}

// TestTraceContextKeySet checks the admission key map built from config: the
// active and retained keys under their derived key IDs, so envelopes signed
// before and after a rotation both admit.
func TestTraceContextKeySet(t *testing.T) {
	activeKey, retainedKey := traceTestKey(1), traceTestKey(2)
	activePub := activeKey.Public().(ed25519.PublicKey)
	retainedPub := retainedKey.Public().(ed25519.PublicKey)
	tc := validTraceContext()

	keys, err := tc.KeySet()
	require.NoError(t, err)
	assert.Equal(t, map[[8]byte]ed25519.PublicKey{
		tracecontext.KeyID(activePub):   activePub,
		tracecontext.KeyID(retainedPub): retainedPub,
	}, keys, "the map holds the active and retained keys under their key IDs")

	inst, err := tc.Installation()
	require.NoError(t, err)
	expected := traceTestIdentity(t)
	expected.Installation = inst
	for _, signer := range []struct {
		name string
		key  ed25519.PrivateKey
	}{{"active", activeKey}, {"retained", retainedKey}} {
		env := traceTestEnvelope(t)
		require.NoError(t, env.Sign(signer.key))
		res := tracecontext.Admit(expected, keys, env.Marshal())
		assert.Equal(t, tracecontext.OutcomeAdmitted, res.Outcome,
			"an envelope signed by the %s key admits with the configured installation (reason %q)", signer.name, res.Reason)
	}
}

func TestPerRepoConfig_TraceContextRoundTrip(t *testing.T) {
	t.Run("config.yaml", func(t *testing.T) {
		w, err := ParsePerRepoConfigWriter(traceContextDoc(t, "version: \"1\"\n", validTraceContext()))
		require.NoError(t, err)
		data, err := w.Marshal()
		require.NoError(t, err)
		assert.Contains(t, string(data), "trace_context:", "Marshal writes the block")
		parsed, err := ParsePerRepoConfig(data)
		require.NoError(t, err)
		assert.Equal(t, validTraceContext(), localTraceBlock(parsed), "the block survives Marshal then load")
	})

	t.Run("repos.yaml config block", func(t *testing.T) {
		var o ManagedConfig
		require.NoError(t, yaml.Unmarshal(traceContextDoc(t, "", validTraceContext()), &o))
		encoded, err := yaml.Marshal(o)
		require.NoError(t, err)
		var back ManagedConfig
		require.NoError(t, yaml.Unmarshal(encoded, &back))
		assert.Equal(t, validTraceContext(), localTraceBlock(back.Writer()), "the block survives a repos.yaml write-back")
	})
}

// TestMergeManaged_TraceContext checks that merging repos.yaml layers keeps
// the block, that a child block replaces the parent's whole block rather than
// merging field by field, and that the result shares no state with either
// layer.
func TestMergeManaged_TraceContext(t *testing.T) {
	// The child block has no retained keys, so a field-by-field merge would
	// show the parent's retained key. The row onto a parent without a block
	// keeps a retained key, so the no-shared-state check covers its copy.
	child := func() *TraceContextConfig {
		tc := validTraceContext()
		tc.VerificationKeys.Retained = nil
		return tc
	}
	layer := func(tc *TraceContextConfig) PerRepoConfigWriter {
		return &perRepoConfig{Runtime: "pi", TraceContextBlock: tc, parent: &perRepoDefaults{}}
	}
	tests := []struct {
		name          string
		parent, child PerRepoConfigWriter
		want          *TraceContextConfig
	}{
		{name: "parent block kept when the child has none", parent: layer(otherTraceContext()), child: layer(nil), want: otherTraceContext()},
		{name: "child block replaces the whole parent block", parent: layer(otherTraceContext()), child: layer(child()), want: child()},
		{name: "child block onto a parent without one", parent: layer(nil), child: layer(validTraceContext()), want: validTraceContext()},
		{name: "parent layer only", parent: layer(otherTraceContext()), want: otherTraceContext()},
		{name: "child layer only", child: layer(child()), want: child()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := func() string {
				out, err := yaml.Marshal([]*TraceContextConfig{localTraceBlock(tt.parent), localTraceBlock(tt.child)})
				require.NoError(t, err)
				return string(out)
			}
			before := snapshot()
			got := localTraceBlock(MergeManaged(tt.parent, tt.child))
			require.NotNil(t, got)
			// Compared as YAML: a nil and an empty retained list are the same block.
			wantYAML, err := yaml.Marshal(tt.want)
			require.NoError(t, err)
			gotYAML, err := yaml.Marshal(got)
			require.NoError(t, err)
			require.Equal(t, string(wantYAML), string(gotYAML))
			mutateTraceBlock(got)
			assert.Equal(t, before, snapshot(), "the merged block shares no state with either layer")
		})
	}
}

func TestManagedConfig_CloneKeepsTraceContext(t *testing.T) {
	var orig ManagedConfig
	require.NoError(t, yaml.Unmarshal(traceContextDoc(t, "", validTraceContext()), &orig))
	got := localTraceBlock(orig.Clone().Writer())
	require.Equal(t, validTraceContext(), got, "Clone keeps the trace_context block")
	mutateTraceBlock(got)
	assert.Equal(t, validTraceContext(), localTraceBlock(orig.Writer()), "the clone shares no state with the original")
}

func TestManagedConfig_WithTraceContext(t *testing.T) {
	t.Run("replaces the block with a copy", func(t *testing.T) {
		var recv ManagedConfig
		require.NoError(t, yaml.Unmarshal(traceContextDoc(t, "keep_history: false\n", otherTraceContext()), &recv))
		arg := validTraceContext()
		with := recv.WithTraceContext(arg)
		require.True(t, with.IsSet())
		assert.Equal(t, validTraceContext(), localTraceBlock(with.Writer()), "the new block replaces the old one")
		assert.False(t, with.Writer().ConfigKeepHistory(), "existing values are kept")
		assert.Equal(t, otherTraceContext(), localTraceBlock(recv.Writer()), "the receiver is not modified")
		mutateTraceBlock(arg)
		assert.Equal(t, validTraceContext(), localTraceBlock(with.Writer()), "the argument is copied")
	})

	t.Run("unset receiver", func(t *testing.T) {
		fromZero := ManagedConfig{}.WithTraceContext(validTraceContext())
		require.True(t, fromZero.IsSet(), "an unset receiver yields a set configuration")
		assert.Equal(t, validTraceContext(), localTraceBlock(fromZero.Writer()))
		assert.Equal(t, PerRepoDefaultRoles(), fromZero.Writer().ConfigRoles(), "roles are not declared")
		encoded, err := yaml.Marshal(fromZero)
		require.NoError(t, err)
		var top map[string]any
		require.NoError(t, yaml.Unmarshal(encoded, &top))
		assert.Len(t, top, 1, "only trace_context is declared")
		assert.Contains(t, top, "trace_context")
	})

	t.Run("nil removes the block", func(t *testing.T) {
		var recv ManagedConfig
		require.NoError(t, yaml.Unmarshal(traceContextDoc(t, "keep_history: false\n", validTraceContext()), &recv))
		removed := recv.WithTraceContext(nil)
		require.True(t, removed.IsSet())
		assert.Nil(t, localTraceBlock(removed.Writer()), "nil removes the block")
		assert.False(t, removed.Writer().ConfigKeepHistory(), "existing values are kept")
		assert.Equal(t, validTraceContext(), localTraceBlock(recv.Writer()), "the receiver is not modified")
	})
}

// TestManagedConfig_RejectsUnknownTraceContextField is a guard: repos.yaml
// strict decoding reaches inside trace_context, so a custom UnmarshalYAML
// must not bypass KnownFields there.
func TestManagedConfig_RejectsUnknownTraceContextField(t *testing.T) {
	for doc, typo := range map[string]string{
		"trace_context:\n  instalation_id: x\n":                   "instalation_id",
		"trace_context:\n  verification_keys:\n    retaind: []\n": "retaind",
	} {
		var o ManagedConfig
		err := yaml.Unmarshal([]byte(doc), &o)
		require.Error(t, err, doc)
		assert.Contains(t, err.Error(), typo)
	}
}

// TestPerRepoConfig_TraceContextHasNoParentFallback checks the reader
// accessor: it returns a copy of the overlay's own block, and a block in
// config.base.yaml is never inherited.
func TestPerRepoConfig_TraceContextHasNoParentFallback(t *testing.T) {
	base := traceContextDoc(t, "version: \"1\"\n", otherTraceContext())
	loaders := map[string]func(t *testing.T, overlay []byte) PerRepoConfigReader{
		"LayerOnBase": func(t *testing.T, overlay []byte) PerRepoConfigReader {
			w, err := ParsePerRepoConfigWriter(overlay)
			require.NoError(t, err)
			layered, err := LayerOnBase(w, base)
			require.NoError(t, err)
			return layered
		},
		"ParsePerRepoConfigWriterLayered": func(t *testing.T, overlay []byte) PerRepoConfigReader {
			layered, err := ParsePerRepoConfigWriterLayered(overlay, base)
			require.NoError(t, err)
			return layered
		},
	}
	for name, load := range loaders {
		t.Run(name, func(t *testing.T) {
			own := load(t, traceContextDoc(t, "", validTraceContext()))
			got := own.TraceContext()
			require.Equal(t, validTraceContext(), got, "the overlay's own block is read")
			mutateTraceBlock(got)
			assert.Equal(t, validTraceContext(), own.TraceContext(), "TraceContext returns a deep copy")

			bare := load(t, []byte("runtime: pi\n"))
			assert.Nil(t, bare.TraceContext(), "config.base.yaml never supplies trace_context")
		})
	}
}

func TestEffectiveYAML_TraceContext(t *testing.T) {
	base := traceContextDoc(t, "", otherTraceContext())
	render := func(t *testing.T, overlay []byte) []byte {
		t.Helper()
		w, err := ParsePerRepoConfigWriter(overlay)
		require.NoError(t, err)
		layered, err := LayerOnBase(w, base)
		require.NoError(t, err)
		data, err := EffectiveYAML(layered)
		require.NoError(t, err)
		return data
	}

	var got struct {
		TraceContext *TraceContextConfig `yaml:"trace_context"`
	}
	require.NoError(t, yaml.Unmarshal(render(t, traceContextDoc(t, "", validTraceContext())), &got))
	assert.Equal(t, validTraceContext(), got.TraceContext, "the overlay's block is rendered")
	assert.NotContains(t, string(render(t, []byte("runtime: pi\n"))), "trace_context", "a config.base.yaml block is not inherited")
}
