package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
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

// The first --rotate-trace-signing-key run of an existing install that a
// glob covers gives it a repos.yaml entry copied from the glob entry that
// selected it. Selection matches a glob case-sensitively, so acme/A*, listed
// first, covers acme/api only when case is ignored and does not select it:
// the entry copies acme/api*, and the commit keeps the committed kill_switch.
func TestRunReposInstall_FirstRotationCopiesTheGlobThatSelectedTheRepo(t *testing.T) {
	manifestPath := writeTestManifest(t, `version: 1
github:
  mint_url: https://mint.example.com
  fullsend_ref: v1.0.0
  inference:
    auth: vertex-wif
  repos:
    - name: acme/A*
      config:
        kill_switch: false
    - name: acme/api*
      config:
        kill_switch: true
`)
	fc := newInstalledFakeClientCLI("acme/api")
	fc.FileContents[overlayPath("acme/api")] = []byte(managedConfigMarker + "kill_switch: true\n")
	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.rotateTraceSigningKey = true
	var err error
	out := captureStdout(t, func() { err = runReposInstall(context.Background(), opts) })
	require.NoError(t, err)

	manifest := mustReadFile(t, manifestPath)
	overlay := string(fc.FileContents[overlayPath("acme/api")])
	keyID, _ := requireTraceKeyWritten(t, fc, "acme/api", out, manifest, overlay)
	assert.Contains(t, out, "[acme/api] Persisted config.trace_context in the new manifest entry acme/api, copied from the glob entry acme/api*: "+
		"installation ID "+traceValue(traceInstallationIDRe, manifest)+", new active key ID "+keyID+" (rotation)")
	assert.Contains(t, manifestEntryYAML(t, manifestPath, "acme/api"), "kill_switch: true", "the new entry copies acme/api*")
	assert.Contains(t, overlay, "kill_switch: true", "the commit keeps the committed kill_switch")
	assert.NotContains(t, overlay, "kill_switch: false")
}

// Install resolves the sign-off identity from the edited manifest, so a
// glob-covered repository's scaffold commit follows the signoff of its new
// entry, which install copies from the glob entry that selected it. acme/A*,
// listed first, covers acme/api only when case is ignored and does not
// select it: the entry copies acme/api*. A dry run reports the trailer the
// install adds.
func TestRunReposInstall_SignoffFollowsTheCopiedEntry(t *testing.T) {
	const signoff = "      signoff: true\n"
	globs := func(upper, lower string) string {
		return strings.TrimSuffix(githubUnmanagedManifestYAML, "    - name: acme/api\n") +
			"    - name: acme/A*\n" + upper + "    - name: acme/api*\n" + lower
	}
	cases := []struct {
		name, manifest string
		// installed makes acme/api an existing install, run with
		// --rotate-trace-signing-key.
		installed, dryRun bool
		// signed reports that the scaffold commit carries the trailer.
		signed bool
	}{
		{name: "fresh install", manifest: globs("", signoff), signed: true},
		{name: "first rotation of an existing install", manifest: globs("", signoff), installed: true, signed: true},
		{name: "signoff only on the glob that does not select it", manifest: globs(signoff, "")},
		{name: "dry run of the fresh install", manifest: globs("", signoff), dryRun: true, signed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifestPath := writeTestManifest(t, tc.manifest)
			fc := newInstallFakeClient("acme/api")
			if tc.installed {
				fc = newInstalledFakeClientCLI("acme/api")
			}
			fc.AuthenticatedUserIdentity = &forge.UserIdentity{Name: "Install User", Email: "install@example.com"}
			out, err := installTrace(t, manifestPath, fc, func(opts *reposInstallConfig) {
				opts.rotateTraceSigningKey = tc.installed
				opts.dryRun = tc.dryRun
			})
			assert.Empty(t, regexp.MustCompile(`FAILED: .*`).FindAllString(out, -1), "no repository fails")
			require.NoError(t, err)
			assert.Contains(t, regexp.MustCompile(`Install complete: .*`).FindString(out), ", 0 failed")
			if tc.dryRun {
				assert.Equal(t, "Would add trailer to GitHub scaffold commits: "+signoffTestTrailer,
					regexp.MustCompile(`Would add trailer to .*`).FindString(out))
				assert.Empty(t, scaffoldCommitMessages(fc), "a dry run commits nothing")
				return
			}
			if tc.signed {
				requireAllSignedOff(t, fc)
			} else {
				requireNoneSignedOff(t, fc)
			}
			requireTraceKeyWritten(t, fc, "acme/api", out, mustReadFile(t, manifestPath), string(fc.FileContents[overlayPath("acme/api")]))
		})
	}
}

