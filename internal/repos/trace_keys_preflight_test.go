package repos

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/telemetry/tracecontext"
)

// Trace installation IDs: 32 lowercase hex characters, not all zero.
const (
	traceInstallA = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"
	traceInstallB = "a1b2c3d4e5f60718293a4b5c6d7e8f90"
)

// traceKey is a deterministic Ed25519 pair built with the standard library,
// so fixtures and expected values never come from the code under test.
type traceKey struct {
	seed []byte
	pub  ed25519.PublicKey
}

func newTraceKey(b byte) traceKey {
	seed := bytes.Repeat([]byte{b}, ed25519.SeedSize)
	return traceKey{seed: seed, pub: ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)}
}

func (k traceKey) seedB64() string { return base64.StdEncoding.EncodeToString(k.seed) }
func (k traceKey) pubB64() string  { return base64.StdEncoding.EncodeToString(k.pub) }
func (k traceKey) id() string      { return traceKeyID(k.pub) }

// traceKeyID is pub's key ID as 16 lowercase hex characters.
func traceKeyID(pub ed25519.PublicKey) string {
	id := tracecontext.KeyID(pub)
	return hex.EncodeToString(id[:])
}

// traceRand is deterministic entropy for key and installation-ID generation.
func traceRand(seed int64) io.Reader {
	return rand.New(rand.NewSource(seed)) //nolint:gosec // deterministic test entropy
}

// traceBlock renders a complete trace_context block.
func traceBlock(installationID string, active traceKey, retained ...traceKey) string {
	var b strings.Builder
	fmt.Fprintf(&b, "trace_context:\n  installation_id: %q\n  signing_key:\n    secret: %s\n  verification_keys:\n    active: %q\n",
		installationID, config.TraceSigningKeySecret, active.pubB64())
	if len(retained) > 0 {
		b.WriteString("    retained:\n")
		for _, k := range retained {
			fmt.Fprintf(&b, "      - %q\n", k.pubB64())
		}
	}
	return b.String()
}

// traceFixture returns repos on forgeName, fresh or fully installed, with the
// convergence config that installs them.
func traceFixture(t *testing.T, forgeName string, installed bool, repos ...string) (*forge.FakeClient, ConvergeConfig) {
	t.Helper()
	fc := newFakeClientForBatch(repos...)
	var cfg ConvergeConfig
	if forgeName == ForgeGitLab {
		cfg = gitlabConvergeCfg(repos[0])
		cfg.Manifest.GitLab.Repos = nil
		for _, r := range repos {
			cfg.Manifest.GitLab.Repos = append(cfg.Manifest.GitLab.Repos, RepoEntry{Name: r})
		}
	} else {
		cfg = convergeCfgWithDefaults(newConvergeManifest(repos...))
		if installed {
			cfg = withoutInferenceInputs(cfg)
		}
	}
	if installed {
		for _, r := range repos {
			owner, name, _ := strings.Cut(r, "/")
			if forgeName == ForgeGitLab {
				populateGitLabInstalled(fc, owner, name)
			} else {
				markFullyInstalled(fc, owner, name)
				populateScaffoldContent(t, fc, owner, name, "v1.0.0", "https://mint.example.com")
			}
		}
	}
	return fc, cfg
}

func setManifestTrace(t *testing.T, m *Manifest, name, block string) {
	t.Helper()
	entry := findExplicitEntry(m, name)
	require.NotNil(t, entry, "no manifest entry for %s", name)
	entry.Config = mustManagedConfig(t, block)
}

func setInstalledTrace(fc *forge.FakeClient, name, block string) {
	fc.FileContents[name+"/"+preset.OverlayPath] = []byte(managedConfigMarker + block)
}

func setTraceSecret(fc *forge.FakeClient, name string) {
	fc.Secrets[name+"/"+forge.SecretTraceSigningKey] = true
}

func setTraceLedger(fc *forge.FakeClient, name, keyID string) {
	fc.VariableValues[name+"/"+forge.VarTraceSigningKeyID] = keyID
	fc.VariablesExist[name+"/"+forge.VarTraceSigningKeyID] = true
}

// healthyTrace makes name a healthy install of block: the manifest and the
// committed overlay carry it, the secret exists and the ledger names active.
func healthyTrace(t *testing.T, m *Manifest, fc *forge.FakeClient, name, block string, active traceKey) {
	t.Helper()
	setManifestTrace(t, m, name, block)
	setInstalledTrace(fc, name, block)
	setTraceSecret(fc, name)
	setTraceLedger(fc, name, active.id())
}

func ensureTrace(t *testing.T, fc *forge.FakeClient, cfg TraceKeysConfig) (map[string]*TraceKeyPlan, error) {
	t.Helper()
	if cfg.Rand == nil {
		cfg.Rand = traceRand(1)
	}
	return EnsureTraceSigningKeys(context.Background(), cfg, newTestClientFactory(fc), noopProgress)
}

// manifestTrace returns the trace_context of name's explicit manifest entry.
func manifestTrace(t *testing.T, m *Manifest, name string) *config.TraceContextConfig {
	t.Helper()
	entry := findExplicitEntry(m, name)
	require.NotNil(t, entry, "%s has no explicit manifest entry", name)
	require.True(t, entry.Config.IsSet(), "%s has no manifest config", name)
	tc := entry.Config.Writer().TraceContext()
	require.NotNil(t, tc, "%s has no trace_context in the manifest", name)
	require.NotNil(t, tc.VerificationKeys, "%s has no verification keys", name)
	return tc
}

func manifestSnapshot(t *testing.T, m *Manifest) string {
	t.Helper()
	out, err := m.Marshal()
	require.NoError(t, err)
	return string(out)
}

// requireTracePlan returns name's plan and checks that it names the
// repository and holds a key.
func requireTracePlan(t *testing.T, plans map[string]*TraceKeyPlan, name, forgeName string) *TraceKeyPlan {
	t.Helper()
	require.Contains(t, plans, name, "no trace key plan for %s", name)
	p := plans[name]
	require.NotNil(t, p)
	owner, repo, _ := strings.Cut(name, "/")
	assert.Equal(t, owner, p.Owner)
	assert.Equal(t, repo, p.Repo)
	assert.Equal(t, forgeName, p.Forge)
	require.False(t, p.Key.IsZero(), "the plan holds no key")
	return p
}

// assertPlanKeyIsActive checks that the plan key's public key is tc's active
// key and that the plan's seed expands to it.
func assertPlanKeyIsActive(t *testing.T, p *TraceKeyPlan, tc *config.TraceContextConfig) {
	t.Helper()
	pub := p.Key.Public()
	assert.Equal(t, base64.StdEncoding.EncodeToString(pub), tc.VerificationKeys.Active)
	seed, err := base64.StdEncoding.DecodeString(p.Key.EncodeSeed())
	require.NoError(t, err)
	require.Equal(t, ed25519.SeedSize, len(seed), "the plan holds a 32-byte seed")
	assert.Equal(t, ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey), pub, "the plan's seed expands to its public key")
}

// assertTraceBlockComplete checks the generated parts of a block.
func assertTraceBlockComplete(t *testing.T, tc *config.TraceContextConfig) {
	t.Helper()
	assert.Regexp(t, `^[0-9a-f]{32}$`, tc.InstallationID)
	assert.NotEqual(t, strings.Repeat("0", 32), tc.InstallationID)
	require.NotNil(t, tc.SigningKey)
	assert.Equal(t, config.TraceSigningKeySecret, tc.SigningKey.Secret)
	assert.NoError(t, config.ValidateTraceContext(tc))
}

// assertNoTraceValues checks that s holds no installation ID, public key or
// seed.
func assertNoTraceValues(t *testing.T, s string, keys ...traceKey) {
	t.Helper()
	for _, v := range []string{traceInstallA, traceInstallB} {
		assert.NotContains(t, s, v)
	}
	for _, k := range keys {
		assert.NotContains(t, s, k.pubB64())
		assert.False(t, strings.Contains(s, k.seedB64()), "the seed of key %s is not shown", k.id())
	}
}

// progressLog collects progress lines; convergence reports concurrently.
type progressLog struct {
	mu    sync.Mutex
	lines []string
}

func (p *progressLog) fn(repo, phase, msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lines = append(p.lines, repo+" "+phase+" "+msg)
}

func (p *progressLog) all() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.lines)
}

func TestTraceSigningKeySecretNamesAgree(t *testing.T) {
	assert.Equal(t, forge.SecretTraceSigningKey, config.TraceSigningKeySecret)
}

func TestEnsureTraceSigningKeys_FreshInstallProvisions(t *testing.T) {
	for _, forgeName := range layerForges {
		t.Run(forgeName, func(t *testing.T) {
			fc, cfg := traceFixture(t, forgeName, false, "acme/api")
			// Config the entry already holds, such as what
			// EnsureManagedConfigDefaults recorded, survives the edit.
			findExplicitEntry(cfg.Manifest, "acme/api").Config = mustManagedConfig(t, "roles: [triage]\n")
			progress := &progressLog{}
			plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{
				Manifest: cfg.Manifest, Rand: traceRand(1),
			}, newTestClientFactory(fc), progress.fn)
			require.NoError(t, err)
			p := requireTracePlan(t, plans, "acme/api", forgeName)
			assert.Equal(t, "fresh install", p.Reason)

			tc := manifestTrace(t, cfg.Manifest, "acme/api")
			assertTraceBlockComplete(t, tc)
			assertPlanKeyIsActive(t, p, tc)
			assert.Empty(t, tc.VerificationKeys.Retained)
			assertExplicitRoles(t, findExplicitEntry(cfg.Manifest, "acme/api"), "triage")
			assertNoForgeWrites(t, fc)

			// Progress shows the installation ID and the key ID,
			// never the seed.
			lines := strings.Join(progress.all(), "\n")
			assert.True(t, strings.Contains(lines, tc.InstallationID), "progress shows the installation ID")
			assert.True(t, strings.Contains(lines, traceKeyID(p.Key.Public())), "progress shows the key ID")
			seed := p.Key.EncodeSeed()
			for _, s := range append(progress.all(), manifestSnapshot(t, cfg.Manifest)) {
				assert.False(t, strings.Contains(s, seed), "the seed never leaves the plan")
			}
		})
	}
}

// assertExplicitRoles checks that entry still declares exactly roles.
func assertExplicitRoles(t *testing.T, entry *RepoEntry, roles ...string) {
	t.Helper()
	require.NotNil(t, entry)
	require.True(t, entry.Config.IsSet(), "%s has no config", entry.Name)
	got, ok := entry.Config.ExplicitRoles()
	assert.True(t, ok, "%s keeps its roles", entry.Name)
	assert.Equal(t, roles, got, "%s keeps its roles", entry.Name)
}

func TestEnsureTraceSigningKeys_GlobCoveredGetsExplicitEntry(t *testing.T) {
	m := &Manifest{Version: 1, Defaults: testInferenceDefaults(), GitHub: &PlatformConfig{
		MintURL: "https://mint.example.com",
		Repos:   []RepoEntry{{Name: "acme/*", FullsendRef: "v1.2.3", Config: mustManagedConfig(t, "roles: [triage]\n")}},
	}}
	fc := newFakeClientForBatch("acme/api", "acme/web")

	plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: m, RepoFilter: []string{"acme/api"}})
	require.NoError(t, err)
	p := requireTracePlan(t, plans, "acme/api", ForgeGitHub)
	assert.NotContains(t, plans, "acme/web")

	assert.Len(t, m.GitHub.Repos, 2, "one entry is carved out")
	assert.Nil(t, findExplicitEntry(m, "acme/web"), "a sibling gains no entry")
	glob := findExplicitEntry(m, "acme/*")
	assertExplicitRoles(t, glob, "triage")
	assert.Nil(t, glob.Config.Writer().TraceContext(), "the glob entry carries no trace_context")

	carved := findExplicitEntry(m, "acme/api")
	require.NotNil(t, carved, "acme/api gets its own entry")
	assert.Equal(t, "v1.2.3", carved.FullsendRef, "the carved entry keeps the glob's settings")
	assertExplicitRoles(t, carved, "triage")
	assertPlanKeyIsActive(t, p, manifestTrace(t, m, "acme/api"))
}

// Repositories install skips, such as those whose overlay needs adoption
// (#8511), gain no key and no manifest edit; names match case-insensitively.
func TestEnsureTraceSigningKeys_ExcludedRepoUntouched(t *testing.T) {
	for _, forgeName := range layerForges {
		t.Run(forgeName, func(t *testing.T) {
			fc, cfg := traceFixture(t, forgeName, false, "acme/api", "acme/web")
			plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, ExcludeRepos: []string{"ACME/web"}})
			require.NoError(t, err)
			requireTracePlan(t, plans, "acme/api", forgeName)
			assert.NotContains(t, plans, "acme/web")
			assert.False(t, findExplicitEntry(cfg.Manifest, "acme/web").Config.IsSet(), "the excluded entry gains no config")
		})
	}
}

