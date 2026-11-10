package cli

import (
	"bytes"
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
	"testing/cryptotest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/repos"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/telemetry/tracecontext"
)

// traceSecretNameRe matches the secret name on its own, not as the prefix
// of the FULLSEND_TRACE_SIGNING_KEY_ID ledger variable.
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
// to repo, or "" when none was written.
func writtenTraceSeed(fc *forge.FakeClient, repo string) string {
	seed := ""
	for _, s := range fc.CreatedSecrets {
		if s.Owner+"/"+s.Repo == repo && s.Name == forge.SecretTraceSigningKey {
			seed = s.Value
		}
	}
	return seed
}

// hexTraceKeyID is the v1 key ID of pub as 16 lowercase hex characters,
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

// healthyTraceInstall is an installed acme/api whose repos.yaml entry and
// committed overlay carry active key 1 and retained key 2, whose secret
// exists and whose ledger names key 1. It returns the repos.yaml path and
// text.
func healthyTraceInstall(t *testing.T) (manifestPath, manifest string, fc *forge.FakeClient) {
	t.Helper()
	active, retained := testTraceVerificationKey(1), testTraceVerificationKey(2)
	manifest = githubUnmanagedManifestYAML + "      config:\n" +
		traceContextYAML("        ", testTraceInstallationID, forge.SecretTraceSigningKey, active, retained)
	fc = newInstalledFakeClientCLI("acme/api")
	fc.FileContents[overlayPath("acme/api")] = []byte(managedConfigMarker +
		traceContextYAML("", testTraceInstallationID, forge.SecretTraceSigningKey, active, retained))
	fc.Secrets["acme/api/"+forge.SecretTraceSigningKey] = true
	fc.VariableValues["acme/api/"+forge.VarTraceSigningKeyID] = hexTraceKeyID(testTracePublicKey(1))
	fc.VariablesExist["acme/api/"+forge.VarTraceSigningKeyID] = true
	return writeTestManifest(t, manifest), manifest, fc
}

// TestReposInstallCmd_TraceKeyFlags pins the one-run trace key flags: a
// boolean rotation whose help says the first rotation provisions an
// existing install, and a repeatable removal flag (ADR 0154).
func TestReposInstallCmd_TraceKeyFlags(t *testing.T) {
	cmd := newReposInstallCmd()

	rotate := cmd.Flags().Lookup("rotate-trace-signing-key")
	require.NotNil(t, rotate)
	assert.Equal(t, "bool", rotate.Value.Type())
	assert.Contains(t, rotate.Usage, "provision", "the help says the first rotation provisions an existing install")

	remove := cmd.Flags().Lookup("remove-trace-verification-key")
	require.NotNil(t, remove)
	assert.Equal(t, "stringArray", remove.Value.Type(), "the removal flag is repeatable")
}

// A dry run of a fresh install, and the install that follows, name the
// trace signing secret and show the installation ID and key ID the
// install records; neither ever shows the seed (ADR 0154). crypto/rand is
// reset to the same stream before each run, so the dry run plans the key
// the install then writes. The install records the written seed's public
// key as active in repos.yaml and in the managed overlay.
func TestRunReposInstall_TraceKeyOutputShowsIDsNeverSeed(t *testing.T) {
	manifestPath := writeTestManifest(t, githubUnmanagedManifestYAML)
	fc := newInstallFakeClient("acme/api")
	install := func(dryRun bool) string {
		t.Helper()
		cryptotest.SetGlobalRandom(t, 577)
		opts := githubManagedInstallOpts(manifestPath, fc)
		opts.dryRun = dryRun
		var err error
		out := captureStdout(t, func() { err = runReposInstall(context.Background(), opts) })
		require.NoError(t, err)
		return out
	}

	dryOut := install(true)
	assert.Regexp(t, traceSecretNameRe, dryOut, "the dry run names %s", forge.SecretTraceSigningKey)
	assert.Equal(t, githubUnmanagedManifestYAML, mustReadFile(t, manifestPath), "a dry run leaves repos.yaml byte-identical")
	assertNoInstallWrites(t, fc)
	liveOut := install(false)

	seed := writtenTraceSeed(fc, "acme/api")
	require.NotEmpty(t, seed, "a fresh install writes %s", forge.SecretTraceSigningKey)
	keyID, pub, leaks := traceSeedForms(t, seed)
	manifest := mustReadFile(t, manifestPath)
	installationID := traceValue(traceInstallationIDRe, manifest)
	require.NotEmpty(t, installationID, "the repos.yaml entry records trace_context.installation_id")
	assert.Equal(t, pub, traceValue(traceActiveKeyRe, manifest), "repos.yaml's active key is the written seed's public key")
	overlay := string(fc.FileContents[overlayPath("acme/api")])
	assert.Equal(t, installationID, traceValue(traceInstallationIDRe, overlay), "the managed overlay carries the installation ID")
	assert.Equal(t, pub, traceValue(traceActiveKeyRe, overlay), "the managed overlay carries the active key")

	// The manifest edit is reported as done only by the run that persists it.
	assert.Contains(t, dryOut, "Would persist config.trace_context", "the dry run previews the repos.yaml edit")
	assert.NotContains(t, dryOut, "Persisted config.trace_context")
	assert.Contains(t, liveOut, "Persisted config.trace_context", "the install reports the repos.yaml edit it persisted")
	assert.NotContains(t, liveOut, "Would persist config.trace_context")
	for _, run := range []struct{ name, out string }{{"dry run", dryOut}, {"install", liveOut}} {
		assert.True(t, traceSecretNameRe.MatchString(run.out), "%s output names %s", run.name, forge.SecretTraceSigningKey)
		assert.True(t, strings.Contains(run.out, installationID), "%s output shows the installation ID", run.name)
		assert.True(t, strings.Contains(run.out, keyID), "%s output shows the key ID", run.name)
		for _, leak := range leaks {
			assert.False(t, strings.Contains(run.out, leak), "%s output never shows the seed", run.name)
		}
	}
}