// Install checks --fullsend-binary against the edited manifest, so the flag
// follows the vendor setting of a glob-covered repository's new entry, which
// install copies from the glob entry that selected it. acme/A*, listed first,
// covers acme/api only when case is ignored and does not select it: the
// entry copies acme/api*. A refusal stops the run before repos.yaml, a key or
// a commit is written, and a dry run gives the install's answer.
func TestRunReposInstall_VendorFlagsFollowTheCopiedEntry(t *testing.T) {
	const vendor = "      vendor: true\n"
	globs := func(upper, lower string) string {
		return strings.TrimSuffix(githubUnmanagedManifestYAML, "    - name: acme/api\n") +
			"    - name: acme/A*\n" + upper + "    - name: acme/api*\n" + lower
	}
	cases := []struct {
		name, manifest string
		dryRun         bool
		// refused reports that install refuses --fullsend-binary.
		refused bool
	}{
		{name: "vendor only on the glob that selects it", manifest: globs("", vendor)},
		{name: "vendor only on the glob that does not select it", manifest: globs(vendor, ""), refused: true},
		{name: "dry run with vendor only on the glob that selects it", manifest: globs("", vendor), dryRun: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifestPath := writeTestManifest(t, tc.manifest)
			fc := newInstallFakeClient("acme/api")
			out, err := installTrace(t, manifestPath, fc, func(opts *reposInstallConfig) {
				opts.repoFilter = []string{"acme/api"}
				opts.fullsendBinary = amd64VendorBinary(t)
				opts.dryRun = tc.dryRun
			})
			// The checks on out never print it whole.
			if tc.refused {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "--fullsend-binary is only used for repositories whose repos.yaml settings resolve vendor: true, and no selected repository does")
				assert.True(t, mustReadFile(t, manifestPath) == tc.manifest, "repos.yaml is byte-identical")
				assert.False(t, strings.Contains(out, "Persisted"), "the run reports no repos.yaml edit")
				assertNoInstallWrites(t, fc)
				return
			}
			require.NoError(t, err)
			assert.Contains(t, regexp.MustCompile(`Install complete: .*`).FindString(out), ", 0 failed")
			if tc.dryRun {
				assert.True(t, strings.Contains(out, "[acme/api] Would persist config.trace_context in the new manifest entry acme/api, copied from the glob entry acme/api*: "),
					"the dry run reports the entry copied from acme/api*")
				assert.True(t, mustReadFile(t, manifestPath) == tc.manifest, "a dry run leaves repos.yaml unchanged")
				assertNoInstallWrites(t, fc)
				return
			}
			requireTraceKeyWritten(t, fc, "acme/api", out, mustReadFile(t, manifestPath))
			assert.True(t, strings.Contains(out, "[acme/api] Persisted config.trace_context in the new manifest entry acme/api, copied from the glob entry acme/api*: "),
				"the install reports the entry copied from acme/api*")
			assert.Contains(t, manifestEntryYAML(t, manifestPath, "acme/api"), "vendor: true", "the new entry copies acme/api*")
			assert.True(t, bytes.Equal(fc.FileContents["acme/api/"+layers.VendoredBinaryPathPerRepo], minimalLinuxAmd64ELF),
				"the scaffold commit carries the vendored binary")
		})
	}
}