// Guard: a healthy install (secret present, ledger naming the active key)
// plans nothing, so a read-only manifest is not an error either.
func TestEnsureTraceSigningKeys_HealthyPlansNothing(t *testing.T) {
	a, r := newTraceKey(1), newTraceKey(2)
	for _, forgeName := range layerForges {
		t.Run(forgeName, func(t *testing.T) {
			fc, cfg := traceFixture(t, forgeName, true, "acme/api")
			healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, a, r), a)
			before := manifestSnapshot(t, cfg.Manifest)
			for _, path := range []string{"", "https://example.com/repos.yaml"} {
				plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, ManifestPath: path})
				require.NoError(t, err)
				assert.Empty(t, plans)
				assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest))
			}
			assertNoForgeWrites(t, fc)
		})
	}
}

// Every unhealthy state, and a rotation, gets a new active key; the previous
// active key is retained, no key is dropped and the installation ID is kept.
func TestEnsureTraceSigningKeys_RegeneratesKeepingInstallation(t *testing.T) {
	a, r := newTraceKey(1), newTraceKey(2)
	cases := []struct {
		name      string
		installed bool
		rotate    bool
		// reason is the plan's Reason; empty when the state allows either.
		reason string
		setup  func(t *testing.T, m *Manifest, fc *forge.FakeClient)
	}{
		{name: "ledger missing", installed: true, reason: "recovery", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			setManifestTrace(t, m, "acme/api", traceBlock(traceInstallA, a, r))
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a, r))
			setTraceSecret(fc, "acme/api")
		}},
		{name: "ledger and secret missing", installed: true, reason: "recovery", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			setManifestTrace(t, m, "acme/api", traceBlock(traceInstallA, a, r))
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a, r))
		}},
		{name: "secret missing", installed: true, reason: "recovery", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			setManifestTrace(t, m, "acme/api", traceBlock(traceInstallA, a, r))
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a, r))
			setTraceLedger(fc, "acme/api", a.id())
		}},
		{name: "ledger names a retained key", installed: true, reason: "recovery", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			// A run recorded a as active, then failed before writing its
			// seed: the secret and the committed config still hold r.
			setManifestTrace(t, m, "acme/api", traceBlock(traceInstallA, a, r))
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, r))
			setTraceSecret(fc, "acme/api")
			setTraceLedger(fc, "acme/api", r.id())
		}},
		{name: "uninstall-only, then install", reason: "recovery", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			// Uninstall removed the secret, the ledger and config.yaml; the
			// manifest entry kept its keys (the recovery path).
			setManifestTrace(t, m, "acme/api", traceBlock(traceInstallA, a, r))
		}},
		{name: "rotate a healthy install", installed: true, rotate: true, reason: "rotation", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, a, r), a)
		}},
	}
	for _, forgeName := range layerForges {
		for _, tc := range cases {
			t.Run(forgeName+"/"+tc.name, func(t *testing.T) {
				fc, cfg := traceFixture(t, forgeName, tc.installed, "acme/api")
				tc.setup(t, cfg.Manifest, fc)
				if forgeName == ForgeGitLab && tc.installed {
					// The fullsend jobs on the default branch unset the seed.
					commitGitLabJobTemplates(t, fc, "acme/api", nil)
				}

				plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rotate: tc.rotate})
				require.NoError(t, err)
				p := requireTracePlan(t, plans, "acme/api", forgeName)
				if tc.reason != "" {
					assert.Equal(t, tc.reason, p.Reason)
				}

				got := manifestTrace(t, cfg.Manifest, "acme/api")
				assert.Equal(t, traceInstallA, got.InstallationID, "the installation ID is kept")
				assertPlanKeyIsActive(t, p, got)
				assert.NotEqual(t, a.pubB64(), got.VerificationKeys.Active)
				assert.ElementsMatch(t, []string{r.pubB64(), a.pubB64()}, got.VerificationKeys.Retained,
					"the previous active key is retained and no key is dropped")
				keys, err := got.KeySet()
				require.NoError(t, err)
				for _, k := range []traceKey{a, r} {
					assert.Equal(t, k.pub, keys[tracecontext.KeyID(k.pub)], "a key from before still verifies")
				}
				assertNoForgeWrites(t, fc)
			})
		}
	}
}

// Guard: an existing install without trace_context gains nothing
// unless the operator rotates.
func TestEnsureTraceSigningKeys_ExistingInstallUnchangedWithoutRotate(t *testing.T) {
	for _, forgeName := range layerForges {
		t.Run(forgeName, func(t *testing.T) {
			fc, cfg := traceFixture(t, forgeName, true, "acme/api")
			before := manifestSnapshot(t, cfg.Manifest)
			plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest})
			require.NoError(t, err)
			assert.Empty(t, plans)
			assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest))
			assert.False(t, findExplicitEntry(cfg.Manifest, "acme/api").Config.IsSet(), "the manifest entry gains no config")
			assertNoForgeWrites(t, fc)
		})
	}
}

// The first rotation of an existing install provisions its keys. On GitLab
// the committed fullsend jobs already unset FULLSEND_TRACE_SIGNING_KEY.
func TestEnsureTraceSigningKeys_RotateProvisionsExistingInstall(t *testing.T) {
	for _, forgeName := range layerForges {
		t.Run(forgeName, func(t *testing.T) {
			fc, cfg := traceFixture(t, forgeName, true, "acme/api")
			if forgeName == ForgeGitLab {
				commitGitLabJobTemplates(t, fc, "acme/api", nil)
			}
			plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rotate: true})
			require.NoError(t, err)
			p := requireTracePlan(t, plans, "acme/api", forgeName)
			assert.Equal(t, "rotation", p.Reason)
			tc := manifestTrace(t, cfg.Manifest, "acme/api")
			assertTraceBlockComplete(t, tc)
			assertPlanKeyIsActive(t, p, tc)
			assert.Empty(t, tc.VerificationKeys.Retained)
			assertNoForgeWrites(t, fc)
		})
	}
}

// gitlabTraceJobTemplates are the committed templates whose fullsend job must
// unset FULLSEND_TRACE_SIGNING_KEY on its first before_script line before a
// GitLab repository with a fullsend workflow on its default branch gets a key.
var gitlabTraceJobTemplates = []string{fullsendPollTemplatePath, fullsendDispatcherTemplatePath, fullsendAgentTemplatePath}

// traceUnsetLine is the shipped jobs' before_script entry that clears the
// seed, as the templates indent it.
const traceUnsetLine = "    - unset " + forge.SecretTraceSigningKey + "\n"

// commitGitLabJobTemplates puts the shipped poll, dispatcher and agent
// templates on name's default branch, each passed through edit when it is not
// nil. A template that edit returns as nil is absent.
func commitGitLabJobTemplates(t *testing.T, fc *forge.FakeClient, name string, edit func(t *testing.T, path string, content []byte) []byte) {
	t.Helper()
	for _, path := range gitlabTraceJobTemplates {
		content, err := scaffold.GitLabPerRepoFile(path)
		require.NoError(t, err)
		require.Contains(t, string(content), traceUnsetLine, "the shipped %s unsets the seed", path)
		if edit != nil {
			content = edit(t, path, content)
		}
		if content == nil {
			delete(fc.FileContents, name+"/"+path)
			continue
		}
		fc.FileContents[name+"/"+path] = content
	}
}

// withoutTraceUnset removes the unset line from a shipped template, as in
// a scaffold that predates it.
func withoutTraceUnset(_ *testing.T, _ string, content []byte) []byte {
	return bytes.Replace(content, []byte(traceUnsetLine), nil, 1)
}

// onlyTemplate applies edit to the template at path and keeps the others.
func onlyTemplate(path string, edit func(t *testing.T, path string, content []byte) []byte) func(*testing.T, string, []byte) []byte {
	return func(t *testing.T, p string, content []byte) []byte {
		if p != path {
			return content
		}
		return edit(t, p, content)
	}
}

// GitLab passes the protected seed to every job on a protected branch or tag,
// and install commits the scaffold change whose fullsend jobs unset it only
// after the seed write, by default through a merge request. So while a
// fullsend workflow is on the default branch, a key for any reason waits
// until each poll, dispatcher and agent job there unsets
// FULLSEND_TRACE_SIGNING_KEY on its first before_script line, as a plain list
// entry: the first rotation of an existing install without trace_context, a
// recovery, the rotation of a keyed install and the replacement of an
// exposed seed. Until then the repository gets no plan and its entry records
// no new key, from a read-only manifest too, and progress prints one notice
// that says why and to run install again for the repository once the change
// is on the default branch, naming --rotate-trace-signing-key only for a
// rotation and asking for a writable manifest when the manifest is
// read-only. Committed jobs that unset it get the key.
func TestEnsureTraceSigningKeys_GitLabKeyWritesWaitForUnsetJobs(t *testing.T) {
	a, r := newTraceKey(1), newTraceKey(2)
	healthy := func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
		healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, a, r), a)
	}
	reasons := []struct {
		name   string
		rotate bool
		// reason is the plan's reason when the committed jobs unset the key;
		// head starts the notice otherwise.
		reason, head string
		setup        func(t *testing.T, m *Manifest, fc *forge.FakeClient)
	}{
		{name: "first rotation", rotate: true, reason: "rotation", head: "Trace signing keys not provisioned"},
		{name: "recovery", reason: "recovery", head: "Trace signing key not written (recovery)", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthy(t, m, fc)
			delete(fc.Secrets, "acme/api/"+forge.SecretTraceSigningKey)
		}},
		{name: "rotation of a keyed install", rotate: true, reason: "rotation", head: "Trace signing key not written (rotation)", setup: healthy},
		{name: "exposed seed", reason: "exposed", head: "Trace signing key not written (exposed: unmasked variable)", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthy(t, m, fc)
			fc.SecretProtections["acme/api/"+forge.SecretTraceSigningKey] = forge.SecretProtection{Exists: true, Protected: true}
		}},
	}
	cases := []struct {
		name string
		edit func(t *testing.T, path string, content []byte) []byte
	}{
		{name: "no job unsets the seed", edit: withoutTraceUnset},
		{name: "the poll job does not unset it", edit: onlyTemplate(fullsendPollTemplatePath, withoutTraceUnset)},
		{name: "the dispatcher job does not unset it", edit: onlyTemplate(fullsendDispatcherTemplatePath, withoutTraceUnset)},
		{name: "the agent job does not unset it", edit: onlyTemplate(fullsendAgentTemplatePath, withoutTraceUnset)},
		{name: "a command runs before the unset", edit: onlyTemplate(fullsendAgentTemplatePath, func(_ *testing.T, _ string, c []byte) []byte {
			return bytes.Replace(c, []byte(traceUnsetLine), []byte("    - env\n"+traceUnsetLine), 1)
		})},
		// GitLab runs the before_script that a !reference names, not the
		// list's entries.
		{name: "the before_script is a reference", edit: onlyTemplate(fullsendAgentTemplatePath, func(t *testing.T, _ string, c []byte) []byte {
			key := []byte("  before_script:\n")
			require.Contains(t, string(c), string(key))
			return bytes.Replace(c, key, []byte("  before_script: !reference\n"), 1)
		})},
		{name: "the agent template is missing", edit: onlyTemplate(fullsendAgentTemplatePath, func(*testing.T, string, []byte) []byte { return nil })},
		{name: "the poll template cannot be decoded", edit: onlyTemplate(fullsendPollTemplatePath, func(*testing.T, string, []byte) []byte {
			return []byte("spec: [\n")
		})},
		{name: "the dispatcher job is missing", edit: onlyTemplate(fullsendDispatcherTemplatePath, func(t *testing.T, _ string, c []byte) []byte {
			key := []byte(`"` + gitlabDispatcherJobName + `":`)
			require.Contains(t, string(c), string(key))
			return bytes.Replace(c, key, []byte(`"another job":`), 1)
		})},
	}
	runs := []struct {
		name string
		cfg  TraceKeysConfig
	}{
		{name: "install"},
		{name: "read-only manifest", cfg: TraceKeysConfig{ManifestPath: "https://example.com/repos.yaml"}},
	}
	for _, rs := range reasons {
		fixture := func(t *testing.T, edit func(t *testing.T, path string, content []byte) []byte) (*forge.FakeClient, ConvergeConfig) {
			fc, cfg := traceFixture(t, ForgeGitLab, true, "acme/api")
			if rs.setup != nil {
				rs.setup(t, cfg.Manifest, fc)
			}
			commitGitLabJobTemplates(t, fc, "acme/api", edit)
			return fc, cfg
		}
		t.Run(rs.name+"/the committed jobs unset it", func(t *testing.T) {
			fc, cfg := fixture(t, nil)
			plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rotate: rs.rotate})
			require.NoError(t, err)
			assert.Equal(t, rs.reason, requireTracePlan(t, plans, "acme/api", ForgeGitLab).Reason)
		})
		// The run the notice asks for: only a rotation needs the flag again.
		rerun := "run repos install acme/api again"
		if rs.rotate {
			rerun = "run repos install acme/api --rotate-trace-signing-key again"
		}
		for _, tc := range cases {
			for _, run := range runs {
				t.Run(rs.name+"/"+tc.name+"/"+run.name, func(t *testing.T) {
					fc, cfg := fixture(t, tc.edit)
					before := manifestSnapshot(t, cfg.Manifest)
					progress := &progressLog{}

					run.cfg.Manifest, run.cfg.Rotate, run.cfg.Rand = cfg.Manifest, rs.rotate, traceRand(1)
					plans, err := EnsureTraceSigningKeys(context.Background(), run.cfg, newTestClientFactory(fc), progress.fn)
					require.NoError(t, err)
					assert.Empty(t, plans)
					assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest), "the entry records no new key")
					assertNoForgeWrites(t, fc)
					lines := progress.all()
					require.Len(t, lines, 1, "one notice: %v", lines)
					for _, want := range []string{"acme/api manifest " + rs.head + ": the committed fullsend GitLab jobs do not unset " + forge.SecretTraceSigningKey,
						rerun, "default branch"} {
						assert.Contains(t, lines[0], want)
					}
					if !rs.rotate {
						assert.NotContains(t, lines[0], "--rotate-trace-signing-key")
					}
					// Recording the key needs a repos.yaml edit, so the run the
					// notice asks for needs a writable manifest.
					if run.cfg.ManifestPath != "" {
						assert.Contains(t, lines[0], "with a writable manifest")
					} else {
						assert.NotContains(t, lines[0], "writable")
					}
					assertNoTraceValues(t, lines[0], a, r)
				})
			}
		}
	}
}

