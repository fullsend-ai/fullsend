package tracecontext

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// v01Identity is the shared V01 fixture: one resolved identity per forge
// kind, exercised by the derivation, sensitivity, rename, and concurrency
// tests below.
func v01Identity() Identity {
	return Identity{
		Installation: [16]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
			0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		Instance:  "https://github.com",
		Container: 274390431,
		Kind:      KindWorkItem,
		Entity:    2468913570,
	}
}

// TestDeriveFallback_ResolvedAndNonzero is V01: the same resolved immutable
// identity derives a nonzero fallback, and repeated derivation in one
// process is deterministic.
func TestDeriveFallback_ResolvedAndNonzero(t *testing.T) {
	fb, ok := DeriveFallback(v01Identity())
	require.True(t, ok, "resolved identity must derive a fallback")
	assert.True(t, fb.TraceID.IsValid(), "derived TraceID must be nonzero")
	assert.True(t, fb.AnchorSpanID.IsValid(), "derived anchor SpanID must be nonzero")

	again, ok := DeriveFallback(v01Identity())
	require.True(t, ok)
	assert.Equal(t, fb, again, "derivation must be deterministic")
}

// TestDeriveFallback_ComponentSensitivity is V01: changing any binding
// component changes the derived identity, so distinct
// instances, containers, kinds, or entities cannot alias.
func TestDeriveFallback_ComponentSensitivity(t *testing.T) {
	base, ok := DeriveFallback(v01Identity())
	require.True(t, ok)

	changed := v01Identity()
	changed.Instance = "https://ghe.example.com"
	other, ok := DeriveFallback(changed)
	require.True(t, ok)
	assert.NotEqual(t, base, other, "forge instance must change identity")

	changed = v01Identity()
	changed.Container = 274390432
	other, ok = DeriveFallback(changed)
	require.True(t, ok)
	assert.NotEqual(t, base, other, "container must change identity")

	changed = v01Identity()
	changed.Kind = KindChangeProposal
	other, ok = DeriveFallback(changed)
	require.True(t, ok)
	assert.NotEqual(t, base, other, "entity kind must change identity: GitLab issue and MR id spaces are independent")

	changed = v01Identity()
	changed.Entity = 2468913571
	other, ok = DeriveFallback(changed)
	require.True(t, ok)
	assert.NotEqual(t, base, other, "entity must change identity")
}

// TestDeriveFallback_InstallationExcluded pins the ADR 0154 rule: the
// fallback derivation covers forge instance, repository, and entity IDs —
// never the installation.
func TestDeriveFallback_InstallationExcluded(t *testing.T) {
	base, ok := DeriveFallback(v01Identity())
	require.True(t, ok)

	other := v01Identity()
	other.Installation = [16]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	got, ok := DeriveFallback(other)
	require.True(t, ok)
	assert.Equal(t, base, got, "installation must not participate in fallback derivation")
}

// TestDeriveFallback_Unresolved is V01/FR-002: missing identity components
// are explicit unresolved outcomes, never a random or zero-context guess.
func TestDeriveFallback_Unresolved(t *testing.T) {
	cases := map[string]Identity{
		"empty instance":  func() Identity { i := v01Identity(); i.Instance = ""; return i }(),
		"zero container":  func() Identity { i := v01Identity(); i.Container = 0; return i }(),
		"zero entity":     func() Identity { i := v01Identity(); i.Entity = 0; return i }(),
		"negative entity": func() Identity { i := v01Identity(); i.Entity = -1; return i }(),
		"invalid kind":    func() Identity { i := v01Identity(); i.Kind = EntityKind(0x00); return i }(),
		"unknown kind":    func() Identity { i := v01Identity(); i.Kind = EntityKind(0x7f); return i }(),
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			fb, ok := DeriveFallback(id)
			assert.False(t, ok, "unresolved identity must be explicit")
			assert.False(t, fb.TraceID.IsValid(), "unresolved identity must not fabricate a TraceID")
			assert.False(t, fb.AnchorSpanID.IsValid(), "unresolved identity must not fabricate a SpanID")
			assert.False(t, id.Resolved(), "Resolved must agree with DeriveFallback")
		})
	}
}

// TestIdentity_Resolved accepts the V01 fixture.
func TestIdentity_Resolved(t *testing.T) {
	assert.True(t, v01Identity().Resolved())
}

// TestDeriveFallback_RenameStable documents the D01 rename rule: display
// names never enter the tuple, so a rename cannot change the binding. Two
// identities built from different display names for the same database IDs
// are the same identity.
func TestDeriveFallback_RenameStable(t *testing.T) {
	before, ok := DeriveFallback(v01Identity())
	require.True(t, ok)

	renamed := v01Identity() // same database IDs; only display names changed
	after, ok := DeriveFallback(renamed)
	require.True(t, ok)
	assert.Equal(t, before, after, "a rename must not change trace identity")
}

