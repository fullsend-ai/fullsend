package cli

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/telemetry/tracecontext"
)

// traceSecretNameRe matches the secret name on its own, not as the prefix
// of FULLSEND_TRACE_SIGNING_KEY_ID.
var traceSecretNameRe = regexp.MustCompile(`\b` + forge.SecretTraceSigningKey + `\b`)

// trace_context values as repos.yaml and the managed overlay render them.
var (
	traceInstallationIDRe = regexp.MustCompile(`installation_id:\s*["']?([0-9a-f]{32})\b`)
	traceActiveKeyRe      = regexp.MustCompile(`active:\s*["']?([A-Za-z0-9+/]{43}=)`)
)

// traceValue returns the value re captures in yaml, or "" when it has none.
func traceValue(re *regexp.Regexp, yaml string) string {
	if m := re.FindStringSubmatch(yaml); m != nil {
		return m[1]
	}
	return ""
}

// writtenTraceSeed returns the last FULLSEND_TRACE_SIGNING_KEY value written
// to repo, or "" when none was written. It is a seed: compare it, never
// print it.
func writtenTraceSeed(fc *forge.FakeClient, repo string) string {
	seed := ""
	for _, s := range fc.CreatedSecrets {
		if s.Owner+"/"+s.Repo == repo && s.Name == forge.SecretTraceSigningKey {
			seed = s.Value
		}
	}
	return seed
}

// hexTraceKeyID is the key ID of pub as 16 lowercase hex characters,
// derived with tracecontext.KeyID rather than the code under test.
func hexTraceKeyID(pub ed25519.PublicKey) string {
	id := tracecontext.KeyID(pub)
	return hex.EncodeToString(id[:])
}

// traceSeedForms decodes a written seed and returns its hex key ID, its
// public key as standard base64 (the form verification_keys holds), and
// the printable forms that must never appear in output: the 42-character
// base64 prefix it shares with the 64-byte private key, and its hex.
func traceSeedForms(t *testing.T, seedB64 string) (keyID, pub string, leaks []string) {
	t.Helper()
	seed, err := base64.StdEncoding.DecodeString(seedB64)
	require.NoError(t, err, "the secret holds standard base64")
	require.Equal(t, ed25519.SeedSize, len(seed), "the secret holds a 32-byte seed")
	pk := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	return hexTraceKeyID(pk), base64.StdEncoding.EncodeToString(pk), []string{seedB64[:42], hex.EncodeToString(seed)}
}

// requireTraceKeyWritten requires a FULLSEND_TRACE_SIGNING_KEY write to repo
// and, without printing them, that no text shows its seed; then that
// FULLSEND_TRACE_SIGNING_KEY_ID names the written key. It returns the key's
// ID and its public key in standard base64.
func requireTraceKeyWritten(t *testing.T, fc *forge.FakeClient, repo string, texts ...string) (keyID, pub string) {
	t.Helper()
	seed := writtenTraceSeed(fc, repo)
	require.True(t, seed != "", "%s gets a %s write", repo, forge.SecretTraceSigningKey)
	keyID, pub, leaks := traceSeedForms(t, seed)
	for i, s := range texts {
		for _, leak := range leaks {
			require.False(t, strings.Contains(s, leak), "text %d shows the seed of %s", i, repo)
		}
	}
	assert.Equal(t, keyID, fc.VariableValues[repo+"/"+forge.VarTraceSigningKeyID], "the variable names the written key of %s", repo)
	return keyID, pub
}

// indentLines prefixes every line of s with indent.
func indentLines(indent, s string) string {
	return indent + strings.ReplaceAll(strings.TrimSuffix(s, "\n"), "\n", "\n"+indent) + "\n"
}

// --rotate-trace-signing-key is a one-run boolean, off by default, whose help
// says that the first rotation provisions an existing install.
func TestReposInstallCmd_RotateTraceSigningKeyFlag(t *testing.T) {
	f := newReposInstallCmd().Flags().Lookup("rotate-trace-signing-key")
	require.NotNil(t, f)
	assert.Equal(t, "bool", f.Value.Type())
	assert.Equal(t, "false", f.DefValue)
	assert.Contains(t, f.Usage, "on an existing install without trace_context, the first rotation provisions the keys")
}