// A GitLab key write refuses, before any edit and without its notice, when
// the workflow marker or a committed job template cannot be read for a
// reason other than its absence, whatever the reason for the key, and a key
// that waits for the committed jobs prints no notice when another repository
// refuses the run.
func TestEnsureTraceSigningKeys_GitLabKeyWriteRefusalsPrintNoNotice(t *testing.T) {
	a, r, x := newTraceKey(1), newTraceKey(2), newTraceKey(5)
	healthy := func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
		healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, a, r), a)
	}
	reasons := []struct {
		name   string
		rotate bool
		setup  func(t *testing.T, m *Manifest, fc *forge.FakeClient)
	}{
		{name: "first rotation", rotate: true, setup: func(*testing.T, *Manifest, *forge.FakeClient) {}},
		{name: "recovery", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthy(t, m, fc)
			delete(fc.Secrets, "acme/api/"+forge.SecretTraceSigningKey)
		}},
		{name: "rotation of a keyed install", rotate: true, setup: healthy},
		{name: "exposed seed", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthy(t, m, fc)
			fc.SecretProtections["acme/api/"+forge.SecretTraceSigningKey] = forge.SecretProtection{Exists: true, Protected: true}
		}},
	}
	// unreadable makes acme/api's file at path fail to read.
	unreadable := func(path string) func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
		return func(t *testing.T, _ *Manifest, fc *forge.FakeClient) {
			commitGitLabJobTemplates(t, fc, "acme/api", nil)
			fc.GetFileContentErrors = map[string]error{"acme/api/" + path: errors.New("gateway timeout")}
		}
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, m *Manifest, fc *forge.FakeClient)
		want  []string
	}{
		{name: "the workflow marker cannot be read", want: []string{"acme/api", "checking workflow file", "gateway timeout"}, setup: unreadable(fullsendPipelineInclude)},
		{name: "a template cannot be read", want: []string{"acme/api", fullsendAgentTemplatePath, "gateway timeout"}, setup: unreadable(fullsendAgentTemplatePath)},
		{name: "another repository is stale", want: []string{"acme/web", x.id()}, setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			commitGitLabJobTemplates(t, fc, "acme/api", withoutTraceUnset)
			healthyTrace(t, m, fc, "acme/web", traceBlock(traceInstallB, a), a)
			setInstalledTrace(fc, "acme/web", traceBlock(traceInstallB, a, x))
		}},
	}
	for _, rs := range reasons {
		for _, tc := range cases {
			t.Run(rs.name+"/"+tc.name, func(t *testing.T) {
				fc, cfg := traceFixture(t, ForgeGitLab, true, "acme/api", "acme/web")
				rs.setup(t, cfg.Manifest, fc)
				tc.setup(t, cfg.Manifest, fc)
				before := manifestSnapshot(t, cfg.Manifest)
				progress := &progressLog{}

				plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{
					Manifest: cfg.Manifest, Rotate: rs.rotate, Rand: traceRand(1),
				}, newTestClientFactory(fc), progress.fn)
				require.Error(t, err)
				for _, want := range tc.want {
					assert.Contains(t, err.Error(), want)
				}
				assertNoTraceValues(t, err.Error(), a, r, x)
				assert.Empty(t, plans)
				assert.Empty(t, progress.all(), "a refusal reports nothing")
				assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest))
				assertNoForgeWrites(t, fc)
			})
		}
	}
}

// Every GitLab key write reads the committed job templates once while a
// fullsend workflow is on the default branch. GitHub, a healthy GitLab
// repository, and a GitLab repository without a fullsend workflow there (a
// fresh install, a keyed one after an uninstall that kept its keys, one whose
// install change has not merged, or one whose committed block belongs to an
// ended installation) read none. A GitLab key write reads the workflow marker
// once, also when the marker was read to tell an ended installation, and a
// healthy GitLab repository does not read it.
func TestEnsureTraceSigningKeys_GitLabKeyWritesReadCommittedJobs(t *testing.T) {
	a := newTraceKey(1)
	healthy := func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
		healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, a), a)
	}
	recovery := func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
		setManifestTrace(t, m, "acme/api", traceBlock(traceInstallA, a))
		setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a))
		setTraceSecret(fc, "acme/api")
	}
	// Install wrote the key, and its change has not merged.
	unmerged := func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
		setManifestTrace(t, m, "acme/api", traceBlock(traceInstallA, a))
		setTraceSecret(fc, "acme/api")
		setTraceLedger(fc, "acme/api", a.id())
	}
	cases := []struct {
		name      string
		forge     string
		installed bool
		rotate    bool
		// reason is the plan's reason, "" for none; reads is whether the
		// committed job templates are read.
		reason string
		reads  bool
		setup  func(t *testing.T, m *Manifest, fc *forge.FakeClient)
	}{
		{name: "GitHub first rotation", forge: ForgeGitHub, installed: true, rotate: true, reason: "rotation"},
		{name: "GitHub recovery", forge: ForgeGitHub, installed: true, reason: "recovery", setup: recovery},
		{name: "GitLab healthy", forge: ForgeGitLab, installed: true, setup: healthy},
		{name: "GitLab fresh install", forge: ForgeGitLab, reason: "fresh install"},
		{name: "GitLab fresh install with a rotation", forge: ForgeGitLab, rotate: true, reason: "fresh install"},
		// Uninstall-only removed the workflow, seed, ledger and config.
		{name: "GitLab recovery without a fullsend workflow", forge: ForgeGitLab, reason: "recovery", setup: func(t *testing.T, m *Manifest, _ *forge.FakeClient) {
			setManifestTrace(t, m, "acme/api", traceBlock(traceInstallA, a))
		}},
		{name: "GitLab rotation without a fullsend workflow", forge: ForgeGitLab, rotate: true, reason: "rotation", setup: unmerged},
		// Re-enrolled: the committed block is the ended installation's.
		{name: "GitLab recovery replacing an ended installation", forge: ForgeGitLab, reason: "recovery", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			setManifestTrace(t, m, "acme/api", traceBlock(traceInstallA, a))
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallB, newTraceKey(5)))
		}},
		{name: "GitLab first rotation", forge: ForgeGitLab, installed: true, rotate: true, reason: "rotation", reads: true},
		{name: "GitLab recovery", forge: ForgeGitLab, installed: true, reason: "recovery", reads: true, setup: recovery},
		{name: "GitLab rotation of a keyed install", forge: ForgeGitLab, installed: true, rotate: true, reason: "rotation", reads: true, setup: healthy},
		{name: "GitLab exposed seed", forge: ForgeGitLab, installed: true, reason: "exposed", reads: true, setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthy(t, m, fc)
			fc.SecretProtections["acme/api/"+forge.SecretTraceSigningKey] = forge.SecretProtection{Exists: true, Protected: true}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc, cfg := traceFixture(t, tc.forge, tc.installed, "acme/api")
			if tc.setup != nil {
				tc.setup(t, cfg.Manifest, fc)
			}
			if tc.forge == ForgeGitLab && tc.installed {
				commitGitLabJobTemplates(t, fc, "acme/api", nil)
			}
			counter := newReadCountingClient(fc)

			plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{
				Manifest: cfg.Manifest, Rotate: tc.rotate, Rand: traceRand(1),
			}, newTestClientFactory(counter), noopProgress)
			require.NoError(t, err)
			if tc.reason == "" {
				assert.Empty(t, plans)
			} else {
				assert.Equal(t, tc.reason, requireTracePlan(t, plans, "acme/api", tc.forge).Reason)
			}
			reads := 0
			if tc.reads {
				reads = 1
			}
			for _, path := range gitlabTraceJobTemplates {
				assert.Equal(t, reads, counter.count("file:acme/api/"+path), "reads of %s", path)
			}
			if tc.forge == ForgeGitLab {
				markerReads := 1
				if tc.reason == "" {
					markerReads = 0
				}
				assert.Equal(t, markerReads, counter.count("file:acme/api/"+fullsendPipelineInclude), "reads of the workflow marker")
			}
		})
	}
}

// gitlabTracePin is the gitlab.fullsend_ref of the GitLab trace fixtures
// (gitlabConvergeCfg). The tests run as a development build, which has no
// release ref, so the fixtures pin another release.
const gitlabTracePin = "v2.5.0"

// serveGitLabScaffold serves the shipped GitLab scaffold at ref in the
// upstream repository, where install fetches the templates of a pinned
// fullsend_ref, each file passed through edit. A file that edit returns as
// nil is absent at ref.
func serveGitLabScaffold(t *testing.T, fc *forge.FakeClient, ref string, edit func(t *testing.T, path string, content []byte) []byte) {
	t.Helper()
	for _, p := range scaffoldGitLabPaths {
		content, err := scaffold.GitLabPerRepoFile(p.outPath)
		require.NoError(t, err)
		key := shimOwner + "/" + shimRepo + "/" + p.repoPath + "@" + ref
		if content = edit(t, p.outPath, content); content == nil {
			delete(fc.FileContentsRef, key)
			continue
		}
		fc.FileContentsRef[key] = content
	}
}

// Install delivers the fullsend GitLab jobs of a fullsend_ref that pins a
// release other than the running one, and GitLab passes the protected seed
// to every job on a protected branch or tag. So when a job of the pinned
// release does not unset FULLSEND_TRACE_SIGNING_KEY on its first
// before_script line, the repository gets no key and no edit, whatever the
// reason for the key, in a dry run and from a read-only manifest too, and
// progress prints one notice that names the reason, the fullsend_ref and the
// variable and says to move gitlab.fullsend_ref. A fresh install then goes
// ahead without keys, and once install's commit is on the default branch
// only a rotation provisions them, so its notice also says to rotate, with a
// writable manifest when the manifest is read-only. The same pin serving
// jobs that unset it plans the key as before.
func TestEnsureTraceSigningKeys_GitLabPinnedJobsMustUnsetTheKey(t *testing.T) {
	a, r := newTraceKey(1), newTraceKey(2)
	healthy := func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
		healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, a, r), a)
	}
	cases := []struct {
		name      string
		installed bool
		rotate    bool
		// reason is the plan's reason when the pinned jobs unset the key,
		// and what the notice names otherwise, with defect, the exposed
		// seed's defect.
		reason, defect string
		setup          func(t *testing.T, m *Manifest, fc *forge.FakeClient)
	}{
		{name: "fresh install", reason: "fresh install"},
		{name: "recovery", installed: true, reason: "recovery", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthy(t, m, fc)
			delete(fc.Secrets, "acme/api/"+forge.SecretTraceSigningKey)
		}},
		{name: "rotation of a keyed install", installed: true, rotate: true, reason: "rotation", setup: healthy},
		{name: "exposed seed", installed: true, reason: "exposed", defect: "unmasked variable", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthy(t, m, fc)
			fc.SecretProtections["acme/api/"+forge.SecretTraceSigningKey] = forge.SecretProtection{Exists: true, Protected: true}
		}},
		{name: "first rotation of an existing install", installed: true, rotate: true, reason: "rotation"},
	}
	runs := []struct {
		name string
		cfg  TraceKeysConfig
	}{
		{name: "install"},
		{name: "dry run", cfg: TraceKeysConfig{DryRun: true}},
		{name: "read-only manifest", cfg: TraceKeysConfig{ManifestPath: "https://example.com/repos.yaml"}},
	}
	for _, tc := range cases {
		fixture := func(t *testing.T) (*forge.FakeClient, ConvergeConfig) {
			fc, cfg := traceFixture(t, ForgeGitLab, tc.installed, "acme/api")
			if tc.setup != nil {
				tc.setup(t, cfg.Manifest, fc)
			}
			if tc.installed {
				// The jobs on the default branch unset the key already.
				commitGitLabJobTemplates(t, fc, "acme/api", nil)
			}
			return fc, cfg
		}
		t.Run(tc.name+"/the pinned jobs unset it", func(t *testing.T) {
			fc, cfg := fixture(t)
			plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rotate: tc.rotate})
			require.NoError(t, err)
			assert.Equal(t, tc.reason, requireTracePlan(t, plans, "acme/api", ForgeGitLab).Reason)
		})
		for _, run := range runs {
			t.Run(tc.name+"/the pinned jobs do not unset it/"+run.name, func(t *testing.T) {
				fc, cfg := fixture(t)
				serveGitLabScaffold(t, fc, gitlabTracePin, withoutTraceUnset)
				before := manifestSnapshot(t, cfg.Manifest)
				progress := &progressLog{}

				run.cfg.Manifest, run.cfg.Rotate, run.cfg.Rand = cfg.Manifest, tc.rotate, traceRand(1)
				plans, err := EnsureTraceSigningKeys(context.Background(), run.cfg, newTestClientFactory(fc), progress.fn)
				require.NoError(t, err)
				assert.Empty(t, plans)
				assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest), "the entry is not edited")
				assertNoForgeWrites(t, fc)
				lines := progress.all()
				require.Len(t, lines, 1, "one notice: %v", lines)
				for _, want := range []string{"acme/api", tc.reason, tc.defect, gitlabTracePin, forge.SecretTraceSigningKey, "gitlab.fullsend_ref"} {
					assert.Contains(t, lines[0], want)
				}
				fresh := tc.reason == "fresh install"
				if fresh {
					assert.Contains(t, lines[0], "then run repos install acme/api --rotate-trace-signing-key", "the notice says how this fresh install gets its keys")
				} else {
					assert.NotContains(t, lines[0], "--rotate-trace-signing-key")
				}
				if fresh && run.cfg.ManifestPath != "" {
					assert.Contains(t, lines[0], "with a writable manifest")
				} else {
					assert.NotContains(t, lines[0], "writable")
				}
				assertNoTraceValues(t, lines[0], a, r)
			})
		}
	}
}