// TestDeriveFallback_MoveIsNewBinding documents the D01 move rule: moving an
// entity to another bound container changes the binding (container or
// entity database ID), which is a new identity, never a rename.
func TestDeriveFallback_MoveIsNewBinding(t *testing.T) {
	base, ok := DeriveFallback(v01Identity())
	require.True(t, ok)

	// Jira project move: same issue id, new project container.
	moved := v01Identity()
	moved.Container = 999888777
	after, ok := DeriveFallback(moved)
	require.True(t, ok)
	assert.NotEqual(t, base, after, "a moved entity has a new binding")

	// GitHub issue transfer: new container and new entity id.
	transfered := v01Identity()
	transfered.Container = 111222333
	transfered.Entity = 987654321
	after, ok = DeriveFallback(transfered)
	require.True(t, ok)
	assert.NotEqual(t, base, after, "a transferred entity has a new binding")
}

// TestDeriveFallback_IIDsAreNotIdentity documents the D01 mapping: GitLab
// IIDs (and GitHub numbers, Jira keys) are display values. The same IID in
// two projects, or an issue and MR sharing a database id in different kind
// spaces, must not alias.
func TestDeriveFallback_IIDsAreNotIdentity(t *testing.T) {
	a := Identity{Instance: "https://gitlab.com", Container: 101, Kind: KindWorkItem, Entity: 555}
	b := Identity{Instance: "https://gitlab.com", Container: 102, Kind: KindWorkItem, Entity: 555}
	fa, ok := DeriveFallback(a)
	require.True(t, ok)
	fb, ok := DeriveFallback(b)
	require.True(t, ok)
	assert.NotEqual(t, fa, fb, "the same IID in another project is a different entity")
}

// TestDeriveFallback_ConcurrentFirstEvents is V01: concurrent first events
// converge on the same fallback. Run with -race.
func TestDeriveFallback_ConcurrentFirstEvents(t *testing.T) {
	const goroutines = 64
	results := make([]Fallback, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			fb, ok := DeriveFallback(v01Identity())
			if !ok {
				t.Error("concurrent derivation must resolve")
				return
			}
			results[slot] = fb
		}(i)
	}
	wg.Wait()
	for i := 1; i < goroutines; i++ {
		assert.Equal(t, results[0], results[i], "all first events must converge")
	}
	assert.True(t, results[0].TraceID.IsValid())
	assert.True(t, results[0].AnchorSpanID.IsValid())
}

// deriveChildEnv re-executes this test binary as a fresh process that
// derives the V01 fixture and prints the result, proving there is no shared
// in-memory state behind convergence.
const deriveChildEnv = "FULLSEND_TRACECONTEXT_DERIVE_CHILD=1"

// TestDeriveFallback_IdenticalAcrossProcesses is V01: two fresh processes
// derive identical nonzero fallbacks for the same immutable identity.
func TestDeriveFallback_IdenticalAcrossProcesses(t *testing.T) {
	if os.Getenv("FULLSEND_TRACECONTEXT_DERIVE_CHILD") == "1" {
		fb, ok := DeriveFallback(v01Identity())
		if !ok {
			fmt.Println("unresolved")
			os.Exit(1)
		}
		// Print raw byte slices: the otel ID types implement Stringer,
		// and %x on a Stringer would hex-encode the hex string.
		fmt.Printf("%x %x\n", fb.TraceID[:], fb.AnchorSpanID[:])
		os.Exit(0)
	}

	run := func() string {
		cmd := exec.Command(os.Args[0], "-test.run", "^TestDeriveFallback_IdenticalAcrossProcesses$")
		cmd.Env = append(os.Environ(), deriveChildEnv)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "child process: %s", out)
		return strings.TrimSpace(string(out))
	}
	first := run()
	second := run()

	assert.NotEqual(t, "unresolved", first)
	assert.Equal(t, first, second, "two fresh processes must derive identical fallbacks")
	assert.Equal(t, "c9bdb0e38247ebbe2e0f1039250c32d4 892a346c55356aca", first)

	parts := strings.Fields(first)
	require.Len(t, parts, 2, "child output: %q", first)
	assert.Len(t, parts[0], 32, "TraceID must be 16 bytes hex")
	assert.Len(t, parts[1], 16, "SpanID must be 8 bytes hex")
	assert.NotEqual(t, strings.Repeat("00", 16), parts[0], "TraceID must be nonzero")
	assert.NotEqual(t, strings.Repeat("00", 8), parts[1], "SpanID must be nonzero")
}