// A dry run of a fresh install, and the install that follows, name both
// trace names and show the installation ID and key ID, never the seed
// (ADR 0154). The dry run's key is never written, so the test cannot
// read its seed: the dry run's trace lines must show no value shaped like
// one, and the install's output, repos.yaml and the overlay must not show
// the written seed. The dry run leaves repos.yaml byte-identical and writes
// nothing. The install records the written seed's public key and the
// installation ID in repos.yaml and the managed overlay, and sets
// FULLSEND_TRACE_SIGNING_KEY_ID to the seed's key ID.
func TestRunReposInstall_TraceKeyOutputShowsIDsNeverSeed(t *testing.T) {
	persisted := regexp.MustCompile(`\[acme/api\] (Would persist|Persisted) config\.trace_context in the manifest entry: ` +
		`installation ID ([0-9a-f]{32}), new active key ID ([0-9a-f]{16}) \(fresh install\)`)
	// A 32-byte value as standard base64 or unpadded URL-safe base64. Its 64
	// hex digits match too, as does the private key in any of these forms.
	seedShaped := regexp.MustCompile(`[A-Za-z0-9+/]{43}=|[A-Za-z0-9_-]{43}`)
	manifestPath := writeTestManifest(t, githubUnmanagedManifestYAML)
	fc := newInstallFakeClient("acme/api")
	install := func(dryRun bool) string {
		t.Helper()
		opts := githubManagedInstallOpts(manifestPath, fc)
		opts.dryRun = dryRun
		var err error
		out := captureStdout(t, func() { err = runReposInstall(context.Background(), opts) })
		require.NoError(t, err)
		return out
	}

	dryOut := install(true)
	// Checked before any assertion that prints the dry run's output, on the
	// trace lines only, since other lines may hold long names, and with the
	// manifest path masked, since t.TempDir names it after this test: the
	// name contains "Trace" and is long enough to look like a seed.
	for _, line := range strings.Split(strings.ReplaceAll(dryOut, manifestPath, "<manifest>"), "\n") {
		if strings.Contains(strings.ToLower(line), "trace") {
			require.False(t, seedShaped.MatchString(line), "a dry-run trace line shows a seed-shaped value")
		}
	}
	assert.Equal(t, githubUnmanagedManifestYAML, mustReadFile(t, manifestPath), "a dry run leaves repos.yaml byte-identical")
	assertNoInstallWrites(t, fc)

	liveOut := install(false)
	manifest := mustReadFile(t, manifestPath)
	overlay := string(fc.FileContents[overlayPath("acme/api")])
	keyID, pub := requireTraceKeyWritten(t, fc, "acme/api", liveOut, manifest, overlay)

	dry := persisted.FindAllStringSubmatch(dryOut, -1)
	require.Len(t, dry, 1, "the dry run previews the repos.yaml edit once:\n%s", dryOut)
	assert.Equal(t, "Would persist", dry[0][1])
	assert.Regexp(t, `\[acme/api\] Would set secret `+traceSecretNameRe.String(), dryOut)
	assert.Contains(t, dryOut, "[acme/api] Would set variable "+forge.VarTraceSigningKeyID)
	live := persisted.FindAllStringSubmatch(liveOut, -1)
	require.Len(t, live, 1, "the install reports the repos.yaml edit once")
	assert.Equal(t, "Persisted", live[0][1])
	installationID := traceValue(traceInstallationIDRe, manifest)
	assert.Equal(t, live[0][2], installationID, "repos.yaml records the installation ID the install showed")
	assert.Equal(t, keyID, live[0][3], "the install shows the written seed's key ID")
	assert.Equal(t, pub, traceValue(traceActiveKeyRe, manifest), "repos.yaml's active key is the written seed's public key")
	assert.Equal(t, installationID, traceValue(traceInstallationIDRe, overlay), "the managed overlay carries the installation ID")
	assert.Equal(t, pub, traceValue(traceActiveKeyRe, overlay), "the managed overlay carries the active key")
	assert.Regexp(t, `\[acme/api\] Set secret `+traceSecretNameRe.String(), liveOut)
	assert.Contains(t, liveOut, "[acme/api] Set variable "+forge.VarTraceSigningKeyID)
}