// repos.yaml records the new key before the first forge write, so a failed
// seed write leaves the key recorded and the key ID variable absent. The next
// plain run writes a new key and the variable names it; the installation ID
// is kept. Neither the variable nor the secret exists and nothing is
// committed, so the secret cannot hold the recorded key: the run replaces it
// instead of retaining it, and names its key ID.
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
	assert.NotContains(t, second, unwritten, "the key that was never written is not retained")
	assert.Equal(t, 1, strings.Count(out, notRetained(verificationKeyID(t, unwritten))), "the retry names the key it replaces")
}

// notRetained is how install names a recorded active key that it replaces
// because FULLSEND_TRACE_SIGNING_KEY cannot hold it.
func notRetained(keyID string) string {
	return "(recovery); key ID " + keyID + " is not retained: " + forge.SecretTraceSigningKey +
		" does not hold it and no committed config lists it"
}

// verificationKeyID returns the key ID of a public key in standard base64,
// the form trace_context.verification_keys holds.
func verificationKeyID(t *testing.T, pub string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(pub)
	require.NoError(t, err)
	require.Len(t, b, ed25519.PublicKeySize)
	return hexTraceKeyID(b)
}

// apiTrace returns acme/api's trace_context in the manifest at path, or nil.
// It holds public values only.
func apiTrace(t *testing.T, path string) *config.TraceContextConfig {
	t.Helper()
	m, err := repos.LoadManifest(context.Background(), path)
	require.NoError(t, err)
	for _, e := range m.AllRepos() {
		if e.Name == "acme/api" && e.Config.Writer() != nil {
			return e.Config.Writer().TraceContext()
		}
	}
	return nil
}

// vendorManifestKey is acme/api's .fullsend vendor manifest in the fake. A
// malformed one fails acme/api's convergence before its key step.
var vendorManifestKey = "acme/api/" + scaffold.VendorManifestPath(scaffold.PerRepoVendorPrefix)

// installTrace runs repos install with the manifest at path against fc,
// after change edits the options, and returns its output and error.
func installTrace(t *testing.T, path string, fc *forge.FakeClient, change func(*reposInstallConfig)) (string, error) {
	t.Helper()
	opts := githubManagedInstallOpts(path, fc)
	if change != nil {
		change(opts)
	}
	var err error
	out := captureStdout(t, func() { err = runReposInstall(context.Background(), opts) })
	return out, err
}