// --rotate-trace-signing-key reaches the key planner: an existing install
// without trace_context gets keys only with the flag, its first rotation
// being the initial provisioning. The secret write reaches the forge,
// the ledger names the written key, and the repos.yaml entry gains the
// block with that key active.
func TestRunReposInstall_RotateTraceSigningKeyProvisionsExistingInstall(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rotate bool
	}{
		{name: "without the flag nothing is provisioned", rotate: false},
		{name: "the first rotation provisions keys", rotate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cryptotest.SetGlobalRandom(t, 578)
			manifestPath := writeTestManifest(t, githubUnmanagedManifestYAML)
			fc := newInstalledFakeClientCLI("acme/api")
			opts := githubManagedInstallOpts(manifestPath, fc)
			opts.rotateTraceSigningKey = tc.rotate
			var err error
			out := captureStdout(t, func() { err = runReposInstall(context.Background(), opts) })
			require.NoError(t, err)

			seed := writtenTraceSeed(fc, "acme/api")
			if !tc.rotate {
				assert.Zero(t, len(seed), "no %s write without the flag", forge.SecretTraceSigningKey)
				assert.NotContains(t, fc.VariableValues, "acme/api/"+forge.VarTraceSigningKeyID)
				assert.NotContains(t, mustReadFile(t, manifestPath), "trace_context")
				return
			}
			require.NotEmpty(t, seed, "the rotation writes %s", forge.SecretTraceSigningKey)
			keyID, pub, leaks := traceSeedForms(t, seed)
			assert.Equal(t, keyID, fc.VariableValues["acme/api/"+forge.VarTraceSigningKeyID], "the ledger names the written key")
			manifest := mustReadFile(t, manifestPath)
			assert.NotEmpty(t, traceValue(traceInstallationIDRe, manifest), "the repos.yaml entry gains trace_context")
			assert.Equal(t, pub, traceValue(traceActiveKeyRe, manifest), "repos.yaml's active key is the written seed's public key")
			for _, leak := range leaks {
				assert.False(t, strings.Contains(out, leak), "the output never shows the seed")
			}
		})
	}
}

// gitlabTraceUnset is the before_script entry that clears the seed in the
// shipped fullsend jobs, as the templates indent it, and gitlabTraceJobs are
// the templates that hold those jobs.
const gitlabTraceUnset = "    - unset " + forge.SecretTraceSigningKey + "\n"

var gitlabTraceJobs = []string{".gitlab/ci/fullsend-poll.yml", ".gitlab/ci/fullsend-dispatcher.yml", ".gitlab/ci/fullsend-agent.yml"}

// newEstablishedGitLabTraceFake is an installed group/project with its role
// credentials, whose default branch has the shipped poll, dispatcher and
// agent jobs without the unset line, as in a scaffold that predates it.
func newEstablishedGitLabTraceFake(t *testing.T) *forge.FakeClient {
	t.Helper()
	fc := newGitLabInstalledFake("group/project")
	for _, secret := range []string{forge.SecretGitLabPollerToken, forge.SecretGitLabAnalystToken, forge.SecretGitLabCoderToken} {
		fc.Secrets["group/project/"+secret] = true
	}
	for _, path := range gitlabTraceJobs {
		shipped, err := scaffold.GitLabPerRepoFile(path)
		require.NoError(t, err)
		require.Contains(t, string(shipped), gitlabTraceUnset, "the shipped %s unsets the seed", path)
		fc.FileContents["group/project/"+path] = bytes.Replace(shipped, []byte(gitlabTraceUnset), nil, 1)
	}
	return fc
}

// On an existing GitLab install whose default branch runs fullsend jobs that
// do not unset FULLSEND_TRACE_SIGNING_KEY yet, GitLab would pass the seed to
// them, so --rotate-trace-signing-key provisions nothing. The install and its
// dry run succeed, write neither the seed nor the ledger, leave repos.yaml
// unchanged and say why; the install commits the scaffold change whose jobs
// unset the variable first. Once that change is on the default branch, the
// rotation the notice names provisions the key.
func TestRunReposInstall_GitLabFirstRotationWaitsForUnsetJobs(t *testing.T) {
	notice := regexp.MustCompile(regexp.QuoteMeta("[group/project]") + `.*not provisioned.*` + forge.SecretTraceSigningKey +
		`.*` + regexp.QuoteMeta("run repos install group/project --rotate-trace-signing-key again"))
	cryptotest.SetGlobalRandom(t, 580)
	manifestPath := writeTestManifest(t, gitlabManagedManifestYAML)
	fc := newEstablishedGitLabTraceFake(t)
	install := func(dryRun bool) string {
		t.Helper()
		// The webhook route check needs the GitLab base URL.
		opts := &reposInstallConfig{manifest: manifestPath, concurrency: 4, dryRun: dryRun, rotateTraceSigningKey: true, testClient: triggerInstallBaseURLClient{fc}}
		var err error
		out := captureStdout(t, func() { err = runReposInstall(context.Background(), opts) })
		require.NoError(t, err, out)
		return out
	}

	for _, dryRun := range []bool{true, false} {
		out := install(dryRun)
		assert.Len(t, notice.FindAllString(out, -1), 1, "install says once why the keys wait and what to run again:\n%s", out)
		// Measured, not printed: a failure must not echo a seed.
		assert.Zero(t, len(writtenTraceSeed(fc, "group/project")), "no %s write", forge.SecretTraceSigningKey)
		assert.NotContains(t, fc.VariableValues, "group/project/"+forge.VarTraceSigningKeyID, "no ledger")
		assert.Equal(t, gitlabManagedManifestYAML, mustReadFile(t, manifestPath), "repos.yaml is unchanged")
		if dryRun {
			assertNoInstallWrites(t, fc)
		}
	}
	committed := committedSetupFiles(fc)
	for _, path := range gitlabTraceJobs {
		assert.Contains(t, string(committed[path]), gitlabTraceUnset, "install commits %s with the unset", path)
	}

	// The fake lands branch commits on the default branch too, as when the
	// merge request merges.
	out := install(false)
	seed := writtenTraceSeed(fc, "group/project")
	require.NotEmpty(t, seed, "the rotation writes %s once the jobs unset it", forge.SecretTraceSigningKey)
	keyID, pub, leaks := traceSeedForms(t, seed)
	assert.Equal(t, keyID, fc.VariableValues["group/project/"+forge.VarTraceSigningKeyID], "the ledger names the written key")
	assert.Equal(t, pub, traceValue(traceActiveKeyRe, mustReadFile(t, manifestPath)), "repos.yaml records the written key as active")
	assert.NotContains(t, out, "not provisioned")
	for _, leak := range leaks {
		assert.False(t, strings.Contains(out, leak), "the output never shows the seed")
	}
}