// A failing pin, or committed jobs that do not unset the key yet, withhold
// the key that would replace an exposed seed, but a removal in the same run
// still applies. The exposed key stays active, so progress does not report it
// as retained; the notice names the defect.
func TestEnsureTraceSigningKeys_GitLabWithheldExposedSeedKeepsRemoval(t *testing.T) {
	a, r := newTraceKey(1), newTraceKey(2)
	cases := []struct {
		name string
		// withhold leaves jobs that would receive the seed without the unset;
		// want is what the notice names.
		withhold func(t *testing.T, fc *forge.FakeClient)
		want     string
	}{
		{name: "the pinned jobs do not unset it", want: gitlabTracePin, withhold: func(t *testing.T, fc *forge.FakeClient) {
			serveGitLabScaffold(t, fc, gitlabTracePin, withoutTraceUnset)
		}},
		{name: "the committed jobs do not unset it", want: "default branch", withhold: func(t *testing.T, fc *forge.FakeClient) {
			commitGitLabJobTemplates(t, fc, "acme/api", withoutTraceUnset)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc, cfg := traceFixture(t, ForgeGitLab, true, "acme/api")
			healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, a, r), a)
			fc.SecretProtections["acme/api/"+forge.SecretTraceSigningKey] = forge.SecretProtection{Exists: true, Protected: true}
			tc.withhold(t, fc)
			progress := &progressLog{}

			plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{
				Manifest: cfg.Manifest, RemoveKeyIDs: []string{r.id()}, Rand: traceRand(1),
			}, newTestClientFactory(fc), progress.fn)
			require.NoError(t, err)
			assert.Empty(t, plans, "no key replaces the exposed seed")
			got := manifestTrace(t, cfg.Manifest, "acme/api")
			assert.Equal(t, a.pubB64(), got.VerificationKeys.Active, "the exposed key stays active")
			assert.Empty(t, got.VerificationKeys.Retained, "the removal is recorded")
			assertNoForgeWrites(t, fc)
			lines := progress.all()
			out := strings.Join(lines, "\n")
			require.Len(t, lines, 2, "the notice and the removal:\n%s", out)
			assert.Contains(t, lines[0], "not written (exposed: unmasked variable)")
			assert.Contains(t, lines[0], tc.want)
			assert.Contains(t, lines[1], "removal of retained trace verification key ID "+r.id())
			assert.NotContains(t, out, "was exposed", "no key replaced the exposed one, so it is not retained")
			assertNoTraceValues(t, out, a, r)
		})
	}
}

// The jobs a pinned release delivers meet the rule the jobs on the default
// branch meet: a job that runs a command before the unset, a template that
// cannot be decoded, a missing job, a release that predates the dispatcher,
// whose install delivers no dispatcher template, a template absent at the
// ref, and a fullsend_ref that serves no templates, such as a mistyped tag,
// all leave a fresh install without a key, and the run goes on.
func TestEnsureTraceSigningKeys_GitLabPinnedJobDefectsGetNoKey(t *testing.T) {
	cases := []struct {
		name string
		edit func(t *testing.T, path string, content []byte) []byte
	}{
		{name: "the agent job does not unset it", edit: onlyTemplate(fullsendAgentTemplatePath, withoutTraceUnset)},
		{name: "a command runs before the unset", edit: onlyTemplate(fullsendPollTemplatePath, func(_ *testing.T, _ string, c []byte) []byte {
			return bytes.Replace(c, []byte(traceUnsetLine), []byte("    - env\n"+traceUnsetLine), 1)
		})},
		{name: "the poll template cannot be decoded", edit: onlyTemplate(fullsendPollTemplatePath, func(*testing.T, string, []byte) []byte {
			return []byte("spec: [\n")
		})},
		{name: "the dispatcher job is missing", edit: onlyTemplate(fullsendDispatcherTemplatePath, func(t *testing.T, _ string, c []byte) []byte {
			key := []byte(`"` + gitlabDispatcherJobName + `":`)
			require.Contains(t, string(c), string(key))
			return bytes.Replace(c, key, []byte(`"another job":`), 1)
		})},
		{name: "the release predates the dispatcher", edit: onlyTemplate(fullsendPipelineInclude, func(t *testing.T, _ string, c []byte) []byte {
			include := []byte("  - local: '" + fullsendDispatcherTemplatePath + "'\n")
			require.Contains(t, string(c), string(include))
			return bytes.Replace(c, include, nil, 1)
		})},
		{name: "a template is absent at the ref", edit: onlyTemplate(fullsendAgentTemplatePath, func(*testing.T, string, []byte) []byte { return nil })},
		{name: "the fullsend_ref serves no templates", edit: func(*testing.T, string, []byte) []byte { return nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc, cfg := traceFixture(t, ForgeGitLab, false, "acme/api")
			serveGitLabScaffold(t, fc, gitlabTracePin, tc.edit)
			before := manifestSnapshot(t, cfg.Manifest)
			progress := &progressLog{}

			plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{
				Manifest: cfg.Manifest, Rand: traceRand(1),
			}, newTestClientFactory(fc), progress.fn)
			require.NoError(t, err)
			assert.Empty(t, plans)
			assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest))
			lines := progress.all()
			require.Len(t, lines, 1, "one notice: %v", lines)
			assert.Contains(t, lines[0], gitlabTracePin)
		})
	}
}

// A GitLab key write checks the jobs the pinned release delivers as well as
// the jobs on the default branch, whatever the reason for the key. While the
// pinned jobs unset FULLSEND_TRACE_SIGNING_KEY, the notice says to run
// install again once install's commit is on the default branch. When they do
// not, that commit cannot help, so only the notice to move
// gitlab.fullsend_ref prints.
func TestEnsureTraceSigningKeys_GitLabPinNoticeWins(t *testing.T) {
	a, r := newTraceKey(1), newTraceKey(2)
	healthy := func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
		healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, a, r), a)
	}
	reasons := []struct {
		name   string
		rotate bool
		setup  func(t *testing.T, m *Manifest, fc *forge.FakeClient)
	}{
		{name: "first rotation of an existing install", rotate: true, setup: func(*testing.T, *Manifest, *forge.FakeClient) {}},
		{name: "recovery", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthy(t, m, fc)
			delete(fc.Secrets, "acme/api/"+forge.SecretTraceSigningKey)
		}},
		{name: "rotation of a keyed install", rotate: true, setup: healthy},
		{name: "exposed seed", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthy(t, m, fc)
			fc.SecretProtections["acme/api/"+forge.SecretTraceSigningKey] = forge.SecretProtection{Exists: true, Protected: true}
		}},
	}
	cases := []struct {
		name      string
		pinUnsets bool
		want      string
		notWant   string
	}{
		{name: "the pinned jobs unset it", pinUnsets: true, want: "default branch", notWant: "gitlab.fullsend_ref"},
		{name: "the pinned jobs do not unset it", want: "gitlab.fullsend_ref", notWant: "default branch"},
	}
	for _, reason := range reasons {
		for _, tc := range cases {
			t.Run(reason.name+"/"+tc.name, func(t *testing.T) {
				fc, cfg := traceFixture(t, ForgeGitLab, true, "acme/api")
				reason.setup(t, cfg.Manifest, fc)
				// The jobs on the default branch do not unset the key yet.
				commitGitLabJobTemplates(t, fc, "acme/api", withoutTraceUnset)
				if !tc.pinUnsets {
					serveGitLabScaffold(t, fc, gitlabTracePin, withoutTraceUnset)
				}
				before := manifestSnapshot(t, cfg.Manifest)
				progress := &progressLog{}

				plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{
					Manifest: cfg.Manifest, Rotate: reason.rotate, Rand: traceRand(1),
				}, newTestClientFactory(fc), progress.fn)
				require.NoError(t, err)
				assert.Empty(t, plans)
				assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest))
				lines := progress.all()
				require.Len(t, lines, 1, "one notice: %v", lines)
				assert.Contains(t, lines[0], tc.want)
				assert.NotContains(t, lines[0], tc.notWant)
			})
		}
	}
}

// A repository whose own repos.yaml entry sets fullsend_ref is pinned by
// that entry, so the notice says to move the entry's fullsend_ref.
func TestEnsureTraceSigningKeys_GitLabEntryPinNoticeNamesTheEntry(t *testing.T) {
	const pin = "v3.0.0"
	fc, cfg := traceFixture(t, ForgeGitLab, false, "acme/api")
	findExplicitEntry(cfg.Manifest, "acme/api").FullsendRef = pin
	serveGitLabScaffold(t, fc, pin, withoutTraceUnset)
	progress := &progressLog{}

	plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{
		Manifest: cfg.Manifest, Rand: traceRand(1),
	}, newTestClientFactory(fc), progress.fn)
	require.NoError(t, err)
	assert.Empty(t, plans)
	lines := progress.all()
	require.Len(t, lines, 1, "one notice: %v", lines)
	assert.Contains(t, lines[0], pin)
	assert.Contains(t, lines[0], "repos.yaml entry")
	assert.NotContains(t, lines[0], "gitlab.fullsend_ref")
}

// When the jobs of a pinned release cannot be read for a reason other than a
// missing template or job, the run is refused before any edit, naming the
// repository and the fullsend_ref, with no notice: the upstream read fails,
// or no upstream client exists, which convergence needs for the pin too. A
// fresh install from a read-only manifest gets no key whatever those jobs
// are, so without a rotation it goes ahead without keys and prints the
// read-only notice once; convergence meets the same read on its own.
func TestEnsureTraceSigningKeys_GitLabPinnedJobsReadErrorRefuses(t *testing.T) {
	cases := []struct {
		name    string
		clients func(t *testing.T, fc *forge.FakeClient) ForgeClientFactory
		want    string
	}{
		{name: "the upstream read fails", want: "gateway timeout", clients: func(_ *testing.T, fc *forge.FakeClient) ForgeClientFactory {
			fc.Errors["GetFileContentAtRef"] = errors.New("gateway timeout")
			return newTestClientFactory(fc)
		}},
		{name: "no upstream client", want: "upstream GitHub client", clients: func(_ *testing.T, fc *forge.FakeClient) ForgeClientFactory {
			return &perForgeClientFactory{
				clients: map[string]forge.Client{ForgeGitLab: fc},
				errs:    map[string]error{ForgeGitHub: errors.New("no GitHub token found")},
			}
		}},
	}
	const url = "https://example.com/repos.yaml"
	runs := []struct {
		name string
		cfg  TraceKeysConfig
		// skipped is a fresh install that goes ahead without keys.
		skipped bool
	}{
		{name: "writable manifest"},
		{name: "read-only manifest", cfg: TraceKeysConfig{ManifestPath: url}, skipped: true},
		{name: "read-only manifest with a rotation", cfg: TraceKeysConfig{ManifestPath: url, Rotate: true}},
	}
	for _, tc := range cases {
		for _, run := range runs {
			t.Run(tc.name+"/"+run.name, func(t *testing.T) {
				fc, cfg := traceFixture(t, ForgeGitLab, false, "acme/api")
				clients := tc.clients(t, fc)
				before := manifestSnapshot(t, cfg.Manifest)
				progress := &progressLog{}

				run.cfg.Manifest, run.cfg.Rand = cfg.Manifest, traceRand(1)
				plans, err := EnsureTraceSigningKeys(context.Background(), run.cfg, clients, progress.fn)
				assert.Empty(t, plans)
				assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest))
				assertNoForgeWrites(t, fc)
				if run.skipped {
					require.NoError(t, err)
					lines := progress.all()
					require.Len(t, lines, 1, "one notice: %v", lines)
					assert.Contains(t, lines[0], "acme/api")
					assert.Contains(t, lines[0], "not provisioned: repos.yaml is read-only")
					assert.Contains(t, lines[0], "a later repos install acme/api --rotate-trace-signing-key", "the notice names the run that provisions them")
					return
				}
				require.Error(t, err)
				for _, want := range []string{"acme/api", gitlabTracePin, tc.want} {
					assert.Contains(t, err.Error(), want)
				}
				assert.Empty(t, progress.all(), "a refusal reports nothing")
			})
		}
	}
}