// A run that stops before acme/api's key step, at a run-level input check or
// at one of acme/api's convergence checks, has already recorded a new active
// key whose seed it never writes. The next run replaces that key instead of
// retaining it: FULLSEND_TRACE_SIGNING_KEY cannot hold it, since neither the
// variable nor the secret exists and nothing is committed or, after a
// rotation, the variable still names the previous key, which the committed
// config lists without the new one. Each of two failed runs keeps the
// installation ID, records another active key and names the one it
// replaces, and the retained keys stay as they were (the previous key, after
// a rotation). A run without the cause installs, and its commit delivers the
// entry's block.
func TestRunReposInstall_TraceKeyNeverWrittenIsNotRetained(t *testing.T) {
	cases := []struct {
		name string
		// rotate first installs acme/api, then fails its first run with
		// --rotate-trace-signing-key.
		rotate bool
		// cause makes a run stop before acme/api's key step, with want in
		// its error or output.
		cause func(fc *forge.FakeClient, opts *reposInstallConfig)
		want  string
	}{
		{name: "missing inference inputs", want: "no credentials were supplied", cause: func(_ *forge.FakeClient, opts *reposInstallConfig) {
			opts.inferenceProject, opts.inferenceProjectNumber = "", ""
		}},
		{name: "malformed vendor manifest", want: "parsing vendor manifest", cause: func(fc *forge.FakeClient, _ *reposInstallConfig) {
			fc.FileContents[vendorManifestKey] = []byte("{not a manifest")
		}},
		{name: "unused --jira-token", want: "--jira-token was supplied but no selected repository", cause: func(_ *forge.FakeClient, opts *reposInstallConfig) {
			opts.jiraToken = "unused-token"
		}},
		{name: "rotation, malformed vendor manifest", rotate: true, want: "parsing vendor manifest", cause: func(fc *forge.FakeClient, _ *reposInstallConfig) {
			fc.FileContents[vendorManifestKey] = []byte("{not a manifest")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTestManifest(t, githubUnmanagedManifestYAML)
			fc := newInstallFakeClient("acme/api")
			var retained []string
			if tc.rotate {
				_, err := installTrace(t, path, fc, nil)
				require.NoError(t, err, "acme/api installs")
				retained = []string{apiTrace(t, path).VerificationKeys.Active}
			}

			var installation, active string
			commits, secrets, variables := repoWrites(fc, "acme/api")
			for run := 1; run <= 2; run++ {
				out, err := installTrace(t, path, fc, func(opts *reposInstallConfig) {
					opts.rotateTraceSigningKey = tc.rotate && run == 1
					tc.cause(fc, opts)
				})
				require.Error(t, err, "run %d fails", run)
				// Neither is printed: a failed run writes no seed to check
				// them against first.
				require.True(t, strings.Contains(err.Error()+out, tc.want), "run %d stops at the expected check", run)
				c, s, v := repoWrites(fc, "acme/api")
				assert.Equal(t, []int{commits, secrets, variables}, []int{c, s, v}, "run %d writes nothing to acme/api", run)
				got := apiTrace(t, path)
				require.NotNil(t, got, "run %d records trace_context", run)
				if run == 1 {
					installation = got.InstallationID
					assert.Zero(t, strings.Count(out, "is not retained"), "run 1 replaces no key")
				} else {
					assert.Equal(t, installation, got.InstallationID, "the installation ID is kept")
					assert.NotEqual(t, active, got.VerificationKeys.Active, "run 2 records another key")
					assert.Equal(t, 1, strings.Count(out, notRetained(verificationKeyID(t, active))), "run 2 names the key it replaces")
				}
				assert.Equal(t, retained, got.VerificationKeys.Retained, "run %d keeps the retained keys as they were", run)
				active = got.VerificationKeys.Active
			}

			delete(fc.FileContents, vendorManifestKey)
			out, err := installTrace(t, path, fc, nil)
			require.NoError(t, err, "the run without the cause installs")
			manifest := mustReadFile(t, path)
			overlay := fc.FileContents[overlayPath("acme/api")]
			_, pub := requireTraceKeyWritten(t, fc, "acme/api", out, manifest, string(overlay))
			got := apiTrace(t, path)
			assert.Equal(t, installation, got.InstallationID, "the installation ID is kept")
			assert.Equal(t, pub, got.VerificationKeys.Active, "the written key is active")
			assert.Equal(t, retained, got.VerificationKeys.Retained, "the retained keys are as they were")
			assert.Equal(t, 1, strings.Count(out, notRetained(verificationKeyID(t, active))), "the run names the key it replaces")
			committed, err := config.ParsePerRepoConfig(overlay)
			require.NoError(t, err)
			assert.Equal(t, got, committed.TraceContext(), "the commit delivers the entry's block")
		})
	}
}