// A keyed GitLab repository gets no new key while the fullsend jobs on its
// default branch do not unset FULLSEND_TRACE_SIGNING_KEY, whatever the reason
// for the key: GitLab would pass the seed to them. With its secret missing,
// install writes neither the seed nor the ledger, leaves repos.yaml unchanged
// and says once why; it commits the scaffold change whose jobs unset the
// variable first. Once that change is on the default branch, the plain run
// the notice names writes the key and keeps the previous one retained.
func TestRunReposInstall_GitLabKeyRecoveryWaitsForUnsetJobs(t *testing.T) {
	notice := regexp.MustCompile(regexp.QuoteMeta("[group/project]") + `.*not written \(recovery\).*` + forge.SecretTraceSigningKey +
		`.*` + regexp.QuoteMeta("run repos install group/project again") + `.*default branch`)
	cryptotest.SetGlobalRandom(t, 581)
	active, activeID := testTraceVerificationKey(1), hexTraceKeyID(testTracePublicKey(1))
	manifest := gitlabManagedManifestYAML + traceContextYAML("        ", testTraceInstallationID, forge.SecretTraceSigningKey, active)
	manifestPath := writeTestManifest(t, manifest)
	fc := newEstablishedGitLabTraceFake(t)
	// The committed config and the ledger name the active key; its secret is
	// missing.
	fc.FileContents[overlayPath("group/project")] = []byte(managedConfigMarker +
		traceContextYAML("", testTraceInstallationID, forge.SecretTraceSigningKey, active))
	fc.VariableValues["group/project/"+forge.VarTraceSigningKeyID] = activeID
	fc.VariablesExist["group/project/"+forge.VarTraceSigningKeyID] = true
	install := func() string {
		t.Helper()
		// The webhook route check needs the GitLab base URL.
		opts := &reposInstallConfig{manifest: manifestPath, concurrency: 4, testClient: triggerInstallBaseURLClient{fc}}
		var err error
		out := captureStdout(t, func() { err = runReposInstall(context.Background(), opts) })
		require.NoError(t, err, out)
		return out
	}

	out := install()
	assert.Len(t, notice.FindAllString(out, -1), 1, "install says once why the key waits and what to run again:\n%s", out)
	assert.NotContains(t, out, "--rotate-trace-signing-key", "a recovery needs no rotation")
	// Measured, not printed: a failure must not echo a seed.
	assert.Zero(t, len(writtenTraceSeed(fc, "group/project")), "no %s write", forge.SecretTraceSigningKey)
	assert.Equal(t, activeID, fc.VariableValues["group/project/"+forge.VarTraceSigningKeyID], "no ledger write")
	assert.Equal(t, manifest, mustReadFile(t, manifestPath), "repos.yaml is unchanged")
	committed := committedSetupFiles(fc)
	for _, path := range gitlabTraceJobs {
		assert.Contains(t, string(committed[path]), gitlabTraceUnset, "install commits %s with the unset", path)
	}

	// The fake lands branch commits on the default branch too, as when the
	// merge request merges.
	out = install()
	seed := writtenTraceSeed(fc, "group/project")
	require.NotEmpty(t, seed, "install writes %s once the jobs unset it", forge.SecretTraceSigningKey)
	keyID, pub, leaks := traceSeedForms(t, seed)
	assert.Equal(t, keyID, fc.VariableValues["group/project/"+forge.VarTraceSigningKeyID], "the ledger names the written key")
	after := mustReadFile(t, manifestPath)
	assert.Equal(t, pub, traceValue(traceActiveKeyRe, after), "repos.yaml records the written key as active")
	assert.Regexp(t, `retained:\s*-\s*["']?`+regexp.QuoteMeta(active), after, "the previous key stays retained")
	assert.NotContains(t, out, "not written")
	for _, leak := range leaks {
		assert.False(t, strings.Contains(out, leak), "the output never shows the seed")
	}
}