// A pinned read error stands for every repository on that fullsend_ref: from
// a read-only manifest, a fresh install goes ahead without keys, while
// another repository on the same pin that needs a key still refuses the run,
// and the refusal names only that repository.
func TestEnsureTraceSigningKeys_GitLabPinnedReadErrorRefusesOtherKeyWrites(t *testing.T) {
	a := newTraceKey(1)
	fc, cfg := traceFixture(t, ForgeGitLab, false, "acme/api", "acme/web")
	// acme/web's secret is missing, so it needs a key to recover.
	healthyTrace(t, cfg.Manifest, fc, "acme/web", traceBlock(traceInstallA, a), a)
	delete(fc.Secrets, "acme/web/"+forge.SecretTraceSigningKey)
	fc.Errors["GetFileContentAtRef"] = errors.New("gateway timeout")
	before := manifestSnapshot(t, cfg.Manifest)
	progress := &progressLog{}

	plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{
		Manifest: cfg.Manifest, ManifestPath: "https://example.com/repos.yaml", Rand: traceRand(1),
	}, newTestClientFactory(fc), progress.fn)
	require.Error(t, err)
	for _, want := range []string{"acme/web", gitlabTracePin, "gateway timeout"} {
		assert.Contains(t, err.Error(), want)
	}
	assert.NotContains(t, err.Error(), "acme/api", "the fresh install is not refused")
	assert.Empty(t, plans)
	assert.Empty(t, progress.all(), "a refusal reports nothing")
	assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest))
	assertNoForgeWrites(t, fc)
}

// Only a GitLab repository whose fullsend_ref pins another release reads
// that release's jobs upstream, once per fullsend_ref however many
// repositories share it. GitHub, and GitLab without fullsend_ref or with the
// running release's tag or ref, install jobs that this build ships, so
// nothing is read upstream.
func TestEnsureTraceSigningKeys_GitLabPinnedJobsReadOncePerPin(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	cases := []struct {
		name  string
		forge string
		// ref is gitlab.fullsend_ref.
		ref                      string
		upstreamRef, upstreamTag string
		reads                    bool
	}{
		{name: "GitHub", forge: ForgeGitHub},
		{name: "GitLab without fullsend_ref", forge: ForgeGitLab},
		{name: "GitLab pinned to the running release's tag", forge: ForgeGitLab, ref: gitlabTracePin, upstreamRef: sha, upstreamTag: gitlabTracePin},
		{name: "GitLab pinned to the running release's ref", forge: ForgeGitLab, ref: sha, upstreamRef: sha, upstreamTag: "v9.9.9"},
		{name: "GitLab pinned to another release", forge: ForgeGitLab, ref: gitlabTracePin, upstreamRef: sha, upstreamTag: "v9.9.9", reads: true},
		{name: "GitLab pinned by a development build", forge: ForgeGitLab, ref: gitlabTracePin, reads: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc, cfg := traceFixture(t, tc.forge, false, "acme/api", "acme/web")
			if tc.forge == ForgeGitLab {
				cfg.Manifest.GitLab.FullsendRef = tc.ref
			}
			counter := newReadCountingClient(fc)

			plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{
				Manifest: cfg.Manifest, UpstreamRef: tc.upstreamRef, UpstreamTag: tc.upstreamTag, Rand: traceRand(1),
			}, newTestClientFactory(counter), noopProgress)
			require.NoError(t, err)
			for _, name := range []string{"acme/api", "acme/web"} {
				assert.Equal(t, "fresh install", requireTracePlan(t, plans, name, tc.forge).Reason)
			}
			reads := counter.refReads()
			if !tc.reads {
				assert.Empty(t, reads, "nothing is read upstream")
				return
			}
			for _, path := range gitlabTraceJobTemplates {
				key := "ref:" + shimOwner + "/" + shimRepo + "/internal/scaffold/fullsend-repo-gitlab/" + path + "@" + gitlabTracePin
				assert.Equal(t, 1, reads[key], "%s is read once for both repositories", path)
			}
			for key, n := range reads {
				assert.Equal(t, 1, n, "%s is read once", key)
			}
		})
	}
}

func TestEnsureTraceSigningKeys_RemoveRetainedKey(t *testing.T) {
	a, r1, r2 := newTraceKey(1), newTraceKey(2), newTraceKey(3)
	for _, forgeName := range layerForges {
		t.Run(forgeName, func(t *testing.T) {
			fc, cfg := traceFixture(t, forgeName, true, "acme/api")
			healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, a, r1, r2), a)

			plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, RemoveKeyIDs: []string{r1.id()}})
			require.NoError(t, err)
			// A removal alone writes no key: no plan may reach convergence.
			assert.Empty(t, plans)
			got := manifestTrace(t, cfg.Manifest, "acme/api")
			assert.Equal(t, []string{r2.pubB64()}, got.VerificationKeys.Retained, "only the named key goes")
			assert.Equal(t, a.pubB64(), got.VerificationKeys.Active)
			assert.Equal(t, traceInstallA, got.InstallationID)
			assertNoForgeWrites(t, fc)
		})
	}
}

// A key ID that names the active key or no key, or that is not exactly 16
// lowercase hex characters, is refused before any edit, and the error never
// echoes it: a seed pasted by mistake must not reach logs.
func TestEnsureTraceSigningKeys_RemoveKeyIDRefusals(t *testing.T) {
	a, r := newTraceKey(1), newTraceKey(2)
	require.NotEqual(t, r.id(), strings.ToUpper(r.id()), "the retained key's ID has hex letters")
	cases := []struct{ name, keyID string }{
		{"active key", a.id()},
		{"unknown key", newTraceKey(9).id()},
		{"uppercase hex of a retained key", strings.ToUpper(r.id())},
		{"a seed pasted as a key ID", r.seedB64()},
	}
	for _, forgeName := range layerForges {
		for _, tc := range cases {
			t.Run(forgeName+"/"+tc.name, func(t *testing.T) {
				fc, cfg := traceFixture(t, forgeName, true, "acme/api")
				healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, a, r), a)
				before := manifestSnapshot(t, cfg.Manifest)

				plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, RemoveKeyIDs: []string{tc.keyID}})
				require.Error(t, err)
				assert.False(t, strings.Contains(err.Error(), tc.keyID), "the error never echoes the value")
				assertNoTraceValues(t, err.Error(), a, r)
				assert.Empty(t, plans)
				assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest))
				assertNoForgeWrites(t, fc)
			})
		}
	}
}

// Guard: a retained key the operator already dropped from repos.yaml is not
// a stale manifest when --remove-trace-verification-key names it, and the
// healthy install needs no key and no edit.
func TestEnsureTraceSigningKeys_RemovedKeyAlreadyDroppedFromManifest(t *testing.T) {
	a, r := newTraceKey(1), newTraceKey(2)
	for _, forgeName := range layerForges {
		t.Run(forgeName, func(t *testing.T) {
			fc, cfg := traceFixture(t, forgeName, true, "acme/api")
			healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, a), a)
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a, r))
			before := manifestSnapshot(t, cfg.Manifest)

			plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, RemoveKeyIDs: []string{r.id()}})
			require.NoError(t, err)
			assert.Empty(t, plans)
			assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest))
			assertNoForgeWrites(t, fc)
		})
	}
}

// assertPendingRemovalFirst checks the stale refusal for a committed key that
// the repos.yaml entry lacks. A removal delivered by pull or merge request
// leaves that state until it merges, and install cannot tell it from a key
// added to the committed config by hand, so the refusal names the pending
// removal before an edited config. Its closing advice does not ask to
// restore the entry, which is what the removal run wrote.
func assertPendingRemovalFirst(t *testing.T, msg string) {
	t.Helper()
	pending := strings.Index(msg, removeTraceKeyFlag+" run removed the key and that change has not merged yet")
	require.GreaterOrEqual(t, pending, 0, "the refusal names a removal whose change has not merged yet")
	assert.Contains(t, msg[pending:], "or name the key with "+removeTraceKeyFlag+" again on each run until it merges",
		"the refusal says how to pass until the removal merges")
	assert.Greater(t, strings.Index(msg, "may have been edited"), pending, "the pending removal comes before an edited config")
	assert.NotContains(t, msg, "were not changed: restore", "the closing advice does not undo the pending removal")
}

// assertNoPendingRemoval checks the stale refusal for a committed active key
// that the repos.yaml entry lacks. A removal never drops an active key, so
// the refusal does not name a pending removal, and its closing advice is to
// restore the entry.
func assertNoPendingRemoval(t *testing.T, msg string) {
	t.Helper()
	assert.NotContains(t, msg, "has not merged yet", "no removal leaves an active key committed")
	assert.Contains(t, msg, "repos.yaml and the trace keys were not changed: restore the entry's trace_context", "the closing advice restores the entry")
}

// A manifest that would drop or replace installed keys is refused before
// any edit, naming key IDs only.
func TestEnsureTraceSigningKeys_StaleManifestRefused(t *testing.T) {
	a, n, x, z := newTraceKey(1), newTraceKey(4), newTraceKey(5), newTraceKey(6)
	cases := []struct {
		name      string
		setup     func(t *testing.T, m *Manifest, fc *forge.FakeClient)
		wantKeyID string
		// retainedMissing: the committed config retains a key that the
		// entry lacks, as while a removal's pull request is open.
		// activeMissing: the entry lacks the committed active key.
		retainedMissing, activeMissing bool
	}{
		{name: "installed installation_id differs", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, a), a)
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallB, a))
		}},
		{name: "installed active key missing from the manifest", wantKeyID: n.id(), activeMissing: true, setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			// An older copy of repos.yaml that predates the last rotation.
			// There is no ledger, so only the installed-key check refuses.
			setManifestTrace(t, m, "acme/api", traceBlock(traceInstallA, a))
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, n, a))
			setTraceSecret(fc, "acme/api")
		}},
		{name: "committed config has one extra retained key", wantKeyID: x.id(), retainedMissing: true, setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, a), a)
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a, x))
		}},
		{name: "installed trace_context, none in the manifest", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a))
			setTraceSecret(fc, "acme/api")
			setTraceLedger(fc, "acme/api", a.id())
		}},
		{name: "ledger names a key outside the set", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, a), a)
			setTraceLedger(fc, "acme/api", z.id())
		}},
	}
	for _, forgeName := range layerForges {
		for _, tc := range cases {
			// A rotation is refused too: it would drop the same keys.
			for _, rotate := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/rotate=%t", forgeName, tc.name, rotate), func(t *testing.T) {
					fc, cfg := traceFixture(t, forgeName, true, "acme/api")
					tc.setup(t, cfg.Manifest, fc)
					before := manifestSnapshot(t, cfg.Manifest)

					plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rotate: rotate})
					require.ErrorIs(t, err, ErrTraceManifestStale)
					assert.Contains(t, err.Error(), "acme/api")
					if tc.wantKeyID != "" {
						assert.Contains(t, err.Error(), tc.wantKeyID)
					}
					if tc.retainedMissing {
						assertPendingRemovalFirst(t, err.Error())
					}
					if tc.activeMissing {
						assertNoPendingRemoval(t, err.Error())
					}
					assertNoTraceValues(t, err.Error(), a, n, x, z)
					assert.Empty(t, plans)
					assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest))
					assertNoForgeWrites(t, fc)
				})
			}
		}
	}
}

// A committed key that the repos.yaml entry lacks may come from an edit
// to the committed config rather than from an older repos.yaml, and then
// neither removal (the key is active) nor rotation gets past the refusal.
// The refusal says so: adding the key to repos.yaml would trust it.
func TestEnsureTraceSigningKeys_StaleRefusalWarnsOfEditedConfig(t *testing.T) {
	a, f := newTraceKey(1), newTraceKey(8)
	for _, forgeName := range layerForges {
		t.Run(forgeName, func(t *testing.T) {
			fc, cfg := traceFixture(t, forgeName, true, "acme/api")
			healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, a), a)
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, f, a))

			_, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest})
			require.ErrorIs(t, err, ErrTraceManifestStale)
			assert.Contains(t, err.Error(), f.id())
			assert.Contains(t, err.Error(), "revert that change", "the refusal says to revert an edit it did not expect")
			assert.Contains(t, err.Error(), "do not add the key to repos.yaml", "the refusal warns against trusting the key")
			assertNoPendingRemoval(t, err.Error())
			assertNoTraceValues(t, err.Error(), a, f)
		})
	}
}