// A recovery replaces the recorded active key only when
// FULLSEND_TRACE_SIGNING_KEY cannot hold it, so it also replaces a key whose
// seed was written once but never committed, and still retains one key per
// failed run while the secret may hold the key:
//   - L2: the first rotation of an install without keys wrote its seed and
//     variable, its commit did not land, and repos uninstall
//     --uninstall-only deleted both; the next run replaces that key.
//   - H1: a hand-declared trace_context whose key no secret holds and no
//     committed config lists; the first run replaces it.
//   - R1: FULLSEND_TRACE_SIGNING_KEY_ID was deleted while the secret holds
//     the key; each run that stops before its key step retains one more.
//   - --uninstall-only after the install's commit landed: likewise.
func TestRunReposInstall_TraceKeyRecoveryBounds(t *testing.T) {
	uninstallOnly := func(t *testing.T, path string, fc *forge.FakeClient) {
		t.Helper()
		require.NoError(t, runReposUninstall(context.Background(), &reposUninstallConfig{
			manifest: path, yes: true, concurrency: 4, uninstallOnly: true, testClient: fc,
		}, []string{"acme/api"}), "repos uninstall --uninstall-only")
	}
	installed := func(t *testing.T, path string, fc *forge.FakeClient) {
		t.Helper()
		_, err := installTrace(t, path, fc, nil)
		require.NoError(t, err, "acme/api installs")
	}
	cases := []struct {
		name     string
		manifest string
		// existing makes acme/api an install without trace keys.
		existing bool
		// prepare brings acme/api to the row's state.
		prepare func(t *testing.T, path string, fc *forge.FakeClient)
		// failing makes two runs stop at acme/api's convergence checks;
		// otherwise one run installs.
		failing bool
		// replaced reports that a run replaces the recorded active key
		// instead of retaining it.
		replaced bool
	}{
		{name: "L2: first rotation written, commit not landed, then --uninstall-only", existing: true, replaced: true,
			prepare: func(t *testing.T, path string, fc *forge.FakeClient) {
				fc.Errors["CommitFiles"] = errors.New("commit refused")
				_, err := installTrace(t, path, fc, func(opts *reposInstallConfig) { opts.rotateTraceSigningKey = true })
				require.Error(t, err, "the rotation's commit fails")
				delete(fc.Errors, "CommitFiles")
				requireTraceKeyWritten(t, fc, "acme/api")
				uninstallOnly(t, path, fc)
			}},
		{name: "H1: hand-declared key, no secret, nothing committed", replaced: true,
			manifest: githubUnmanagedManifestYAML + "      config:\n" +
				indentLines("        ", traceContextYAML(testTraceInstallationID, forge.SecretTraceSigningKey, testTraceVerificationKey(1)))},
		{name: "R1: key ID variable deleted, secret present", failing: true,
			prepare: func(t *testing.T, path string, fc *forge.FakeClient) {
				installed(t, path, fc)
				delete(fc.VariableValues, "acme/api/"+forge.VarTraceSigningKeyID)
				delete(fc.VariablesExist, "acme/api/"+forge.VarTraceSigningKeyID)
			}},
		{name: "--uninstall-only after the install's commit landed", failing: true,
			prepare: func(t *testing.T, path string, fc *forge.FakeClient) {
				installed(t, path, fc)
				uninstallOnly(t, path, fc)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manifest := githubUnmanagedManifestYAML
			if tc.manifest != "" {
				manifest = tc.manifest
			}
			path := writeTestManifest(t, manifest)
			fc := newInstallFakeClient("acme/api")
			if tc.existing {
				fc = newInstalledFakeClientCLI("acme/api")
			}
			if tc.prepare != nil {
				tc.prepare(t, path, fc)
			}
			runs := 1
			if tc.failing {
				fc.FileContents[vendorManifestKey] = []byte("{not a manifest")
				runs = 2
			}
			for run := 1; run <= runs; run++ {
				before := apiTrace(t, path)
				require.NotNil(t, before, "acme/api's entry has trace_context")
				out, err := installTrace(t, path, fc, nil)
				got := apiTrace(t, path)
				require.NotNil(t, got)
				assert.Equal(t, before.InstallationID, got.InstallationID, "the installation ID is kept")
				assert.NotEqual(t, before.VerificationKeys.Active, got.VerificationKeys.Active, "run %d records a new key", run)
				if tc.replaced {
					requireTraceKeyWritten(t, fc, "acme/api", out, mustReadFile(t, path))
					require.NoError(t, err)
					assert.Equal(t, before.VerificationKeys.Retained, got.VerificationKeys.Retained, "the recorded key is replaced")
					assert.Equal(t, 1, strings.Count(out, notRetained(verificationKeyID(t, before.VerificationKeys.Active))), "the run names the key it replaces")
					continue
				}
				require.Error(t, err, "run %d fails", run)
				require.True(t, strings.Contains(err.Error()+out, "parsing vendor manifest"), "run %d stops before the key step", run)
				assert.Equal(t, append(slices.Clone(before.VerificationKeys.Retained), before.VerificationKeys.Active), got.VerificationKeys.Retained,
					"run %d retains one more key", run)
				assert.Zero(t, strings.Count(out, "is not retained"), "run %d replaces no key", run)
			}
		})
	}
}