// newFreshGitLabTraceFake is group/project with role credentials and
// pipeline schedules but no fullsend pipeline on its default branch, so
// install treats it as a fresh install. The fake upstream serves the shipped
// v1.0.0 GitLab templates with the unset line taken out of each fullsend job.
func newFreshGitLabTraceFake(t *testing.T) *forge.FakeClient {
	t.Helper()
	fc := newGitLabInstalledFake("group/project")
	delete(fc.FileContents, "group/project/.gitlab/ci/fullsend-pipeline.yml")
	for _, secret := range []string{forge.SecretGitLabPollerToken, forge.SecretGitLabAnalystToken, forge.SecretGitLabCoderToken} {
		fc.Secrets["group/project/"+secret] = true
	}
	for _, path := range gitlabTraceJobs {
		key := "fullsend-ai/fullsend/internal/scaffold/fullsend-repo-gitlab/" + path + "@v1.0.0"
		require.Contains(t, string(fc.FileContentsRef[key]), gitlabTraceUnset, "the shipped %s unsets the seed", path)
		fc.FileContentsRef[key] = bytes.Replace(fc.FileContentsRef[key], []byte(gitlabTraceUnset), nil, 1)
	}
	return fc
}

// A GitLab repository whose gitlab.fullsend_ref pins a release other than
// the running one gets that release's fullsend jobs from install. When they
// do not unset FULLSEND_TRACE_SIGNING_KEY, GitLab would pass them the seed,
// so a fresh install goes ahead without keys: it writes neither the seed nor
// the ledger, records no trace_context, says once which fullsend_ref to
// move and that a rotation then provisions the keys, and commits the pinned
// release's jobs. When the running release is that release, install delivers
// its own jobs, which unset the variable, and writes the key: the running
// release's ref and tag reach the trace-key preflight as they reach
// convergence.
func TestRunReposInstall_GitLabPinnedJobsWithoutUnsetGetNoKey(t *testing.T) {
	notice := regexp.MustCompile(regexp.QuoteMeta("[group/project]") + `.*not written.*` + regexp.QuoteMeta("v1.0.0") +
		`.*` + forge.SecretTraceSigningKey + `.*` + regexp.QuoteMeta("gitlab.fullsend_ref"))
	for _, tc := range []struct {
		name    string
		release bool
	}{
		{name: "development build"},
		{name: "release build of the pinned release", release: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.release {
				origSHA, origVersion := commitSHA, version
				commitSHA, version = "0123456789abcdef0123456789abcdef01234567", "1.0.0"
				t.Cleanup(func() { commitSHA, version = origSHA, origVersion })
			}
			manifestPath := writeTestManifest(t, gitlabManagedManifestYAML)
			fc := newFreshGitLabTraceFake(t)
			opts := &reposInstallConfig{manifest: manifestPath, concurrency: 4, testClient: triggerInstallBaseURLClient{fc}}
			var err error
			out := captureStdout(t, func() { err = runReposInstall(context.Background(), opts) })
			require.NoError(t, err, out)

			committed := committedSetupFiles(fc)
			if tc.release {
				seed := writtenTraceSeed(fc, "group/project")
				require.NotEmpty(t, seed, "install writes %s", forge.SecretTraceSigningKey)
				keyID, pub, leaks := traceSeedForms(t, seed)
				assert.Equal(t, keyID, fc.VariableValues["group/project/"+forge.VarTraceSigningKeyID], "the ledger names the written key")
				assert.Equal(t, pub, traceValue(traceActiveKeyRe, mustReadFile(t, manifestPath)), "repos.yaml records the written key as active")
				assert.NotRegexp(t, notice, out)
				for _, path := range gitlabTraceJobs {
					assert.Contains(t, string(committed[path]), gitlabTraceUnset, "install commits its own %s", path)
				}
				for _, leak := range leaks {
					assert.False(t, strings.Contains(out, leak), "the output never shows the seed")
				}
				return
			}
			assert.Len(t, notice.FindAllString(out, -1), 1, "install says once why no key is written:\n%s", out)
			assert.Regexp(t, `not written.*then run repos install group/project --rotate-trace-signing-key`, out, "the notice says how the keys are provisioned")
			// Measured, not printed: a failure must not echo a seed.
			assert.Zero(t, len(writtenTraceSeed(fc, "group/project")), "no %s write", forge.SecretTraceSigningKey)
			assert.NotContains(t, fc.VariableValues, "group/project/"+forge.VarTraceSigningKeyID, "no ledger")
			assert.NotContains(t, mustReadFile(t, manifestPath), "trace_context", "repos.yaml records no key")
			for _, path := range gitlabTraceJobs {
				require.Contains(t, committed, path, "install commits %s", path)
				assert.NotContains(t, string(committed[path]), gitlabTraceUnset, "install commits the pinned release's %s", path)
			}
		})
	}
}

// A fresh GitLab repository pinned to another release needs that release's
// jobs read before a key is planned, so without an upstream client the
// install stops before it writes repos.yaml or any key, naming the repository
// and the fullsend_ref. From a read-only repos.yaml the repository could not
// record a key anyway, so install goes ahead without keys, leaves repos.yaml
// byte-identical and says once that the keys were not provisioned;
// convergence then fails the repository on its own, naming the upstream
// GitHub client the pin needs.
func TestRunReposInstall_GitLabPinnedJobsUnreadable(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("read-only=%t", readOnly), func(t *testing.T) {
			manifest := gitlabManagedManifestYAML
			if readOnly {
				// The entry declares its tracking, so install needs no other
				// repos.yaml edit.
				manifest += "        tracker:\n          gitlab: {}\n"
			}
			manifestPath := writeTestManifest(t, manifest)
			if readOnly {
				require.NoError(t, os.Chmod(manifestPath, 0o444))
			}
			fc := newGitLabInstalledFake("group/project")
			delete(fc.FileContents, "group/project/.gitlab/ci/fullsend-pipeline.yml")
			factory := &filterForgeFactory{
				clients: map[string]forge.Client{repos.ForgeGitLab: triggerInstallBaseURLClient{fc}},
				errs:    map[string]error{repos.ForgeGitHub: errors.New("no GitHub token found")},
			}
			var err error
			out := captureStdout(t, func() {
				err = runReposInstall(context.Background(), &reposInstallConfig{manifest: manifestPath, concurrency: 4, testFactory: factory})
			})
			require.Error(t, err)
			assert.Equal(t, manifest, mustReadFile(t, manifestPath), "repos.yaml is not written")
			if !readOnly {
				for _, want := range []string{"group/project", "v1.0.0", "upstream GitHub client", "no GitHub token found"} {
					assert.Contains(t, err.Error(), want)
				}
				assertNoInstallWrites(t, fc)
				return
			}
			assert.Contains(t, err.Error(), "1 repos failed", "convergence fails the repository; the trace-key preflight does not stop the install")
			assert.Regexp(t, `FAILED: group/project — .*upstream GitHub client`, out, "convergence names what the pin needs")
			notice := regexp.MustCompile(regexp.QuoteMeta("[group/project]") + `.*not provisioned: repos.yaml is read-only`)
			assert.Len(t, notice.FindAllString(out, -1), 1, "install says once that the keys were not provisioned:\n%s", out)
			// Measured, not printed: a failure must not echo a seed.
			assert.Zero(t, len(fc.CreatedSecrets), "no secret writes")
			assert.NotContains(t, fc.VariableValues, "group/project/"+forge.VarTraceSigningKeyID, "no ledger")
		})
	}
}