// One stale repository refuses the run before any edit, even when a
// repository selected before it could be planned.
func TestEnsureTraceSigningKeys_StaleRepoRefusesBeforeAnyEdit(t *testing.T) {
	a, b, x := newTraceKey(1), newTraceKey(2), newTraceKey(5)
	for _, forgeName := range layerForges {
		t.Run(forgeName, func(t *testing.T) {
			fc, cfg := traceFixture(t, forgeName, true, "acme/api", "acme/web")
			// acme/api needs recovery: its ledger is missing.
			setManifestTrace(t, cfg.Manifest, "acme/api", traceBlock(traceInstallA, a))
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a))
			setTraceSecret(fc, "acme/api")
			// acme/web's committed config trusts a key its entry lacks.
			healthyTrace(t, cfg.Manifest, fc, "acme/web", traceBlock(traceInstallB, b), b)
			setInstalledTrace(fc, "acme/web", traceBlock(traceInstallB, b, x))
			before := manifestSnapshot(t, cfg.Manifest)

			plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest})
			require.ErrorIs(t, err, ErrTraceManifestStale)
			assert.Contains(t, err.Error(), "acme/web")
			assertNoTraceValues(t, err.Error(), a, b, x)
			assert.Empty(t, plans)
			assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest), "no entry is edited")
			assertNoForgeWrites(t, fc)
		})
	}
}

func TestEnsureTraceSigningKeys_DuplicateInstallationIDRefused(t *testing.T) {
	a, b := newTraceKey(1), newTraceKey(2)
	for _, forgeName := range layerForges {
		// The other entry counts even when the run does not select it.
		for _, sel := range []struct {
			name   string
			filter []string
		}{{"all selected", nil}, {"one selected", []string{"acme/api"}}} {
			t.Run(forgeName+"/"+sel.name, func(t *testing.T) {
				fc, cfg := traceFixture(t, forgeName, true, "acme/api", "acme/web")
				healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, a), a)
				healthyTrace(t, cfg.Manifest, fc, "acme/web", traceBlock(traceInstallA, b), b)
				before := manifestSnapshot(t, cfg.Manifest)

				plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, RepoFilter: sel.filter})
				require.ErrorIs(t, err, ErrTraceManifestStale)
				assertNoTraceValues(t, err.Error(), a, b)
				assert.Empty(t, plans)
				assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest))
				assertNoForgeWrites(t, fc)
			})
		}
	}
}

// While no fullsend workflow is on the default branch, a committed block
// whose installation ID differs from the entry's belongs to an ended
// installation and does not make the entry stale. The other refusals stay:
// with a fullsend workflow on the default branch the mismatch is still
// refused, and so are a ledger naming a key the entry does not list and an
// installation ID another entry uses. A refused run reports nothing.
func TestEnsureTraceSigningKeys_EndedInstallationRefusals(t *testing.T) {
	a, k, z := newTraceKey(1), newTraceKey(2), newTraceKey(9)
	cases := []struct {
		name string
		// installed puts a fullsend workflow on the default branch.
		installed bool
		setup     func(t *testing.T, m *Manifest, fc *forge.FakeClient)
		want      string
	}{
		{name: "fullsend workflow on the default branch", installed: true, want: "installation_id differs"},
		{name: "ledger names a key the entry does not list", want: forge.VarTraceSigningKeyID + " does not name a key", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			setTraceLedger(fc, "acme/api", a.id())
		}},
		{name: "another entry uses the installation ID", want: "also used by the repos.yaml entry acme/web", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			setManifestTrace(t, m, "acme/web", traceBlock(traceInstallB, z))
		}},
	}
	for _, forgeName := range layerForges {
		for _, tc := range cases {
			t.Run(forgeName+"/"+tc.name, func(t *testing.T) {
				fc, cfg := traceFixture(t, forgeName, tc.installed, "acme/api", "acme/web")
				// The entry's installation B replaced installation A, whose
				// block is still committed; the seed and the ledger are B's.
				setManifestTrace(t, cfg.Manifest, "acme/api", traceBlock(traceInstallB, k))
				setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a))
				setTraceSecret(fc, "acme/api")
				setTraceLedger(fc, "acme/api", k.id())
				if tc.setup != nil {
					tc.setup(t, cfg.Manifest, fc)
				}
				before := manifestSnapshot(t, cfg.Manifest)
				progress := &progressLog{}

				plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{
					Manifest: cfg.Manifest, Rand: traceRand(1),
				}, newTestClientFactory(fc), progress.fn)
				require.ErrorIs(t, err, ErrTraceManifestStale)
				assert.Contains(t, err.Error(), tc.want)
				if !tc.installed {
					assert.NotContains(t, err.Error(), "installation_id differs", "the ended installation's block is no reason")
					assert.NotContains(t, err.Error(), "may have been edited", "the refusal gives no false hint")
				}
				assertNoTraceValues(t, err.Error(), a, k, z)
				assert.Empty(t, plans)
				assert.Empty(t, progress.all(), "a refusal reports nothing")
				assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest))
				assertNoForgeWrites(t, fc)
			})
		}
	}
}

// The keys of an ended installation's committed block are neither active
// nor retained in the repository: the installation in the entry, or the
// fresh install, replaces that block and stops trusting them. So a removal
// that names one is refused as naming no key a selected repository retains,
// never as an active key that a rotation would retire.
func TestEnsureTraceSigningKeys_RemovalIgnoresEndedBlock(t *testing.T) {
	a, r, k := newTraceKey(1), newTraceKey(2), newTraceKey(3)
	cases := []struct {
		name  string
		setup func(t *testing.T, m *Manifest, fc *forge.FakeClient)
	}{
		// Neither has a fullsend workflow on the default branch.
		{name: "re-enrollment not landed", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			setManifestTrace(t, m, "acme/api", traceBlock(traceInstallB, k))
			setTraceSecret(fc, "acme/api")
			setTraceLedger(fc, "acme/api", k.id())
		}},
		{name: "full uninstall", setup: func(*testing.T, *Manifest, *forge.FakeClient) {}},
	}
	for _, forgeName := range layerForges {
		for _, tc := range cases {
			for _, key := range []struct{ name, id string }{{"active", a.id()}, {"retained", r.id()}} {
				t.Run(forgeName+"/"+tc.name+"/"+key.name, func(t *testing.T) {
					fc, cfg := traceFixture(t, forgeName, false, "acme/api")
					setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a, r))
					tc.setup(t, cfg.Manifest, fc)
					before := manifestSnapshot(t, cfg.Manifest)
					progress := &progressLog{}

					plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{
						Manifest: cfg.Manifest, RemoveKeyIDs: []string{key.id}, Rand: traceRand(1),
					}, newTestClientFactory(fc), progress.fn)
					require.Error(t, err)
					assert.Contains(t, err.Error(), "--remove-trace-verification-key value 1 names no verification key that a selected repository retains")
					assert.NotContains(t, err.Error(), key.id, "the error never echoes the key ID")
					assertNoTraceValues(t, err.Error(), a, r, k)
					assert.Empty(t, plans)
					assert.Empty(t, progress.all(), "a refusal reports nothing")
					assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest))
					assertNoForgeWrites(t, fc)
				})
			}
		}
	}
}

// plannedTraceValues runs the preflight for acme/api in memory and returns
// what its plan records: the seed, the public key and the installation ID.
// Run with the fixture and Rand of a refused run, it shows the values that
// refusal must not carry.
func plannedTraceValues(t *testing.T, fc *forge.FakeClient, cfg TraceKeysConfig, forgeName string) []string {
	t.Helper()
	plans, err := ensureTrace(t, fc, cfg)
	require.NoError(t, err)
	p := requireTracePlan(t, plans, "acme/api", forgeName)
	tc := manifestTrace(t, cfg.Manifest, "acme/api")
	return []string{p.Key.EncodeSeed(), tc.VerificationKeys.Active, tc.InstallationID}
}

// Random keys written through a manifest the command cannot edit would never
// converge, so a needed edit is refused before it is made, and the refusal
// carries none of the values the edit would have recorded. A fresh install
// is the exception: it plans nothing, edits nothing and says how to
// provision the keys later; asking for a rotation still refuses it, also
// when it enrolls a fully uninstalled repository again
// (TestTraceKeys_GitHubReenrollFromReadOnlyManifest covers that one without
// a rotation).
func TestEnsureTraceSigningKeys_ReadOnlyManifestRefused(t *testing.T) {
	a, r := newTraceKey(1), newTraceKey(2)
	const url = "https://example.com/repos.yaml"
	healthy := func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
		healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, a, r), a)
	}
	cases := []struct {
		name      string
		installed bool
		setup     func(t *testing.T, m *Manifest, fc *forge.FakeClient)
		path      string
		remote    bool
		rotate    bool
		remove    []string
		// skipped is a fresh install that goes ahead without keys.
		skipped bool
	}{
		{name: "fresh install", path: url, skipped: true},
		{name: "remotely loaded manifest", remote: true, skipped: true},
		{name: "recovery needs a key", installed: true, path: url, setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthy(t, m, fc)
			delete(fc.VariableValues, "acme/api/"+forge.VarTraceSigningKeyID)
			delete(fc.VariablesExist, "acme/api/"+forge.VarTraceSigningKeyID)
		}},
		{name: "rotation needs a key", installed: true, path: url, rotate: true, setup: healthy},
		{name: "removal needs an edit", installed: true, path: url, remove: []string{r.id()}, setup: healthy},
		{name: "rotation on a remotely loaded manifest", installed: true, remote: true, rotate: true, setup: healthy},
		{name: "fresh install with a rotation", path: url, rotate: true},
		{name: "re-enrollment after a full uninstall with a rotation", path: url, rotate: true, setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			// The committed block belongs to the ended installation.
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := func() (*forge.FakeClient, ConvergeConfig) {
				fc, cfg := traceFixture(t, ForgeGitHub, tc.installed, "acme/api")
				if tc.setup != nil {
					tc.setup(t, cfg.Manifest, fc)
				}
				return fc, cfg
			}
			fc, cfg := fixture()
			cfg.Manifest.sourceRemote = tc.remote
			before := manifestSnapshot(t, cfg.Manifest)
			progress := &progressLog{}

			plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{
				Manifest: cfg.Manifest, ManifestPath: tc.path, Rotate: tc.rotate, RemoveKeyIDs: tc.remove, Rand: traceRand(1),
			}, newTestClientFactory(fc), progress.fn)
			assert.Empty(t, plans)
			assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest))
			assertNoForgeWrites(t, fc)
			if tc.skipped {
				require.NoError(t, err)
				lines := progress.all()
				require.Len(t, lines, 1, "one notice: %v", lines)
				assert.Contains(t, lines[0], "acme/api")
				assert.Contains(t, lines[0], "not provisioned")
				assert.Contains(t, lines[0], "read-only")
				assert.Contains(t, lines[0], "a later repos install acme/api --rotate-trace-signing-key", "the notice names the run that provisions them")
				return
			}
			require.ErrorIs(t, err, ErrTraceManifestReadOnly)
			assert.Empty(t, progress.all(), "a refusal reports nothing")
			assertNoTraceValues(t, err.Error(), a, r)
			if tc.remove == nil {
				probeFC, probe := fixture()
				for _, v := range plannedTraceValues(t, probeFC, TraceKeysConfig{Manifest: probe.Manifest, Rotate: tc.rotate}, ForgeGitHub) {
					assert.NotContains(t, err.Error(), v, "the refusal carries no generated value")
				}
			}
		})
	}
}

// A manifest that cannot be written yields no plan, so convergence never
// writes a key the manifest does not record, and the error carries none of
// the values the edit would have recorded.
func TestEnsureTraceSigningKeys_ManifestWriteFailureReturnsNoPlan(t *testing.T) {
	fc, cfg := traceFixture(t, ForgeGitHub, false, "acme/api")
	plans, err := ensureTrace(t, fc, TraceKeysConfig{
		Manifest: cfg.Manifest, ManifestPath: filepath.Join(t.TempDir(), "missing-dir", "repos.yaml"),
	})
	require.Error(t, err)
	assert.Empty(t, plans)
	assertNoForgeWrites(t, fc)

	probeFC, probe := traceFixture(t, ForgeGitHub, false, "acme/api")
	for _, v := range plannedTraceValues(t, probeFC, TraceKeysConfig{Manifest: probe.Manifest}, ForgeGitHub) {
		assert.NotContains(t, err.Error(), v, "the error carries no generated value")
	}
}

// The DryRun/Deferred contract of EnsureManagedConfigDefaults. The CLI
// passes DryRun true: the in-memory entry is edited and a plan returned,
// and the file is left for the caller to persist. DryRun false writes the
// file, which records the public block and never the seed.
func TestEnsureTraceSigningKeys_ManifestFileFollowsDryRun(t *testing.T) {
	for _, tc := range []struct {
		name             string
		dryRun, deferred bool
	}{
		{"dry run, caller persists", true, true},
		{"dry run preview", true, false},
		{"write", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc, cfg := traceFixture(t, ForgeGitHub, false, "acme/api")
			path := filepath.Join(t.TempDir(), "repos.yaml")
			before := manifestSnapshot(t, cfg.Manifest)
			require.NoError(t, os.WriteFile(path, []byte(before), 0o644))

			plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, ManifestPath: path, DryRun: tc.dryRun, Deferred: tc.deferred})
			require.NoError(t, err)
			p := requireTracePlan(t, plans, "acme/api", ForgeGitHub)
			mt := manifestTrace(t, cfg.Manifest, "acme/api")
			assertPlanKeyIsActive(t, p, mt)
			onDisk, err := os.ReadFile(path)
			require.NoError(t, err)
			if tc.dryRun {
				assert.Equal(t, before, string(onDisk), "a dry run leaves the file to the caller")
			} else {
				assert.Contains(t, string(onDisk), mt.InstallationID)
				assert.Contains(t, string(onDisk), mt.VerificationKeys.Active)
			}
			assert.False(t, strings.Contains(string(onDisk), p.Key.EncodeSeed()), "the manifest never holds the seed")
			assertNoForgeWrites(t, fc)
		})
	}
}