// The token-scope check and the sign-off identity lookup only read, and they
// run before install writes repos.yaml: when either fails, repos.yaml stays
// byte-identical, so it records no trace_context for a key that was never
// written, and nothing is written to the forge.
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
// fails the repository before repos.yaml is persisted or the forge is
// written: it is listed as FAILED with an error that names the key by its ID
// only.
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
	require.EqualError(t, err, "1 repos failed")
	failed := regexp.MustCompile(`FAILED: acme/api — ` + regexp.QuoteMeta(repos.ErrTraceManifestStale.Error()) + `.*`).FindString(out)
	require.NotEmpty(t, failed, "acme/api is listed as FAILED with the refusal")
	assert.Contains(t, failed, "trusts key ID "+hexTraceKeyID(testTracePublicKey(3)), "the error names the extra key by its ID")
	for _, key := range []string{active, extra} {
		assert.NotContains(t, out, key, "a verification key appears only as its ID")
	}
	assert.Equal(t, manifest, mustReadFile(t, manifestPath), "repos.yaml is not written")
	assertNoInstallWrites(t, fc)
}

// repoWrites counts the commits, the secret writes and deletions, and the
// variable writes and deletions the fake recorded for repo. It returns
// counts only, so no seed is printed.
func repoWrites(fc *forge.FakeClient, repo string) (commits, secrets, variables int) {
	for _, c := range fc.CommittedFiles {
		if c.Owner+"/"+c.Repo == repo {
			commits++
		}
	}
	for _, c := range fc.CommittedFilesToBranch {
		if c.Owner+"/"+c.Repo == repo {
			commits++
		}
	}
	for _, records := range [][]forge.SecretRecord{fc.CreatedSecrets, fc.DeletedSecrets} {
		for _, s := range records {
			if s.Owner+"/"+s.Repo == repo {
				secrets++
			}
		}
	}
	for _, records := range [][]forge.VariableRecord{fc.Variables, fc.UpdatedVariables, fc.DeletedVariables} {
		for _, v := range records {
			if v.Owner+"/"+v.Repo == repo {
				variables++
			}
		}
	}
	return commits, secrets, variables
}

// manifestEntryYAML returns the repos.yaml entry named name in the manifest
// at path as YAML, or "" when the manifest has none. An entry holds public
// values only.
func manifestEntryYAML(t *testing.T, path, name string) string {
	t.Helper()
	m, err := repos.LoadManifest(context.Background(), path)
	require.NoError(t, err)
	for _, e := range m.AllRepos() {
		if e.Name == name {
			data, err := yaml.Marshal(e)
			require.NoError(t, err)
			return string(data)
		}
	}
	return ""
}