// --remove-trace-verification-key reaches the key planner: naming a
// retained key's ID drops exactly that key from the repos.yaml entry of a
// healthy install and from its managed overlay, and keeps the active key
// and the installation ID. A removal alone is no rotation: no seed is
// written and the ledger still names the active key.
func TestRunReposInstall_RemoveTraceVerificationKeyEditsManifest(t *testing.T) {
	active, retained := testTraceVerificationKey(1), testTraceVerificationKey(2)
	manifestPath, _, fc := healthyTraceInstall(t)

	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.removeTraceVerificationKeys = []string{hexTraceKeyID(testTracePublicKey(2))}
	var err error
	captureStdout(t, func() { err = runReposInstall(context.Background(), opts) })
	require.NoError(t, err)

	after := mustReadFile(t, manifestPath)
	assert.NotContains(t, after, retained, "the named retained key leaves the repos.yaml entry")
	assert.Equal(t, active, traceValue(traceActiveKeyRe, after), "the active key stays active")
	assert.Equal(t, testTraceInstallationID, traceValue(traceInstallationIDRe, after), "the installation ID stays")
	assert.Zero(t, len(writtenTraceSeed(fc, "acme/api")), "a removal writes no new seed")
	assert.Equal(t, hexTraceKeyID(testTracePublicKey(1)), fc.VariableValues["acme/api/"+forge.VarTraceSigningKeyID],
		"the ledger still names the active key")
	overlay := string(fc.FileContents[overlayPath("acme/api")])
	assert.NotContains(t, overlay, retained, "the managed overlay drops the key too")
	assert.Equal(t, active, traceValue(traceActiveKeyRe, overlay), "the managed overlay keeps the active key")
}

// --remove-trace-verification-key refuses the retained key that
// FULLSEND_TRACE_SIGNING_KEY_ID names after an interrupted run, before
// repos.yaml is persisted or the forge is written, and never echoes its ID.
func TestRunReposInstall_RemoveTraceKeyTheLedgerNamesRefused(t *testing.T) {
	manifestPath, manifest, fc := healthyTraceInstall(t)
	retainedID := hexTraceKeyID(testTracePublicKey(2))
	fc.VariableValues["acme/api/"+forge.VarTraceSigningKeyID] = retainedID

	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.removeTraceVerificationKeys = []string{retainedID}
	var err error
	out := captureStdout(t, func() { err = runReposInstall(context.Background(), opts) })
	require.Error(t, err)
	for _, want := range []string{"--remove-trace-verification-key value 1 ", "acme/api", forge.VarTraceSigningKeyID} {
		assert.Contains(t, err.Error(), want)
	}
	assert.NotContains(t, err.Error()+out, retainedID, "the key ID is never echoed")
	assert.Equal(t, manifest, mustReadFile(t, manifestPath), "repos.yaml is byte-identical")
	assertNoInstallWrites(t, fc)
}

// An --remove-trace-verification-key value that is not exactly 16
// lowercase hex characters fails the install before any write, even when
// it is a near miss of a retained key's ID, and neither the error nor the
// output echoes it: a seed pasted by mistake must not reach logs.
func TestRunReposInstall_InvalidRemoveTraceVerificationKeyRejected(t *testing.T) {
	rid := hexTraceKeyID(testTracePublicKey(2)) // retained in the fixture; it has hex letters
	for _, tc := range []struct{ name, value string }{
		{name: "uppercase hex", value: strings.ToUpper(rid)},
		{name: "too short", value: rid[:12]},
		{name: "too long", value: rid + "00"},
		{name: "not hex", value: "zz" + rid[2:]},
		{name: "base64 seed", value: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, ed25519.SeedSize))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifestPath, manifest, fc := healthyTraceInstall(t)
			opts := githubManagedInstallOpts(manifestPath, fc)
			opts.removeTraceVerificationKeys = []string{tc.value}
			var err error
			out := captureStdout(t, func() { err = runReposInstall(context.Background(), opts) })
			require.Error(t, err)
			assert.Contains(t, err.Error(), "remove-trace-verification-key", "the error names the flag")
			assert.False(t, strings.Contains(err.Error(), tc.value), "the error never echoes the value")
			assert.False(t, strings.Contains(out, tc.value), "the output never echoes the value")
			assert.Equal(t, manifest, mustReadFile(t, manifestPath), "the manifest is not written")
			assertNoInstallWrites(t, fc)
		})
	}
}