// TestNormalizeInstance covers the D01 instance form: lowercase
// scheme://host, no path, query, fragment, or user info.
func TestNormalizeInstance(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"github default", "https://github.com", "https://github.com"},
		{"gitlab default", "https://gitlab.com", "https://gitlab.com"},
		{"jira site", "https://acme.atlassian.net", "https://acme.atlassian.net"},
		{"GHES host", "https://ghe.example.com", "https://ghe.example.com"},
		{"uppercase host", "https://GitHub.com", "https://github.com"},
		{"uppercase scheme", "HTTPS://github.com", "https://github.com"},
		{"trailing slash", "https://github.com/", "https://github.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeInstance(tc.raw)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestNormalizeInstance_Rejects(t *testing.T) {
	for _, raw := range []string{
		"",
		"://missing-scheme",
		"https://github.com/owner/repo",
		"https://user:passgithub.com",
		"https://github.com?query=1",
		"https://github.com#fragment",
		"https://github.com?",
		"https://github.com#",
		"https://:443",
		"ftp://github.com",
		"https://" + strings.Repeat("a", 246),
		"https://\xff.example",
	} {
		_, err := NormalizeInstance(raw)
		assert.Error(t, err, "input %q must be rejected", raw)
	}
}

// These preimages and IDs were computed independently with Python struct
// (>H string lengths, >QBQ IDs/kind) and hashlib.sha256, not DeriveFallback.
func TestDeriveFallback_FixedVectors(t *testing.T) {
	for _, tc := range []struct {
		id                        Identity
		preimage, traceID, anchor string
	}{
		{v01Identity(), "002166756c6c73656e642e7472616365636f6e746578742e6964656e746974792e7631001268747470733a2f2f6769746875622e636f6d00000000105add9f01000000009328a1a2", "c9bdb0e38247ebbe2e0f1039250c32d4", "892a346c55356aca"},
		{Identity{Instance: "https://gitlab.com", Container: 101, Kind: KindChangeProposal, Entity: 555}, "002166756c6c73656e642e7472616365636f6e746578742e6964656e746974792e7631001268747470733a2f2f6769746c61622e636f6d000000000000006502000000000000022b", "407b66d9453075850f3ee7ef31d6ff1e", "a39491edef0440ae"},
		{Identity{Instance: "https://acme.atlassian.net", Container: 10000, Kind: KindWorkItem, Entity: 10100}, "002166756c6c73656e642e7472616365636f6e746578742e6964656e746974792e7631001a68747470733a2f2f61636d652e61746c61737369616e2e6e65740000000000002710010000000000002774", "535e9f2c6d8bd8a814edd68dceb60e9a", "7e00bd92068701ac"},
	} {
		t.Run(tc.id.Instance, func(t *testing.T) {
			digest := sha256.Sum256(mustHex(t, tc.preimage))
			assert.Equal(t, tc.traceID, fmt.Sprintf("%x", digest[:16]))
			assert.Equal(t, tc.anchor, fmt.Sprintf("%x", digest[16:24]))
			fb, ok := DeriveFallback(tc.id)
			require.True(t, ok)
			assert.Equal(t, tc.traceID, fb.TraceID.String())
			assert.Equal(t, tc.anchor, fb.AnchorSpanID.String())
		})
	}
}

func TestFallback_SyntheticAnchorExportLifecycle(t *testing.T) {
	seen := map[trace.SpanID]bool{}
	for range 2 {
		// Each provider models an independent launch with no retained state.
		exporter := tracetest.NewInMemoryExporter()
		provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter), sdktrace.WithSampler(sdktrace.AlwaysSample()))
		t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
		fb, ok := DeriveFallback(v01Identity())
		require.True(t, ok)
		parent := trace.NewSpanContext(trace.SpanContextConfig{TraceID: fb.TraceID, SpanID: fb.AnchorSpanID})
		ctx := trace.ContextWithRemoteSpanContext(context.Background(), parent)
		tracer := provider.Tracer("tracecontext-test")
		for range 2 {
			_, span := tracer.Start(ctx, "operation")
			span.End()
		}
		spans := exporter.GetSpans()
		require.Len(t, spans, 2, "only operation spans are exported")
		for _, span := range spans {
			assert.Equal(t, "c9bdb0e38247ebbe2e0f1039250c32d4", span.SpanContext.TraceID().String())
			assert.Equal(t, "892a346c55356aca", span.Parent.SpanID().String())
			assert.Equal(t, span.SpanContext.TraceID(), span.Parent.TraceID())
			assert.NotEqual(t, fb.AnchorSpanID, span.SpanContext.SpanID(), "anchor is never exported")
			assert.False(t, seen[span.SpanContext.SpanID()], "operations have distinct IDs across launches")
			seen[span.SpanContext.SpanID()] = true
		}
	}
}

// TestDeriveFallback_InstancesDifferByHost ensures normalization feeds
// derivation distinctly per host.
func TestDeriveFallback_InstancesDifferByHost(t *testing.T) {
	a := v01Identity()
	b := v01Identity()
	b.Instance = "https://ghe.example.com"
	fa, ok := DeriveFallback(a)
	require.True(t, ok)
	fb, ok := DeriveFallback(b)
	require.True(t, ok)
	assert.False(t, bytes.Equal(fa.TraceID[:], fb.TraceID[:]))
}