// A repository whose trace signing keys install refuses is skipped and
// listed as FAILED, like one that needs adoption (#8511), and the run exits
// non-zero, while acme/web converges. The refused repository gets no commit,
// no secret and no variable write, its committed config is unchanged and
// its repos.yaml entry is byte-identical: install records no tracker
// default for it and carves no entry for it out of a glob. Its layers and
// its infrastructure are not checked, so they cannot stop the run.
func TestRunReposInstall_TraceKeyRefusalSkipsOnlyThatRepo(t *testing.T) {
	const web = "    - name: acme/web\n"
	declared := strings.TrimPrefix(behaviourTestManifestYAML, "version: 1\ngithub:\n  repos:\n")
	block := indentLines("        ", traceContextYAML(testTraceInstallationID, forge.SecretTraceSigningKey, testTraceVerificationKey(1)))
	setKeyIDVar := func(fc *forge.FakeClient, value string) {
		fc.VariableValues["acme/api/"+forge.VarTraceSigningKeyID] = value
		fc.VariablesExist["acme/api/"+forge.VarTraceSigningKeyID] = true
	}
	malformed := func(fc *forge.FakeClient) { setKeyIDVar(fc, "not-a-key-id") }
	// installAPI makes acme/api an existing install whose committed
	// .fullsend/config.yaml is the managed overlay.
	installAPI := func(fc *forge.FakeClient, overlay string) {
		installed := newInstalledFakeClientCLI("acme/api")
		maps.Copy(fc.VariableValues, installed.VariableValues)
		maps.Copy(fc.Secrets, installed.Secrets)
		maps.Copy(fc.FileContents, installed.FileContents)
		fc.FileContents[overlayPath("acme/api")] = []byte(managedConfigMarker + overlay)
	}
	cases := []struct {
		name     string
		manifest string
		readOnly bool
		dryRun   bool
		setup    func(fc *forge.FakeClient)
		sentinel error
	}{
		{name: "malformed key ID variable", manifest: githubUnmanagedManifestYAML + web, setup: malformed,
			sentinel: repos.ErrTraceKeyIDInvalid},
		{name: "malformed key ID variable, dry run", manifest: githubUnmanagedManifestYAML + web, dryRun: true, setup: malformed,
			sentinel: repos.ErrTraceKeyIDInvalid},
		// As an uninstall by a release without this change leaves it.
		{name: "key ID variable on a keyless entry", manifest: githubUnmanagedManifestYAML + web,
			setup: func(fc *forge.FakeClient) { setKeyIDVar(fc, hexTraceKeyID(testTracePublicKey(1))) }, sentinel: repos.ErrTraceManifestStale},
		{name: "committed config trusts a key the entry lacks", manifest: githubUnmanagedManifestYAML + "      config:\n" + block + web,
			setup: func(fc *forge.FakeClient) {
				installAPI(fc, traceContextYAML(testTraceInstallationID, forge.SecretTraceSigningKey,
					testTraceVerificationKey(1), testTraceVerificationKey(3)))
				fc.Secrets["acme/api/"+forge.SecretTraceSigningKey] = true
				setKeyIDVar(fc, hexTraceKeyID(testTracePublicKey(1)))
			}, sentinel: repos.ErrTraceManifestStale},
		// A hand-declared trace_context without a seed is a recovery, which
		// a read-only repos.yaml cannot record.
		{name: "recovery from a read-only repos.yaml", manifest: behaviourTestManifestYAML + block + strings.Replace(declared, "acme/api", "acme/web", 1),
			readOnly: true, sentinel: repos.ErrTraceManifestReadOnly},
		{name: "glob-covered", manifest: strings.Replace(githubUnmanagedManifestYAML, "acme/api", "acme/*", 1), setup: malformed,
			sentinel: repos.ErrTraceKeyIDInvalid},
		{name: "layers the safety check rejects", manifest: githubUnmanagedManifestYAML + web,
			setup: func(fc *forge.FakeClient) {
				installAPI(fc, "kill_switch: true\n")
				malformed(fc)
			}, sentinel: repos.ErrTraceKeyIDInvalid},
		{name: "mint URL conflict", manifest: githubUnmanagedManifestYAML + "      config:\n        mint_url: https://other-mint.example.com\n" + web,
			setup: malformed, sentinel: repos.ErrTraceKeyIDInvalid},
		// The fake token has no user identity. acme/api gets no scaffold
		// commit, so its sign-off needs none and acme/web still installs.
		{name: "sign-off on the refused repository, token without a user identity",
			manifest: strings.Replace(githubUnmanagedManifestYAML, "    - name: acme/api\n", "    - name: acme/api\n      signoff: true\n", 1) + web,
			setup:    malformed, sentinel: repos.ErrTraceKeyIDInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTestManifest(t, tc.manifest)
			if tc.readOnly {
				require.NoError(t, os.Chmod(path, 0o444))
			}
			fc := newInstallFakeClient("acme/api", "acme/web")
			if tc.setup != nil {
				tc.setup(fc)
			}
			committed := string(fc.FileContents[overlayPath("acme/api")])
			entry := manifestEntryYAML(t, path, "acme/api")
			opts := githubManagedInstallOpts(path, fc)
			opts.dryRun = tc.dryRun
			var err error
			out := captureStdout(t, func() { err = runReposInstall(context.Background(), opts) })
			written := mustReadFile(t, path)

			// acme/web converges. Its seed is checked before any assertion
			// that prints the output.
			switch {
			case tc.dryRun:
				assertNoInstallWrites(t, fc)
				assert.Equal(t, tc.manifest, written, "a dry run leaves repos.yaml byte-identical")
				assert.Equal(t, 1, strings.Count(out, "[acme/web] Would persist config.trace_context"), "the dry run previews acme/web's key")
			case tc.readOnly:
				assert.Equal(t, tc.manifest, written, "repos.yaml is byte-identical")
				commits, _, _ := repoWrites(fc, "acme/web")
				assert.NotZero(t, commits, "acme/web is installed")
				assert.Zero(t, len(writtenTraceSeed(fc, "acme/web")), "acme/web goes ahead without keys")
				assert.Equal(t, 1, strings.Count(out, "[acme/web] Trace signing keys not provisioned: repos.yaml is read-only"), "one notice")
			default:
				_, pub := requireTraceKeyWritten(t, fc, "acme/web", out, written)
				assert.Equal(t, pub, traceValue(traceActiveKeyRe, string(fc.FileContents[overlayPath("acme/web")])), "acme/web's commit carries its key")
			}

			require.EqualError(t, err, "1 repos failed")
			assert.Equal(t, 1, strings.Count(out, "FAILED: "), "one repository fails")
			assert.Equal(t, 1, strings.Count(out, "FAILED: acme/api — "+tc.sentinel.Error()), "acme/api fails with the refusal")
			assert.Equal(t, 1, strings.Count(out, "[acme/api] skipped: "+tc.sentinel.Error()), "acme/api is skipped with the refusal")
			assert.NotRegexp(t, `\[acme/api\] (Persisted|Would persist)`, out, "no repos.yaml edit for acme/api")
			commits, secrets, variables := repoWrites(fc, "acme/api")
			assert.Zero(t, commits, "no commit for acme/api")
			assert.Zero(t, secrets, "no secret write for acme/api")
			assert.Zero(t, variables, "no variable write or deletion for acme/api")
			assert.Equal(t, committed, string(fc.FileContents[overlayPath("acme/api")]), "acme/api's committed config is unchanged")
			assert.Equal(t, entry, manifestEntryYAML(t, path, "acme/api"), "acme/api's repos.yaml entry is unchanged")
		})
	}
}

// --rotate-trace-signing-key from a read-only repos.yaml stops the run
// before any write, also with two repositories: the run cannot record any
// rotation it asks for.
func TestRunReposInstall_ReadOnlyRotationStopsTheRun(t *testing.T) {
	manifest := behaviourTestManifestYAML +
		strings.Replace(strings.TrimPrefix(behaviourTestManifestYAML, "version: 1\ngithub:\n  repos:\n"), "acme/api", "acme/web", 1)
	path := writeTestManifest(t, manifest)
	require.NoError(t, os.Chmod(path, 0o444))
	fc := newInstallFakeClient("acme/api", "acme/web")
	opts := githubManagedInstallOpts(path, fc)
	opts.rotateTraceSigningKey = true
	var err error
	captureStdout(t, func() { err = runReposInstall(context.Background(), opts) })
	require.ErrorIs(t, err, repos.ErrTraceManifestReadOnly)
	assert.Contains(t, err.Error(), "acme/api, acme/web", "the error names both repositories")
	assert.Equal(t, manifest, mustReadFile(t, path), "repos.yaml is byte-identical")
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