// Through the command, repos.yaml records the new key before the
// first forge write, so a failed seed write leaves the key recorded and no
// ledger. The next plain run regenerates: the written key becomes active
// and the ledger names it, the installation ID is kept, and the key that
// was never written stays retained.
func TestRunReposInstall_TraceKeyRetryAfterSecretWriteFailure(t *testing.T) {
	cryptotest.SetGlobalRandom(t, 579)
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
	assert.NotContains(t, fc.VariableValues, "acme/api/"+forge.VarTraceSigningKeyID, "no ledger without the seed")

	delete(fc.Errors, "CreateRepoSecret")
	captureStdout(t, func() { err = runReposInstall(context.Background(), githubManagedInstallOpts(manifestPath, fc)) })
	require.NoError(t, err)

	seed := writtenTraceSeed(fc, "acme/api")
	require.NotEmpty(t, seed, "the retry writes %s", forge.SecretTraceSigningKey)
	keyID, pub, _ := traceSeedForms(t, seed)
	assert.NotEqual(t, unwritten, pub, "the retry generates a new key")
	assert.Equal(t, keyID, fc.VariableValues["acme/api/"+forge.VarTraceSigningKeyID], "the ledger names the written key")
	second := mustReadFile(t, manifestPath)
	assert.Equal(t, pub, traceValue(traceActiveKeyRe, second), "the written key is active")
	assert.Equal(t, installationID, traceValue(traceInstallationIDRe, second), "the installation ID is kept")
	assert.Regexp(t, `retained:\s*-\s*["']?`+regexp.QuoteMeta(unwritten), second, "the key that was never written stays retained")
}

// Through the command, a committed config that trusts a key repos.yaml
// does not list means repos.yaml is stale. The install stops before
// repos.yaml is persisted or the forge is written, and the error names the
// key by its ID only. A retained key in that state may come from a removal
// whose pull request has not merged yet, so the error names that case
// before an edit to the committed config, and its closing advice does not
// ask to restore the entry.
func TestRunReposInstall_StaleTraceManifestRefusedBeforeWrites(t *testing.T) {
	active, extra := testTraceVerificationKey(1), testTraceVerificationKey(3)
	manifest := githubUnmanagedManifestYAML + "      config:\n" +
		traceContextYAML("        ", testTraceInstallationID, forge.SecretTraceSigningKey, active)
	manifestPath := writeTestManifest(t, manifest)
	fc := newInstalledFakeClientCLI("acme/api")
	fc.FileContents[overlayPath("acme/api")] = []byte(managedConfigMarker +
		traceContextYAML("", testTraceInstallationID, forge.SecretTraceSigningKey, active, extra))
	fc.Secrets["acme/api/"+forge.SecretTraceSigningKey] = true
	fc.VariableValues["acme/api/"+forge.VarTraceSigningKeyID] = hexTraceKeyID(testTracePublicKey(1))
	fc.VariablesExist["acme/api/"+forge.VarTraceSigningKeyID] = true

	var err error
	out := captureStdout(t, func() { err = runReposInstall(context.Background(), githubManagedInstallOpts(manifestPath, fc)) })
	require.ErrorIs(t, err, repos.ErrTraceManifestStale)
	assert.Contains(t, err.Error(), "acme/api")
	assert.Contains(t, err.Error(), hexTraceKeyID(testTracePublicKey(3)), "the error names the extra key by ID")
	pending := strings.Index(err.Error(), "--remove-trace-verification-key run removed the key and that change has not merged yet")
	require.GreaterOrEqual(t, pending, 0, "the error names a removal whose change has not merged yet")
	assert.Greater(t, strings.Index(err.Error(), "may have been edited"), pending, "the pending removal comes before an edited config")
	assert.NotContains(t, err.Error(), "were not changed: restore", "the closing advice does not undo the pending removal")
	for _, key := range []string{active, extra} {
		assert.NotContains(t, err.Error()+out, key, "a verification key appears only as its ID")
	}
	assert.Equal(t, manifest, mustReadFile(t, manifestPath), "repos.yaml is not written")
	assertNoInstallWrites(t, fc)
}

// A repository whose re-enrollment has not landed does not stop another
// repository's install. Its repos.yaml entry, seed and ledger belong to the
// new installation, while its default branch has no fullsend workflow and
// keeps the ended installation's trace_context. Install names the ended
// installation, installs both repositories, and the commit replaces the
// ended block with the entry's.
func TestRunReposInstall_PendingReenrollmentDoesNotBlockOtherRepos(t *testing.T) {
	const entryInstallationID = "fedcba9876543210fedcba9876543210"
	manifestPath := writeTestManifest(t, githubUnmanagedManifestYAML+"      config:\n"+
		traceContextYAML("        ", entryInstallationID, forge.SecretTraceSigningKey, testTraceVerificationKey(3))+
		"    - name: acme/web\n")
	fc := newInstallFakeClient("acme/api", "acme/web")
	fc.FileContents[overlayPath("acme/api")] = []byte(managedConfigMarker +
		traceContextYAML("", testTraceInstallationID, forge.SecretTraceSigningKey, testTraceVerificationKey(1)))
	fc.Secrets["acme/api/"+forge.SecretTraceSigningKey] = true
	fc.VariableValues["acme/api/"+forge.VarTraceSigningKeyID] = hexTraceKeyID(testTracePublicKey(3))
	fc.VariablesExist["acme/api/"+forge.VarTraceSigningKeyID] = true

	var err error
	out := captureStdout(t, func() { err = runReposInstall(context.Background(), githubManagedInstallOpts(manifestPath, fc)) })
	require.NoError(t, err)
	ended := regexp.MustCompile(regexp.QuoteMeta("[acme/api]") + `.*ended installation ` + testTraceInstallationID +
		`.*stops trusting key ID ` + hexTraceKeyID(testTracePublicKey(1)))
	assert.Len(t, ended.FindAllString(out, -1), 1, "install names the ended installation once")
	assert.Zero(t, len(writtenTraceSeed(fc, "acme/api")), "acme/api's key is healthy")
	assert.NotEmpty(t, writtenTraceSeed(fc, "acme/web"), "acme/web gets its keys")
	assert.Equal(t, entryInstallationID, traceValue(traceInstallationIDRe, string(fc.FileContents[overlayPath("acme/api")])),
		"the commit replaces the ended block")
}