func TestEnsureTraceSigningKeys_DeterministicRand(t *testing.T) {
	run := func(seed int64) (*TraceKeyPlan, string) {
		fc, cfg := traceFixture(t, ForgeGitHub, false, "acme/api")
		plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rand: traceRand(seed)})
		require.NoError(t, err)
		p := requireTracePlan(t, plans, "acme/api", ForgeGitHub)
		return p, manifestTrace(t, cfg.Manifest, "acme/api").InstallationID
	}
	p1, id1 := run(42)
	p2, id2 := run(42)
	p3, id3 := run(43)
	assert.True(t, p1.Key.EncodeSeed() == p2.Key.EncodeSeed(), "the same Rand gives the same key")
	assert.Equal(t, id1, id2)
	assert.False(t, p1.Key.EncodeSeed() == p3.Key.EncodeSeed(), "the key comes from Rand")
	assert.NotEqual(t, id1, id3, "the installation ID comes from Rand")
}

// Every repository gets its own installation ID and key, even when one
// run provisions several from one Rand.
func TestEnsureTraceSigningKeys_FreshReposGetDistinctInstallations(t *testing.T) {
	for _, forgeName := range layerForges {
		t.Run(forgeName, func(t *testing.T) {
			fc, cfg := traceFixture(t, forgeName, false, "acme/api", "acme/web")
			plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest})
			require.NoError(t, err)
			api := requireTracePlan(t, plans, "acme/api", forgeName)
			web := requireTracePlan(t, plans, "acme/web", forgeName)
			assert.False(t, api.Key.EncodeSeed() == web.Key.EncodeSeed(), "each repository gets its own key")
			assert.NotEqual(t, manifestTrace(t, cfg.Manifest, "acme/api").InstallationID,
				manifestTrace(t, cfg.Manifest, "acme/web").InstallationID)
		})
	}
}

// A removal applies across the selected repositories. The key goes
// from every selected repository that retains it; a selected repository
// that does not retain it, and a repository the run does not select, keep
// their entries as they were.
func TestEnsureTraceSigningKeys_RemoveAcrossSelectedRepos(t *testing.T) {
	a, b, r, k, w := newTraceKey(1), newTraceKey(2), newTraceKey(3), newTraceKey(4), newTraceKey(5)
	cases := []struct {
		name string
		// web is acme/web's block before the run, wantWeb after it.
		web, wantWeb string
		filter       []string
	}{
		{"acme/web does not retain it", traceBlock(traceInstallB, b, w), traceBlock(traceInstallB, b, w), nil},
		{"acme/web retains it too", traceBlock(traceInstallB, b, r, w), traceBlock(traceInstallB, b, w), nil},
		{"acme/web retains it but is not selected", traceBlock(traceInstallB, b, r, w), traceBlock(traceInstallB, b, r, w), []string{"acme/api"}},
	}
	for _, forgeName := range layerForges {
		for _, tc := range cases {
			t.Run(forgeName+"/"+tc.name, func(t *testing.T) {
				fc, cfg := traceFixture(t, forgeName, true, "acme/api", "acme/web")
				healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, a, r, k), a)
				healthyTrace(t, cfg.Manifest, fc, "acme/web", tc.web, b)
				_, want := traceFixture(t, forgeName, true, "acme/api", "acme/web")
				setManifestTrace(t, want.Manifest, "acme/api", traceBlock(traceInstallA, a, k))
				setManifestTrace(t, want.Manifest, "acme/web", tc.wantWeb)

				plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, RepoFilter: tc.filter, RemoveKeyIDs: []string{r.id()}})
				require.NoError(t, err)
				assert.Empty(t, plans, "a removal alone writes no key")
				assert.Equal(t, []string{k.pubB64()}, manifestTrace(t, cfg.Manifest, "acme/api").VerificationKeys.Retained,
					"acme/api drops the named key only")
				assert.Equal(t, manifestTrace(t, want.Manifest, "acme/web").VerificationKeys.Retained,
					manifestTrace(t, cfg.Manifest, "acme/web").VerificationKeys.Retained, "acme/web's retained keys")
				assert.Equal(t, manifestSnapshot(t, want.Manifest), manifestSnapshot(t, cfg.Manifest), "nothing else in either entry changes")
				assertNoForgeWrites(t, fc)
			})
		}
	}
}

// A removal is refused before any edit when no selected repository
// retains the key, or when the key is the active key of a selected
// repository, even one whose key another selected repository retains.
// Every run also names r, which acme/api does retain, so a run that edits
// before it has checked every key ID is caught. The error names the flag,
// never a key ID.
func TestEnsureTraceSigningKeys_RemoveAcrossReposRefusals(t *testing.T) {
	a, b, r, w, x, z := newTraceKey(1), newTraceKey(2), newTraceKey(3), newTraceKey(4), newTraceKey(5), newTraceKey(9)
	cases := []struct {
		name string
		// api and web list each entry's active key, then its retained keys.
		api, web []traceKey
		filter   []string
		keyID    string
	}{
		{"no selected repository retains it", []traceKey{a, r}, []traceKey{b, w}, nil, z.id()},
		{"only a repository the filter leaves out retains it", []traceKey{a, r}, []traceKey{b, w}, []string{"acme/api"}, w.id()},
		{"active in a later repository, retained by an earlier one", []traceKey{a, r, x}, []traceKey{x, w}, nil, x.id()},
		{"active in an earlier repository, retained by a later one", []traceKey{x, r}, []traceKey{b, w, x}, nil, x.id()},
	}
	for _, forgeName := range layerForges {
		for _, tc := range cases {
			t.Run(forgeName+"/"+tc.name, func(t *testing.T) {
				fc, cfg := traceFixture(t, forgeName, true, "acme/api", "acme/web")
				healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, tc.api[0], tc.api[1:]...), tc.api[0])
				healthyTrace(t, cfg.Manifest, fc, "acme/web", traceBlock(traceInstallB, tc.web[0], tc.web[1:]...), tc.web[0])
				before := manifestSnapshot(t, cfg.Manifest)
				remove := []string{r.id(), tc.keyID}

				plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, RepoFilter: tc.filter, RemoveKeyIDs: remove})
				require.Error(t, err)
				assert.Contains(t, err.Error(), "--remove-trace-verification-key", "the error names the flag")
				for _, id := range remove {
					assert.NotContains(t, err.Error(), id, "the error never echoes a key ID")
				}
				assertNoTraceValues(t, err.Error(), a, b, r, w, x, z)
				assert.Empty(t, plans)
				assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest), "neither entry is edited")
				assertNoForgeWrites(t, fc)
			})
		}
	}
}

// A removal is checked against the keys retained before this run, so
// the active key cannot be removed by the run that replaces it, whether the
// run rotates, recovers, or regenerates an exposed GitLab seed.
// Rotation and removal of the retired key are separate runs. On GitHub the
// existence check finds the exposed-seed fixture healthy, which is the
// plain active-key refusal. A key the entry retains is still refused while
// the committed config has it active, as before the pull request of its
// rotation merges, and the refusal says to remove it once the rotation is on
// the default branch. That refusal comes first when an interrupted rotation
// left FULLSEND_TRACE_SIGNING_KEY_ID naming the key too.
func TestEnsureTraceSigningKeys_ReplacedActiveKeyNotRemovable(t *testing.T) {
	a, r, b := newTraceKey(1), newTraceKey(2), newTraceKey(3)
	cases := []struct {
		name   string
		rotate bool
		setup  func(t *testing.T, m *Manifest, fc *forge.FakeClient)
	}{
		{name: "rotation", rotate: true, setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, a, r), a)
		}},
		{name: "recovery of a missing ledger", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			setManifestTrace(t, m, "acme/api", traceBlock(traceInstallA, a, r))
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a, r))
			setTraceSecret(fc, "acme/api")
		}},
		{name: "exposed seed", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, a, r), a)
			fc.SecretProtections["acme/api/"+forge.SecretTraceSigningKey] = forge.SecretProtection{Exists: true, Protected: true}
		}},
		{name: "rotation not on the default branch yet", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, b, r, a), b)
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a, r))
		}},
		{name: "rotation interrupted before its ledger write", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, b, r, a), b)
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a, r))
			setTraceLedger(fc, "acme/api", a.id())
		}},
	}
	for _, forgeName := range layerForges {
		for _, tc := range cases {
			t.Run(forgeName+"/"+tc.name, func(t *testing.T) {
				fc, cfg := traceFixture(t, forgeName, true, "acme/api")
				tc.setup(t, cfg.Manifest, fc)
				before := manifestSnapshot(t, cfg.Manifest)

				plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rotate: tc.rotate, RemoveKeyIDs: []string{a.id()}})
				require.Error(t, err)
				assert.Contains(t, err.Error(), "--remove-trace-verification-key", "the error names the flag")
				assert.Contains(t, err.Error(), "once the rotation is on the default branch", "the error says when the removal can run")
				assert.NotContains(t, err.Error(), forge.VarTraceSigningKeyID, "the active-key refusal comes first")
				assert.NotContains(t, err.Error(), a.id(), "the error never echoes the key ID")
				assertNoTraceValues(t, err.Error(), a, r, b)
				assert.Empty(t, plans)
				assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest), "the entry is not edited")
				assertNoForgeWrites(t, fc)
			})
		}
	}
}

// After an interrupted run, FULLSEND_TRACE_SIGNING_KEY_ID can name a key the
// entry retains. The next run's key write makes it name a new key, but that
// write can wait on GitLab, until the fullsend jobs unset the seed, or fail.
// Removing the key the ledger names before then would leave the ledger naming
// a key the entry no longer lists, which every later run refuses as stale. So
// that removal is refused before any edit on both forges, with or without a
// rotation, whether or not the run's key write waits, and nothing is
// reported. The refusal names the value's position, the repository and the
// ledger, never a key ID. It says to run install without that value, not
// without the flag, since another value can be a pending removal that every
// run must name, and to remove the key only once a run has written a new key
// ID to the ledger, which a run whose key write waits or fails does not.
func TestEnsureTraceSigningKeys_KeyTheLedgerNamesNotRemovable(t *testing.T) {
	a, l := newTraceKey(1), newTraceKey(2)
	cases := []struct {
		name, forge string
		setup       func(t *testing.T, fc *forge.FakeClient)
	}{
		{name: ForgeGitHub, forge: ForgeGitHub},
		{name: ForgeGitLab, forge: ForgeGitLab, setup: func(t *testing.T, fc *forge.FakeClient) {
			// The fullsend jobs on the default branch unset the seed.
			commitGitLabJobTemplates(t, fc, "acme/api", nil)
		}},
		{name: ForgeGitLab + "/the committed jobs do not unset the seed", forge: ForgeGitLab, setup: func(t *testing.T, fc *forge.FakeClient) {
			commitGitLabJobTemplates(t, fc, "acme/api", withoutTraceUnset)
		}},
	}
	for _, tc := range cases {
		for _, rotate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/rotate=%t", tc.name, rotate), func(t *testing.T) {
				fc, cfg := traceFixture(t, tc.forge, true, "acme/api")
				healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, a, l), a)
				setTraceLedger(fc, "acme/api", l.id())
				if tc.setup != nil {
					tc.setup(t, fc)
				}
				before := manifestSnapshot(t, cfg.Manifest)
				progress := &progressLog{}

				plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{
					Manifest: cfg.Manifest, Rotate: rotate, RemoveKeyIDs: []string{l.id()}, Rand: traceRand(1),
				}, newTestClientFactory(fc), progress.fn)
				require.Error(t, err)
				for _, want := range []string{removeTraceKeyFlag + " value 1 ", "acme/api", forge.VarTraceSigningKeyID,
					"run repos install without that value first",
					"once a run has written a new key ID to " + forge.VarTraceSigningKeyID} {
					assert.Contains(t, err.Error(), want)
				}
				for _, id := range []string{a.id(), l.id()} {
					assert.NotContains(t, err.Error(), id, "the error never echoes a key ID")
				}
				assertNoTraceValues(t, err.Error(), a, l)
				assert.Empty(t, plans)
				assert.Empty(t, progress.all(), "a refusal reports nothing")
				assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest), "the entry is not edited")
				assertNoForgeWrites(t, fc)
			})
		}
	}
}