// --rotate-trace-signing-key reaches the key planner: an existing install
// without trace_context gets keys only with the flag, its first rotation
// being the initial provisioning. The install writes the seed, sets the key
// ID variable to its key ID, records the block in repos.yaml with that key
// active and commits the same block in the managed overlay.
func TestRunReposInstall_RotateTraceSigningKeyProvisionsExistingInstall(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		t.Run(fmt.Sprintf("rotate=%t", rotate), func(t *testing.T) {
			manifestPath := writeTestManifest(t, githubUnmanagedManifestYAML)
			fc := newInstalledFakeClientCLI("acme/api")
			opts := githubManagedInstallOpts(manifestPath, fc)
			opts.rotateTraceSigningKey = rotate
			var err error
			out := captureStdout(t, func() { err = runReposInstall(context.Background(), opts) })
			require.NoError(t, err)

			manifest := mustReadFile(t, manifestPath)
			if !rotate {
				assert.Zero(t, len(writtenTraceSeed(fc, "acme/api")), "no %s write without the flag", forge.SecretTraceSigningKey)
				assert.NotContains(t, fc.VariableValues, "acme/api/"+forge.VarTraceSigningKeyID, "no key ID variable")
				assert.NotContains(t, manifest, "trace_context")
				return
			}
			overlay := string(fc.FileContents[overlayPath("acme/api")])
			keyID, pub := requireTraceKeyWritten(t, fc, "acme/api", out, manifest, overlay)
			installationID := traceValue(traceInstallationIDRe, manifest)
			require.NotEmpty(t, installationID, "the repos.yaml entry gains trace_context")
			assert.Equal(t, pub, traceValue(traceActiveKeyRe, manifest), "repos.yaml's active key is the written seed's public key")
			assert.Equal(t, installationID, traceValue(traceInstallationIDRe, overlay), "the commit delivers the installation ID")
			assert.Equal(t, pub, traceValue(traceActiveKeyRe, overlay), "the commit delivers the active key")
			assert.Contains(t, out, "[acme/api] Persisted config.trace_context in the manifest entry: installation ID "+installationID+
				", new active key ID "+keyID+" (rotation)")
		})
	}
}

// repos.yaml records the new key before the first forge write, so a failed
// seed write leaves the key recorded and the key ID variable absent. The next
// plain run writes a new key and the variable names it; the installation ID
// is kept and the key that was never written stays retained.
func TestRunReposInstall_TraceKeyRetryAfterSecretWriteFailure(t *testing.T) {
	manifestPath := writeTestManifest(t, githubUnmanagedManifestYAML)
	fc := newInstallFakeClient("acme/api")
	fc.Errors["CreateRepoSecret"] = errors.New("secret write refused")
	var err error
	captureStdout(t, func() { err = runReposInstall(context.Background(), githubManagedInstallOpts(manifestPath, fc)) })
	require.Error(t, err, "the failed secret write fails the install")

	first := mustReadFile(t, manifestPath)
	installationID := traceValue(traceInstallationIDRe, first)
	require.NotEmpty(t, installationID, "repos.yaml records trace_context before the secret write fails")
	unwritten := traceValue(traceActiveKeyRe, first)
	require.NotEmpty(t, unwritten, "repos.yaml records the new key as active")
	assert.NotContains(t, fc.VariableValues, "acme/api/"+forge.VarTraceSigningKeyID, "no key ID variable without the seed")

	delete(fc.Errors, "CreateRepoSecret")
	out := captureStdout(t, func() { err = runReposInstall(context.Background(), githubManagedInstallOpts(manifestPath, fc)) })
	require.NoError(t, err)

	second := mustReadFile(t, manifestPath)
	_, pub := requireTraceKeyWritten(t, fc, "acme/api", out, second)
	assert.NotEqual(t, unwritten, pub, "the retry generates a new key")
	assert.Equal(t, pub, traceValue(traceActiveKeyRe, second), "the written key is active")
	assert.Equal(t, installationID, traceValue(traceInstallationIDRe, second), "the installation ID is kept")
	assert.Regexp(t, `retained:\s*-\s*["']?`+regexp.QuoteMeta(unwritten), second, "the key that was never written stays retained")
}