// A repository whose trigger-account containment failed is skipped by
// convergence, so the trace-key preflight skips it too: repos.yaml records
// no trace_context for a key that would never be written.
func TestRunReposInstall_ContainmentFailedRepoGetsNoTraceKey(t *testing.T) {
	manifestPath := writeTestManifest(t, triggerInstallManifest)
	fc := newTriggerInstallFake(t)
	opts := gitlabInstallOpts(manifestPath, fc)
	opts.testContainTriggerAccount = func(context.Context, forge.Client, string, string) error {
		return errors.New("restoring Developer access")
	}
	var err error
	out := captureStdout(t, func() { err = runReposInstall(context.Background(), opts) })
	require.Error(t, err)
	assert.NotContains(t, mustReadFile(t, manifestPath), "trace_context", "no key is recorded for the skipped repo")
	assert.NotContains(t, out, "trace_context")
	assert.Zero(t, len(writtenTraceSeed(fc, "group/project")))
}

// A fresh repository whose markerless .fullsend/config.yaml needs adoption
// is skipped by convergence (#8511), so the trace-key preflight skips it
// too: repos.yaml records no trace_context and no seed is written.
func TestRunReposInstall_AdoptionSkippedRepoGetsNoTraceKey(t *testing.T) {
	manifestPath := writeTestManifest(t, githubUnmanagedManifestYAML)
	fc := newInstallFakeClient("acme/api")
	fc.FileContents[overlayPath("acme/api")] = []byte("kill_switch: false\n# hand-authored\n")
	var err error
	out := captureStdout(t, func() { err = runReposInstall(context.Background(), githubManagedInstallOpts(manifestPath, fc)) })
	require.Error(t, err)
	assert.Contains(t, out, "FAILED: acme/api — adoption required")
	assert.Equal(t, githubUnmanagedManifestYAML, mustReadFile(t, manifestPath), "no key is recorded for the skipped repo")
	assert.NotContains(t, out, "trace_context")
	// Measured, not printed: a failure must not echo a seed.
	assert.Zero(t, len(writtenTraceSeed(fc, "acme/api")), "no seed is written")
}

// A removal whose key a read-only repos.yaml already dropped needs no
// manifest edit: the install leaves repos.yaml alone, and convergence drops
// the key from the managed overlay.
func TestRunReposInstall_RemoveTraceKeyAlreadyDroppedFromReadOnlyManifest(t *testing.T) {
	active, retained := testTraceVerificationKey(1), testTraceVerificationKey(2)
	manifest := githubUnmanagedManifestYAML + "      config:\n" +
		traceContextYAML("        ", testTraceInstallationID, forge.SecretTraceSigningKey, active)
	manifestPath := writeTestManifest(t, manifest)
	require.NoError(t, os.Chmod(manifestPath, 0o444))
	fc := newInstalledFakeClientCLI("acme/api")
	fc.FileContents[overlayPath("acme/api")] = []byte(managedConfigMarker +
		traceContextYAML("", testTraceInstallationID, forge.SecretTraceSigningKey, active, retained))
	fc.Secrets["acme/api/"+forge.SecretTraceSigningKey] = true
	fc.VariableValues["acme/api/"+forge.VarTraceSigningKeyID] = hexTraceKeyID(testTracePublicKey(1))
	fc.VariablesExist["acme/api/"+forge.VarTraceSigningKeyID] = true

	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.removeTraceVerificationKeys = []string{hexTraceKeyID(testTracePublicKey(2))}
	var err error
	captureStdout(t, func() { err = runReposInstall(context.Background(), opts) })
	require.NoError(t, err)

	assert.Equal(t, manifest, mustReadFile(t, manifestPath), "repos.yaml is not written")
	overlay := string(fc.FileContents[overlayPath("acme/api")])
	assert.NotContains(t, overlay, retained, "the managed overlay drops the key")
	assert.Equal(t, active, traceValue(traceActiveKeyRe, overlay), "the managed overlay keeps the active key")
	assert.Zero(t, len(writtenTraceSeed(fc, "acme/api")), "a removal writes no new seed")
}