// The refusal names only the repositories whose ledger names the key, not
// every selected repository that retains it.
func TestEnsureTraceSigningKeys_KeyTheLedgerNamesRefusalNamesItsRepo(t *testing.T) {
	a, b, l := newTraceKey(1), newTraceKey(2), newTraceKey(3)
	for _, forgeName := range layerForges {
		t.Run(forgeName, func(t *testing.T) {
			fc, cfg := traceFixture(t, forgeName, true, "acme/api", "acme/web")
			healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, a, l), a)
			healthyTrace(t, cfg.Manifest, fc, "acme/web", traceBlock(traceInstallB, b, l), b)
			setTraceLedger(fc, "acme/web", l.id())
			before := manifestSnapshot(t, cfg.Manifest)

			plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, RemoveKeyIDs: []string{l.id()}})
			require.Error(t, err)
			assert.Contains(t, err.Error(), forge.VarTraceSigningKeyID)
			assert.Contains(t, err.Error(), "acme/web")
			assert.NotContains(t, err.Error(), "acme/api", "acme/api's ledger names its active key")
			assert.NotContains(t, err.Error(), l.id(), "the error never echoes the key ID")
			assertNoTraceValues(t, err.Error(), a, b, l)
			assert.Empty(t, plans)
			assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest), "neither entry is edited")
			assertNoForgeWrites(t, fc)
		})
	}
}

// The refusal covers only the key the ledger names: while
// FULLSEND_TRACE_SIGNING_KEY_ID names a retained key after an interrupted
// run, the same run removes another retained key. Where the run can write a
// key it recovers and retires the active key. On GitLab, while the committed
// jobs do not unset the seed, the removal applies alone, and the ledger still
// names a key the entry lists.
func TestEnsureTraceSigningKeys_KeyTheLedgerNamesRefusalSparesOtherKeys(t *testing.T) {
	a, l, r := newTraceKey(1), newTraceKey(2), newTraceKey(3)
	cases := []struct {
		name, forge string
		edit        func(t *testing.T, path string, content []byte) []byte
		recovery    bool
	}{
		{name: ForgeGitHub, forge: ForgeGitHub, recovery: true},
		{name: ForgeGitLab, forge: ForgeGitLab, recovery: true},
		{name: ForgeGitLab + "/the committed jobs do not unset the seed", forge: ForgeGitLab, edit: withoutTraceUnset},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc, cfg := traceFixture(t, tc.forge, true, "acme/api")
			healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, a, l, r), a)
			setTraceLedger(fc, "acme/api", l.id())
			if tc.forge == ForgeGitLab {
				commitGitLabJobTemplates(t, fc, "acme/api", tc.edit)
			}

			plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, RemoveKeyIDs: []string{r.id()}})
			require.NoError(t, err)
			got := manifestTrace(t, cfg.Manifest, "acme/api").VerificationKeys
			if tc.recovery {
				assert.Equal(t, "recovery", requireTracePlan(t, plans, "acme/api", tc.forge).Reason)
				assert.Equal(t, []string{l.pubB64(), a.pubB64()}, got.Retained, "r goes and the active key retires")
			} else {
				assert.Empty(t, plans, "no key while the committed jobs do not unset the seed")
				assert.Equal(t, a.pubB64(), got.Active)
				assert.Equal(t, []string{l.pubB64()}, got.Retained, "r goes and the key the ledger names stays")
			}
			assertNoForgeWrites(t, fc)
		})
	}
}

// A key retained before the run can be removed by the run that
// rotates: one key is planned, the old active key moves to retained and
// only the named key is dropped.
func TestEnsureTraceSigningKeys_RotateAndRemoveRetainedKey(t *testing.T) {
	a, r1, r2 := newTraceKey(1), newTraceKey(2), newTraceKey(3)
	for _, forgeName := range layerForges {
		t.Run(forgeName, func(t *testing.T) {
			fc, cfg := traceFixture(t, forgeName, true, "acme/api")
			healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, a, r1, r2), a)
			if forgeName == ForgeGitLab {
				// The fullsend jobs on the default branch unset the seed.
				commitGitLabJobTemplates(t, fc, "acme/api", nil)
			}

			plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rotate: true, RemoveKeyIDs: []string{r1.id()}})
			require.NoError(t, err)
			assert.Len(t, plans, 1, "the rotation plans one key write")
			p := requireTracePlan(t, plans, "acme/api", forgeName)
			assert.Equal(t, "rotation", p.Reason)

			got := manifestTrace(t, cfg.Manifest, "acme/api")
			assert.Equal(t, traceInstallA, got.InstallationID, "the installation ID is kept")
			assertPlanKeyIsActive(t, p, got)
			assert.NotEqual(t, a.pubB64(), got.VerificationKeys.Active)
			assert.ElementsMatch(t, []string{r2.pubB64(), a.pubB64()}, got.VerificationKeys.Retained,
				"the old active key is retained and only the named key goes")
			assertNoForgeWrites(t, fc)
		})
	}
}

// On GitLab, a seed variable that is not usable (unmasked, unprotected,
// environment-scoped or file-type) is exposed and is regenerated like a
// recovery: the installation ID is kept, the previous active key stays
// trusted as retained, and progress names its key ID with the removal
// flag. A masked, protected seed is healthy. GitHub keeps the existence
// check: whatever the fake reports about protection, the healthy install
// plans nothing there.
func TestEnsureTraceSigningKeys_ExposedGitLabSeedRegenerated(t *testing.T) {
	a, r := newTraceKey(1), newTraceKey(2)
	usable := forge.SecretProtection{Exists: true, Masked: true, Protected: true}
	cases := []struct {
		name string
		prot forge.SecretProtection
	}{
		{"masked and protected", usable},
		{"unmasked", forge.SecretProtection{Exists: true, Protected: true}},
		{"unprotected", forge.SecretProtection{Exists: true, Masked: true}},
		{"environment-scoped", forge.SecretProtection{Exists: true, Masked: true, Protected: true, EnvironmentScoped: true}},
		{"file-type", forge.SecretProtection{Exists: true, Masked: true, Protected: true, FileType: true}},
	}
	for _, forgeName := range layerForges {
		for _, tc := range cases {
			t.Run(forgeName+"/"+tc.name, func(t *testing.T) {
				fc, cfg := traceFixture(t, forgeName, true, "acme/api")
				healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, a, r), a)
				fc.SecretProtections["acme/api/"+forge.SecretTraceSigningKey] = tc.prot
				if forgeName == ForgeGitLab {
					// The fullsend jobs on the default branch unset the seed.
					commitGitLabJobTemplates(t, fc, "acme/api", nil)
				}
				before := manifestSnapshot(t, cfg.Manifest)
				progress := &progressLog{}

				plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{
					Manifest: cfg.Manifest, Rand: traceRand(1),
				}, newTestClientFactory(fc), progress.fn)
				require.NoError(t, err)
				assertNoForgeWrites(t, fc)
				if forgeName == ForgeGitHub || tc.prot == usable {
					assert.Empty(t, plans, "the seed is healthy")
					assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest))
					return
				}
				p := requireTracePlan(t, plans, "acme/api", ForgeGitLab)
				assert.Equal(t, "exposed", p.Reason)
				got := manifestTrace(t, cfg.Manifest, "acme/api")
				assert.Equal(t, traceInstallA, got.InstallationID, "the installation ID is kept")
				assertPlanKeyIsActive(t, p, got)
				assert.NotEqual(t, a.pubB64(), got.VerificationKeys.Active)
				assert.ElementsMatch(t, []string{r.pubB64(), a.pubB64()}, got.VerificationKeys.Retained,
					"the exposed key stays trusted until an operator removes it")
				lines := progress.all()
				assert.True(t, slices.ContainsFunc(lines, func(l string) bool {
					return strings.Contains(l, a.id()) && strings.Contains(l, "--remove-trace-verification-key")
				}), "a progress line names the exposed key's ID with the removal flag:\n%s", strings.Join(lines, "\n"))
				for _, l := range lines {
					assert.False(t, strings.Contains(l, p.Key.EncodeSeed()), "progress never shows the seed")
				}
			})
		}
	}
}

// A message that lists key IDs fits their count. After an interrupted run,
// an exposed GitLab seed belongs to the active key or to the key the ledger
// names, so progress names both; one run can remove several retained keys;
// and a stale refusal names every committed key that the entry lacks.
func TestEnsureTraceSigningKeys_KeyIDListsFitTheirCount(t *testing.T) {
	a, r1, r2 := newTraceKey(1), newTraceKey(2), newTraceKey(3)
	run := func(t *testing.T, fc *forge.FakeClient, cfg TraceKeysConfig) ([]string, map[string]*TraceKeyPlan, error) {
		t.Helper()
		progress := &progressLog{}
		cfg.Rand = traceRand(1)
		plans, err := EnsureTraceSigningKeys(context.Background(), cfg, newTestClientFactory(fc), progress.fn)
		return progress.all(), plans, err
	}
	hasLine := func(t *testing.T, lines []string, want string) {
		t.Helper()
		assert.True(t, slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, want) }),
			"no progress line contains %q:\n%s", want, strings.Join(lines, "\n"))
	}

	t.Run("exposed GitLab seed after an interrupted run", func(t *testing.T) {
		fc, cfg := traceFixture(t, ForgeGitLab, true, "acme/api")
		healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, a, r1), a)
		setTraceLedger(fc, "acme/api", r1.id())
		fc.SecretProtections["acme/api/"+forge.SecretTraceSigningKey] = forge.SecretProtection{Exists: true, Protected: true}
		// The fullsend jobs on the default branch unset the seed.
		commitGitLabJobTemplates(t, fc, "acme/api", nil)

		lines, plans, err := run(t, fc, TraceKeysConfig{Manifest: cfg.Manifest})
		require.NoError(t, err)
		assert.Equal(t, "exposed", requireTracePlan(t, plans, "acme/api", ForgeGitLab).Reason)
		assert.ElementsMatch(t, []string{r1.pubB64(), a.pubB64()}, manifestTrace(t, cfg.Manifest, "acme/api").VerificationKeys.Retained)
		hasLine(t, lines, fmt.Sprintf("key IDs %s, %s stay trusted as retained keys until you remove them with %s",
			a.id(), r1.id(), removeTraceKeyFlag))
	})

	t.Run("two retained keys removed", func(t *testing.T) {
		fc, cfg := traceFixture(t, ForgeGitHub, true, "acme/api")
		healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, a, r1, r2), a)

		lines, _, err := run(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, RemoveKeyIDs: []string{r1.id(), r2.id()}})
		require.NoError(t, err)
		assert.Empty(t, manifestTrace(t, cfg.Manifest, "acme/api").VerificationKeys.Retained)
		hasLine(t, lines, fmt.Sprintf("retained trace verification key IDs %s, %s from", r1.id(), r2.id()))
	})

	t.Run("two committed keys missing from the entry", func(t *testing.T) {
		fc, cfg := traceFixture(t, ForgeGitHub, true, "acme/api")
		healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, a), a)
		setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a, r1, r2))
		ids := []string{r1.id(), r2.id()}
		slices.Sort(ids)

		_, _, err := run(t, fc, TraceKeysConfig{Manifest: cfg.Manifest})
		require.ErrorIs(t, err, ErrTraceManifestStale)
		assert.Contains(t, err.Error(), "trusts key IDs "+strings.Join(ids, ", ")+", which")
		assertNoTraceValues(t, err.Error(), a, r1, r2)
	})
}

// A plan carries the FULLSEND_TRACE_SIGNING_KEY_ID value its preflight read,
// so the write can tell whether another install changed it since.
func TestEnsureTraceSigningKeys_PlanCarriesTheLedgerItRead(t *testing.T) {
	old := newTraceKey(1)
	// Each keyed reason starts from a healthy install whose ledger names old.
	keyed := []struct {
		reason string
		rotate bool
		setup  func(fc *forge.FakeClient)
	}{
		{reason: "recovery", setup: func(fc *forge.FakeClient) { delete(fc.Secrets, "acme/api/"+forge.SecretTraceSigningKey) }},
		{reason: "rotation", rotate: true, setup: func(*forge.FakeClient) {}},
		{reason: "exposed", setup: func(fc *forge.FakeClient) {
			fc.SecretProtections["acme/api/"+forge.SecretTraceSigningKey] = forge.SecretProtection{Exists: true, Protected: true}
		}},
	}
	for _, forgeName := range layerForges {
		t.Run(forgeName+"/fresh install", func(t *testing.T) {
			fc, cfg := traceFixture(t, forgeName, false, "acme/api")
			plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest})
			require.NoError(t, err)
			assert.Empty(t, requireTracePlan(t, plans, "acme/api", forgeName).Ledger)
		})
		for _, k := range keyed {
			// Only GitLab reports an exposed seed.
			if k.reason == "exposed" && forgeName != ForgeGitLab {
				continue
			}
			t.Run(forgeName+"/"+k.reason, func(t *testing.T) {
				fc, cfg := traceFixture(t, forgeName, true, "acme/api")
				healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, old), old)
				k.setup(fc)
				if forgeName == ForgeGitLab {
					commitGitLabJobTemplates(t, fc, "acme/api", nil)
				}
				plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rotate: k.rotate})
				require.NoError(t, err)
				p := requireTracePlan(t, plans, "acme/api", forgeName)
				assert.Equal(t, k.reason, p.Reason)
				assert.Equal(t, old.id(), p.Ledger)
			})
		}
	}
}