// The token-scope check and the sign-off identity lookup only read, and they
// run before install plans any repos.yaml edit: when either fails, repos.yaml
// stays byte-identical, so it records no trace_context for a key that was
// never written, and nothing is written to the forge.
func TestRunReposInstall_ScopeOrIdentityFailureLeavesManifestUnchanged(t *testing.T) {
	cases := []struct {
		name, manifest, want string
		setup                func(fc *forge.FakeClient)
	}{
		{name: "token without the workflow scope", manifest: githubUnmanagedManifestYAML, want: "token is missing required scopes: workflow",
			setup: func(fc *forge.FakeClient) {
				fc.InstallationToken = false
				fc.TokenScopes = []string{"repo"}
			}},
		{name: "sign-off without a user identity", manifest: strings.Replace(githubUnmanagedManifestYAML, "github:\n", "github:\n  signoff: true\n", 1),
			want: "signoff requires a GitHub user identity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifestPath := writeTestManifest(t, tc.manifest)
			fc := newInstallFakeClient("acme/api")
			if tc.setup != nil {
				tc.setup(fc)
			}
			var err error
			out := captureStdout(t, func() { err = runReposInstall(context.Background(), githubManagedInstallOpts(manifestPath, fc)) })
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			// Compared without printing: no seed is written, so none can be
			// checked for first.
			assert.True(t, mustReadFile(t, manifestPath) == tc.manifest, "repos.yaml is byte-identical, with no trace_context")
			assert.False(t, strings.Contains(out, "Persisted"), "the run reports no repos.yaml edit")
			assertNoInstallWrites(t, fc)
		})
	}
}

// A committed config that trusts a key the repos.yaml entry does not list
// stops the install before repos.yaml is persisted or the forge is written,
// and the error names the key by its ID only.
func TestRunReposInstall_ContradictingCommittedConfigRefusedBeforeWrites(t *testing.T) {
	active, extra := testTraceVerificationKey(1), testTraceVerificationKey(3)
	manifest := githubUnmanagedManifestYAML + "      config:\n" +
		indentLines("        ", traceContextYAML(testTraceInstallationID, forge.SecretTraceSigningKey, active))
	manifestPath := writeTestManifest(t, manifest)
	fc := newInstalledFakeClientCLI("acme/api")
	fc.FileContents[overlayPath("acme/api")] = []byte(managedConfigMarker +
		traceContextYAML(testTraceInstallationID, forge.SecretTraceSigningKey, active, extra))
	fc.Secrets["acme/api/"+forge.SecretTraceSigningKey] = true
	fc.VariableValues["acme/api/"+forge.VarTraceSigningKeyID] = hexTraceKeyID(testTracePublicKey(1))
	fc.VariablesExist["acme/api/"+forge.VarTraceSigningKeyID] = true

	var err error
	out := captureStdout(t, func() { err = runReposInstall(context.Background(), githubManagedInstallOpts(manifestPath, fc)) })
	require.ErrorIs(t, err, repos.ErrTraceManifestStale)
	msg := err.Error()
	assert.True(t, strings.HasPrefix(msg, "planning trace signing keys: "), msg)
	assert.Contains(t, msg, "acme/api")
	assert.Contains(t, msg, "trusts key ID "+hexTraceKeyID(testTracePublicKey(3)), "the error names the extra key by its ID")
	for _, key := range []string{active, extra} {
		assert.NotContains(t, msg+out, key, "a verification key appears only as its ID")
	}
	assert.Equal(t, manifest, mustReadFile(t, manifestPath), "repos.yaml is not written")
	assertNoInstallWrites(t, fc)
}

// A fresh repository whose markerless .fullsend/config.yaml needs adoption
// is skipped by convergence (#8511), so the trace-key preflight skips it
// too: repos.yaml records no trace_context and no key is written.
func TestRunReposInstall_AdoptionSkippedRepoGetsNoTraceKey(t *testing.T) {
	manifestPath := writeTestManifest(t, githubUnmanagedManifestYAML)
	fc := newInstallFakeClient("acme/api")
	fc.FileContents[overlayPath("acme/api")] = []byte("kill_switch: false\n# hand-authored\n")
	var err error
	out := captureStdout(t, func() { err = runReposInstall(context.Background(), githubManagedInstallOpts(manifestPath, fc)) })
	require.Error(t, err)
	require.Zero(t, len(writtenTraceSeed(fc, "acme/api")), "no %s write", forge.SecretTraceSigningKey)
	assert.Contains(t, out, "FAILED: acme/api — adoption required")
	assert.Equal(t, githubUnmanagedManifestYAML, mustReadFile(t, manifestPath), "no key is recorded for the skipped repo")
	assert.NotContains(t, out, "trace_context")
	assert.NotContains(t, fc.VariableValues, "acme/api/"+forge.VarTraceSigningKeyID, "no key ID variable")
}