// A fresh install from a read-only repos.yaml installs the repository
// without trace keys: it writes neither the seed nor the ledger, leaves
// repos.yaml byte-identical and says how to provision the keys later. A
// fully uninstalled GitHub repository enrolled again is such a fresh
// install: its committed .fullsend/config.yaml keeps the ended
// installation's trace_context, which install names once and its commit
// removes. Asking for a rotation still fails before any write.
func TestRunReposInstall_ReadOnlyManifestFreshInstallSkipsTraceKeys(t *testing.T) {
	ended := regexp.MustCompile(regexp.QuoteMeta("[acme/api]") + `.*ended installation ` + testTraceInstallationID +
		`: install's commit removes it.*stops trusting key ID ` + hexTraceKeyID(testTracePublicKey(1)))
	for _, reenroll := range []bool{false, true} {
		for _, rotate := range []bool{false, true} {
			t.Run(fmt.Sprintf("reenroll=%t/rotate=%t", reenroll, rotate), func(t *testing.T) {
				// The entry declares its tracking, so install needs no other
				// repos.yaml edit.
				manifestPath := writeTestManifest(t, behaviourTestManifestYAML)
				require.NoError(t, os.Chmod(manifestPath, 0o444))
				fc := newInstallFakeClient("acme/api")
				if reenroll {
					// GitHub uninstall leaves the committed config.
					fc.FileContents[overlayPath("acme/api")] = []byte(managedConfigMarker +
						traceContextYAML("", testTraceInstallationID, forge.SecretTraceSigningKey, testTraceVerificationKey(1)))
				}
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
				assert.NotEmpty(t, fc.CommittedFiles, "the repository is installed")
				assert.Zero(t, len(writtenTraceSeed(fc, "acme/api")), "no %s write", forge.SecretTraceSigningKey)
				assert.NotContains(t, fc.VariableValues, "acme/api/"+forge.VarTraceSigningKeyID, "no ledger")
				assert.NotContains(t, string(fc.FileContents[overlayPath("acme/api")]), "trace_context")
				assert.Regexp(t, `acme/api.*not provisioned.*read-only.*repos install acme/api --rotate-trace-signing-key`, out, "the install says how to provision the keys")
				want := 0
				if reenroll {
					want = 1
				}
				assert.Len(t, ended.FindAllString(out, -1), want, "install names the ended installation once:\n%s", out)
			})
		}
	}
}

// Every --remove-trace-verification-key value is checked before the install
// reads anything from the forge, and the error names the value's position.
func TestRunReposInstall_InvalidRemoveTraceVerificationKeyReadsNothing(t *testing.T) {
	manifestPath, _, fc := healthyTraceInstall(t)
	counter := newInstallReadCounter(fc)
	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.testClient = counter
	opts.removeTraceVerificationKeys = []string{hexTraceKeyID(testTracePublicKey(2)), "not-a-key-id"}
	err := runReposInstall(context.Background(), opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--remove-trace-verification-key value 2")
	assert.Zero(t, counter.total(), "no forge read: %v", counter.calls)
}

// sharedManifestClient runs another install right after this run's preflight
// has read FULLSEND_TRACE_SIGNING_KEY_ID, as when two installs that use the
// same repos.yaml file overlap and the other one writes the file and its key
// first.
type sharedManifestClient struct {
	*forge.FakeClient
	other func()
}

func (c *sharedManifestClient) GetRepoVariable(ctx context.Context, owner, repo, name string) (string, bool, error) {
	v, ok, err := c.FakeClient.GetRepoVariable(ctx, owner, repo, name)
	if f := c.other; f != nil && name == forge.VarTraceSigningKeyID {
		c.other = nil
		f()
	}
	return v, ok, err
}

// An install whose repos.yaml another install rewrote after this run loaded
// it does not overwrite the file. The file keeps the other install's keys,
// and a plain retry from it finds a healthy install.
func TestRunReposInstall_ManifestChangedMeanwhileIsNotOverwritten(t *testing.T) {
	manifestPath := writeTestManifest(t, githubUnmanagedManifestYAML)
	fc := newInstallFakeClient("acme/api")
	var otherFile string
	var otherErr error
	opts := githubManagedInstallOpts(manifestPath, fc)
	opts.testClient = &sharedManifestClient{FakeClient: fc, other: func() {
		otherErr = runReposInstall(context.Background(), githubManagedInstallOpts(manifestPath, fc))
		otherFile = mustReadFile(t, manifestPath)
	}}

	err := runReposInstall(context.Background(), opts)
	require.NoError(t, otherErr)
	require.NotEmpty(t, otherFile, "the other install ran during this run's preflight")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "changed after this run loaded it")
	assert.Equal(t, otherFile, mustReadFile(t, manifestPath), "the other install's repos.yaml is kept")
	seeds := func() int {
		n := 0
		for _, s := range fc.CreatedSecrets {
			if s.Name == forge.SecretTraceSigningKey {
				n++
			}
		}
		return n
	}
	assert.Equal(t, 1, seeds(), "only the other install wrote a key")
	require.NoError(t, runReposInstall(context.Background(), githubManagedInstallOpts(manifestPath, fc)), "a plain retry from the file converges")
	assert.Equal(t, 1, seeds(), "the retry finds a healthy install and writes no key")
}

// A manifest that can be read only once, as with -f <(cmd), is read once
// and is read-only, like an HTTPS URL. The entry declares its tracking, so
// install needs no other repos.yaml edit, and a fresh install from it goes
// ahead without keys, in a dry run and a live run alike.
func TestRunReposInstall_ManifestFromPipeIsReadOnly(t *testing.T) {
	manifest := strings.Replace(githubUnmanagedManifestYAML, "    - name: acme/api\n",
		"    - name: acme/api\n      config:\n        tracker:\n          github:\n            allowed_targets:\n              repos:\n                - acme/other\n", 1)
	for _, dryRun := range []bool{true, false} {
		t.Run(fmt.Sprintf("dry run %t", dryRun), func(t *testing.T) {
			r, w, err := os.Pipe()
			require.NoError(t, err)
			t.Cleanup(func() { _ = r.Close() })
			_, err = w.WriteString(manifest)
			require.NoError(t, err)
			require.NoError(t, w.Close())
			fc := newInstallFakeClient("acme/api")
			opts := githubManagedInstallOpts(fmt.Sprintf("/dev/fd/%d", r.Fd()), fc)
			opts.dryRun = dryRun
			var runErr error
			out := captureStdout(t, func() { runErr = runReposInstall(context.Background(), opts) })
			require.NoError(t, runErr)
			assert.Contains(t, out, "Trace signing keys not provisioned: repos.yaml is read-only")
			for _, s := range fc.CreatedSecrets {
				assert.NotEqual(t, forge.SecretTraceSigningKey, s.Name, "no key is written")
			}
		})
	}
}