// A fresh install from a read-only repos.yaml installs the repository
// without trace keys: it writes neither the seed nor the key ID variable,
// leaves repos.yaml byte-identical and says which run provisions the keys.
// Asking for a rotation fails before any write. The entry declares its
// tracker, so install needs no other repos.yaml edit.
func TestRunReposInstall_ReadOnlyManifestFreshInstallSkipsTraceKeys(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		t.Run(fmt.Sprintf("rotate=%t", rotate), func(t *testing.T) {
			manifestPath := writeTestManifest(t, behaviourTestManifestYAML)
			require.NoError(t, os.Chmod(manifestPath, 0o444))
			fc := newInstallFakeClient("acme/api")
			opts := githubManagedInstallOpts(manifestPath, fc)
			opts.rotateTraceSigningKey = rotate
			var err error
			out := captureStdout(t, func() { err = runReposInstall(context.Background(), opts) })
			assert.Equal(t, behaviourTestManifestYAML, mustReadFile(t, manifestPath), "repos.yaml is byte-identical")
			if rotate {
				require.ErrorIs(t, err, repos.ErrTraceManifestReadOnly)
				assertNoInstallWrites(t, fc)
				return
			}
			require.NoError(t, err)
			require.Zero(t, len(writtenTraceSeed(fc, "acme/api")), "no %s write", forge.SecretTraceSigningKey)
			assert.NotEmpty(t, fc.CommittedFiles, "the repository is installed")
			assert.NotContains(t, fc.VariableValues, "acme/api/"+forge.VarTraceSigningKeyID, "no key ID variable")
			assert.NotContains(t, string(fc.FileContents[overlayPath("acme/api")]), "trace_context")
			assert.Equal(t, 1, strings.Count(out, "[acme/api] Trace signing keys not provisioned: repos.yaml is read-only; "+
				"a later repos install acme/api --rotate-trace-signing-key with a writable manifest provisions them"), out)
		})
	}
}

// A repository whose re-enrollment has not landed does not stop another
// repository's install. Its repos.yaml entry, seed and key ID variable belong
// to the new installation, while its default branch has no fullsend workflow
// and keeps the ended installation's trace_context. Install names the ended
// installation once, gives the fresh repository its keys, and the commit
// replaces the ended block with the entry's.
func TestRunReposInstall_PendingReenrollmentDoesNotBlockOtherRepos(t *testing.T) {
	const entryInstallationID = "fedcba9876543210fedcba9876543210"
	manifestPath := writeTestManifest(t, githubUnmanagedManifestYAML+"      config:\n"+
		indentLines("        ", traceContextYAML(entryInstallationID, forge.SecretTraceSigningKey, testTraceVerificationKey(3)))+
		"    - name: acme/web\n")
	fc := newInstallFakeClient("acme/api", "acme/web")
	fc.FileContents[overlayPath("acme/api")] = []byte(managedConfigMarker +
		traceContextYAML(testTraceInstallationID, forge.SecretTraceSigningKey, testTraceVerificationKey(1)))
	fc.Secrets["acme/api/"+forge.SecretTraceSigningKey] = true
	fc.VariableValues["acme/api/"+forge.VarTraceSigningKeyID] = hexTraceKeyID(testTracePublicKey(3))
	fc.VariablesExist["acme/api/"+forge.VarTraceSigningKeyID] = true

	var err error
	out := captureStdout(t, func() { err = runReposInstall(context.Background(), githubManagedInstallOpts(manifestPath, fc)) })
	require.NoError(t, err)
	require.Zero(t, len(writtenTraceSeed(fc, "acme/api")), "acme/api's key is healthy")
	_, webPub := requireTraceKeyWritten(t, fc, "acme/web", out)
	ended := regexp.MustCompile(regexp.QuoteMeta("[acme/api]") + `.*ended installation ` + testTraceInstallationID +
		`: the repos.yaml entry's installation replaces it and stops trusting key ID ` + hexTraceKeyID(testTracePublicKey(1)))
	assert.Len(t, ended.FindAllString(out, -1), 1, "install names the ended installation once:\n%s", out)
	assert.Equal(t, webPub, traceValue(traceActiveKeyRe, string(fc.FileContents[overlayPath("acme/web")])), "acme/web's commit carries its key")
	assert.Equal(t, entryInstallationID, traceValue(traceInstallationIDRe, string(fc.FileContents[overlayPath("acme/api")])),
		"the commit replaces the ended block")
}
