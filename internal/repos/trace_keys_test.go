package repos

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
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
)

// Seeds never reach an assertion that prints its arguments: tests compare
// them with assert.True or count them, and pass plans, keys and seed
// slices to no assertion that renders them.

// traceFixture returns GitHub repositories, fresh or fully installed, with
// the convergence config that installs them.
func traceFixture(t *testing.T, installed bool, repos ...string) (*forge.FakeClient, ConvergeConfig) {
	t.Helper()
	fc := newFakeClientForBatch(repos...)
	cfg := convergeCfgWithDefaults(newConvergeManifest(repos...))
	if !installed {
		return fc, cfg
	}
	for _, r := range repos {
		markTraceInstalled(t, fc, r)
	}
	return fc, withoutInferenceInputs(cfg)
}

// markTraceInstalled makes name a current GitHub install: its workflow, the
// scaffold files, the variables and the inference secrets.
func markTraceInstalled(t *testing.T, fc *forge.FakeClient, name string) {
	t.Helper()
	owner, repo, _ := strings.Cut(name, "/")
	markFullyInstalled(fc, owner, repo)
	populateScaffoldContent(t, fc, owner, repo, "v1.0.0", "https://mint.example.com")
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

// setKeyIDVar sets name's FULLSEND_TRACE_SIGNING_KEY_ID to value.
func setKeyIDVar(fc *forge.FakeClient, name, value string) {
	fc.VariableValues[name+"/"+forge.VarTraceSigningKeyID] = value
	fc.VariablesExist[name+"/"+forge.VarTraceSigningKeyID] = true
}

// healthyTrace makes name a healthy install of block: the manifest entry
// and the committed config carry it, the secret exists and the key ID
// variable names active.
func healthyTrace(t *testing.T, m *Manifest, fc *forge.FakeClient, name, block string, active traceKey) {
	t.Helper()
	setManifestTrace(t, m, name, block)
	setInstalledTrace(fc, name, block)
	setTraceSecret(fc, name)
	setKeyIDVar(fc, name, active.id())
}

func ensureTrace(t *testing.T, fc forge.Client, cfg TraceKeysConfig) (map[string]*TraceKeyPlan, error) {
	t.Helper()
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

// requireTracePlan returns name's plan and checks that it holds a key.
func requireTracePlan(t *testing.T, plans map[string]*TraceKeyPlan, name string) *TraceKeyPlan {
	t.Helper()
	p := plans[name]
	require.True(t, p != nil, "no trace key plan for %s (plans for %v)", name, planNames(plans))
	require.False(t, p.Key.IsZero(), "the plan for %s holds no key", name)
	return p
}

// planNames returns the repositories plans holds a plan for, sorted, so an
// assertion can show them without showing a key.
func planNames(plans map[string]*TraceKeyPlan) []string {
	return slices.Sorted(maps.Keys(plans))
}

// assertPlanKeyIsActive checks that the plan key's public key is tc's
// active key and that the plan's seed expands to it.
func assertPlanKeyIsActive(t *testing.T, p *TraceKeyPlan, tc *config.TraceContextConfig) {
	t.Helper()
	pub := p.Key.Public()
	assert.Equal(t, base64.StdEncoding.EncodeToString(pub), tc.VerificationKeys.Active)
	seed, err := base64.StdEncoding.DecodeString(p.Key.EncodeSeed())
	require.NoError(t, err)
	require.True(t, len(seed) == ed25519.SeedSize, "the plan holds a 32-byte seed")
	assert.True(t, bytes.Equal(pub, ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)), "the plan's seed expands to its public key")
}

// assertTraceBlockComplete checks the generated parts of a block.
func assertTraceBlockComplete(t *testing.T, tc *config.TraceContextConfig) {
	t.Helper()
	assert.Regexp(t, `^[0-9a-f]{32}$`, tc.InstallationID)
	assert.NotEqual(t, strings.Repeat("0", 32), tc.InstallationID)
	require.NotNil(t, tc.SigningKey)
	assert.Equal(t, forge.SecretTraceSigningKey, tc.SigningKey.Secret)
	assert.NoError(t, config.ValidateTraceContext(tc))
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

// progressLog collects progress lines as "repo phase message"; convergence
// reports concurrently.
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

// refuseTrace runs the preflight, which must refuse, and checks that the
// error shows no non-empty value of hidden and no value of keys, before any
// assertion that prints it, and that nothing was planned, edited, written
// or reported. It returns the error for the caller's own checks.
func refuseTrace(t *testing.T, fc *forge.FakeClient, cfg TraceKeysConfig, hidden []string, keys ...traceKey) error {
	t.Helper()
	before := manifestSnapshot(t, cfg.Manifest)
	progress := &progressLog{}
	plans, err := EnsureTraceSigningKeys(context.Background(), cfg, newTestClientFactory(fc), progress.fn)
	require.Error(t, err)
	for _, v := range hidden {
		require.False(t, v != "" && strings.Contains(err.Error(), v), "the error never shows a hidden value")
	}
	assertNoTraceValues(t, err.Error(), keys...)
	assert.Empty(t, planNames(plans))
	assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest), "no entry is edited")
	assertNoForgeWrites(t, fc)
	assert.Empty(t, progress.all(), "a refusal reports nothing")
	return err
}

// countLines returns how many lines contain every one of parts.
func countLines(lines []string, parts ...string) int {
	n := 0
	for _, l := range lines {
		if !slices.ContainsFunc(parts, func(p string) bool { return !strings.Contains(l, p) }) {
			n++
		}
	}
	return n
}

// Two fresh repositories each get a new installation and key pair, recorded
// in their entries, which keep their other config; a healthy install beside
// them gets no plan. The forge is not written, and progress names the IDs,
// never the seed.
func TestEnsureTraceSigningKeys_FreshInstall(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		t.Run(fmt.Sprintf("deferred=%t", deferred), func(t *testing.T) {
			fc, cfg := traceFixture(t, false, "acme/api", "acme/web", "acme/ok")
			markTraceInstalled(t, fc, "acme/ok")
			healthyTrace(t, cfg.Manifest, fc, "acme/ok", traceBlock(traceInstallA, newTraceKey(1)), newTraceKey(1))
			// Config the entry already holds, such as what
			// EnsureManagedConfigDefaults recorded, survives the edit.
			findExplicitEntry(cfg.Manifest, "acme/api").Config = mustManagedConfig(t, "roles: [triage]\n")
			progress := &progressLog{}

			plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{Manifest: cfg.Manifest, Deferred: deferred},
				newTestClientFactory(fc), progress.fn)
			require.NoError(t, err)
			assert.Equal(t, []string{"acme/api", "acme/web"}, planNames(plans))
			assertNoForgeWrites(t, fc)

			verb := "Would persist"
			if deferred {
				verb = "Persisted"
			}
			lines := progress.all()
			snapshot := manifestSnapshot(t, cfg.Manifest)
			installations := map[string]bool{}
			for _, name := range []string{"acme/api", "acme/web"} {
				p := requireTracePlan(t, plans, name)
				for _, s := range append(slices.Clone(lines), snapshot) {
					require.False(t, strings.Contains(s, p.Key.EncodeSeed()), "the seed never leaves the plan")
				}
				assert.Equal(t, reasonFreshInstall, p.Reason)
				assert.False(t, p.KeyIDVarExists, "no key ID variable was read")
				assert.Equal(t, "", p.KeyIDVar)

				tc := manifestTrace(t, cfg.Manifest, name)
				assertTraceBlockComplete(t, tc)
				assertPlanKeyIsActive(t, p, tc)
				assert.Empty(t, tc.VerificationKeys.Retained)
				installations[tc.InstallationID] = true
				assert.Equal(t, 1, countLines(lines, name+" manifest "+verb+" config.trace_context in the manifest entry: installation ID "+tc.InstallationID,
					"new active key ID "+traceKeyID(p.Key.Public())+" (fresh install)"), "progress names the IDs:\n%s", strings.Join(lines, "\n"))
			}
			assert.Len(t, lines, 2, "one line per repository:\n%s", strings.Join(lines, "\n"))
			assert.Len(t, installations, 2, "each repository gets its own installation ID")
			assert.False(t, plans["acme/api"].Key.EncodeSeed() == plans["acme/web"].Key.EncodeSeed(), "each repository gets its own key")
			assertExplicitRoles(t, findExplicitEntry(cfg.Manifest, "acme/api"), "triage")
		})
	}
}

// A repository that only a glob covers gets its own GitHub entry, copied
// from the first glob entry that covers it, and progress names that entry
// and that glob, since later edits to the glob no longer reach it; the globs
// and the repositories the run does not select are unchanged.
func TestEnsureTraceSigningKeys_GlobCoveredGetsExplicitEntry(t *testing.T) {
	glob := func(name, ref, role string) RepoEntry {
		return RepoEntry{Name: name, FullsendRef: ref, Config: mustManagedConfig(t, "roles: ["+role+"]\n")}
	}
	cases := []struct {
		name  string
		globs []RepoEntry
		// copied is the glob entry acme/api's entry is copied from.
		copied string
	}{
		{name: "one glob", globs: []RepoEntry{glob("acme/*", "v1.2.3", "triage")}, copied: "acme/*"},
		// acme/w* does not cover acme/api, and of the two that do, the
		// first is copied.
		{name: "first covering glob", globs: []RepoEntry{
			glob("acme/w*", "v0.0.1", "coder"), glob("acme/a*", "v1.2.3", "triage"), glob("acme/*", "v9.9.9", "coder"),
		}, copied: "acme/a*"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manifest{Version: 1, Defaults: testInferenceDefaults(), GitHub: &PlatformConfig{
				MintURL: "https://mint.example.com", Repos: slices.Clone(tc.globs),
			}}
			fc := newFakeClientForBatch("acme/api", "acme/web")
			progress := &progressLog{}

			plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{Manifest: m, RepoFilter: []string{"acme/api"}},
				newTestClientFactory(fc), progress.fn)
			require.NoError(t, err)
			assert.Equal(t, []string{"acme/api"}, planNames(plans))
			p := requireTracePlan(t, plans, "acme/api")
			lines := progress.all()
			requireNoSeed(t, lines, p.Key.EncodeSeed())
			assert.Equal(t, []string{"acme/api manifest Would persist config.trace_context in the new manifest entry acme/api, " +
				"copied from the glob entry " + tc.copied + ": installation ID " + manifestTrace(t, m, "acme/api").InstallationID +
				", new active key ID " + traceKeyID(p.Key.Public()) + " (fresh install)"}, lines)

			assert.Len(t, m.GitHub.Repos, len(tc.globs)+1, "one entry is carved out")
			assert.Nil(t, findExplicitEntry(m, "acme/web"), "a sibling gains no entry")
			for _, g := range tc.globs {
				assert.Nil(t, findExplicitEntry(m, g.Name).Config.Writer().TraceContext(), "the glob entry %s carries no trace_context", g.Name)
			}
			assertExplicitRoles(t, findExplicitEntry(m, tc.copied), "triage")

			carved := findExplicitEntry(m, "acme/api")
			require.NotNil(t, carved, "acme/api gets its own entry")
			assert.Equal(t, "v1.2.3", carved.FullsendRef, "the carved entry keeps the settings of the glob it names")
			assertExplicitRoles(t, carved, "triage")
			assertPlanKeyIsActive(t, p, manifestTrace(t, m, "acme/api"))
		})
	}
}

// Repositories install skips get no key and no manifest edit: those it
// skips for adoption (ExcludeRepos, matched case-insensitively) and those
// without an effective inference.auth, whose convergence fails before any
// key write.
func TestEnsureTraceSigningKeys_SkippedReposGetNoKey(t *testing.T) {
	cases := []struct {
		name    string
		exclude []string
		// noAuth leaves acme/web without inference.auth.
		noAuth bool
	}{
		{name: "excluded", exclude: []string{"ACME/web"}},
		{name: "no inference.auth", noAuth: true},
	}
	for _, tc := range cases {
		for _, glob := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/glob=%t", tc.name, glob), func(t *testing.T) {
				web := RepoEntry{Name: "acme/web"}
				if glob {
					web.Name = "acme/w*"
				}
				m := newConvergeManifest("acme/api")
				m.GitHub.Repos = append(m.GitHub.Repos, web)
				if tc.noAuth {
					m.Defaults.Inference = InferenceSettings{}
					m.GitHub.Repos[0].Inference = InferenceSettings{Auth: InferenceAuthVertexWIF}
				}
				fc := newFakeClientForBatch("acme/api", "acme/web")

				plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: m, ExcludeRepos: tc.exclude})
				require.NoError(t, err)
				assert.Equal(t, []string{"acme/api"}, planNames(plans))
				assert.Len(t, m.GitHub.Repos, 2, "no entry is carved out")
				skipped := findExplicitEntry(m, web.Name)
				require.NotNil(t, skipped)
				assert.False(t, skipped.Config.IsSet(), "the skipped repository's entry gains no config")
			})
		}
	}
}

// GitLab repositories get no key yet, with or without Rotate and with or
// without inference authentication: the preflight reads nothing for them,
// edits nothing, writes nothing and prints one line per repository.
func TestEnsureTraceSigningKeys_GitLabGetsNoKeyYet(t *testing.T) {
	for _, auth := range []bool{true, false} {
		for _, rotate := range []bool{false, true} {
			t.Run(fmt.Sprintf("auth=%t/rotate=%t", auth, rotate), func(t *testing.T) {
				fc := forge.NewFakeClient()
				reads := newReadCountingClient(fc)
				m := testGitLabManifest("acme/api")
				if auth {
					m.GitLab.Inference = InferenceSettings{Auth: InferenceAuthOpenAIAPIKey}
				}
				before := manifestSnapshot(t, m)
				progress := &progressLog{}

				plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{Manifest: m, Rotate: rotate},
					newTestClientFactory(reads), progress.fn)
				require.NoError(t, err)
				assert.Empty(t, planNames(plans))
				assert.Zero(t, reads.total(), "nothing is read for a GitLab repository")
				assert.Equal(t, before, manifestSnapshot(t, m))
				assertNoForgeWrites(t, fc)
				lines := progress.all()
				assert.Len(t, lines, 1, "one line:\n%s", strings.Join(lines, "\n"))
				assert.Equal(t, 1, countLines(lines, "acme/api", "not provisioned", "GitLab"), "the line says GitLab keys are not provisioned:\n%s", strings.Join(lines, "\n"))
			})
		}
	}
}

// A healthy install, whose secret exists and whose key ID variable names
// the active key, plans nothing and prints nothing, also from a read-only
// manifest.
func TestEnsureTraceSigningKeys_HealthyPlansNothing(t *testing.T) {
	a, r := newTraceKey(1), newTraceKey(2)
	for name, path := range map[string]string{"local": "", "https": "https://example.com/repos.yaml"} {
		t.Run(name, func(t *testing.T) {
			fc, cfg := traceFixture(t, true, "acme/api")
			healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, a, r), a)
			before := manifestSnapshot(t, cfg.Manifest)
			progress := &progressLog{}

			plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{Manifest: cfg.Manifest, ManifestPath: path},
				newTestClientFactory(fc), progress.fn)
			require.NoError(t, err)
			assert.Empty(t, planNames(plans))
			assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest))
			assertNoForgeWrites(t, fc)
			assert.Empty(t, progress.all())
		})
	}
}

// Every unhealthy state gets a new active key as "recovery", which wins
// over Rotate, and a healthy install gets one as "rotation". The
// installation ID is kept, the previous active key is retained after the
// keys retained before, and the plan carries FULLSEND_TRACE_SIGNING_KEY_ID
// as the preflight read it.
func TestEnsureTraceSigningKeys_RegeneratesKeepingInstallation(t *testing.T) {
	a, r := newTraceKey(1), newTraceKey(2)
	keyed := func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
		setManifestTrace(t, m, "acme/api", traceBlock(traceInstallA, a, r))
		setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a, r))
	}
	cases := []struct {
		name string
		// installed puts a fullsend workflow on the default branch.
		installed bool
		rotate    bool
		reason    traceKeyReason
		// keyIDVar is the variable's value in the fixture; "" is absent.
		keyIDVar string
		setup    func(t *testing.T, m *Manifest, fc *forge.FakeClient)
	}{
		{name: "variable absent", installed: true, reason: reasonRecovery, setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			keyed(t, m, fc)
			setTraceSecret(fc, "acme/api")
		}},
		{name: "variable and secret absent", installed: true, reason: reasonRecovery, setup: keyed},
		{name: "secret absent", installed: true, reason: reasonRecovery, keyIDVar: a.id(), setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			keyed(t, m, fc)
			setKeyIDVar(fc, "acme/api", a.id())
		}},
		{name: "variable names a retained key", installed: true, reason: reasonRecovery, keyIDVar: r.id(), setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			// A run recorded a as active, then stopped before its seed
			// write: the secret, the variable and the committed config
			// still hold r.
			setManifestTrace(t, m, "acme/api", traceBlock(traceInstallA, a, r))
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, r))
			setTraceSecret(fc, "acme/api")
			setKeyIDVar(fc, "acme/api", r.id())
		}},
		// After repos uninstall --uninstall-only on GitHub: the entry and
		// the committed config keep the block, and nothing else remains.
		{name: "after --uninstall-only", reason: reasonRecovery, setup: keyed},
		{name: "rotate a healthy install", installed: true, rotate: true, reason: reasonRotation, keyIDVar: a.id(), setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, a, r), a)
		}},
	}
	for _, tc := range cases {
		rotations := []bool{tc.rotate}
		if tc.reason == reasonRecovery {
			rotations = []bool{false, true}
		}
		for _, rotate := range rotations {
			t.Run(fmt.Sprintf("%s/rotate=%t", tc.name, rotate), func(t *testing.T) {
				fc, cfg := traceFixture(t, tc.installed, "acme/api")
				tc.setup(t, cfg.Manifest, fc)

				plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rotate: rotate})
				require.NoError(t, err)
				p := requireTracePlan(t, plans, "acme/api")
				assert.Equal(t, tc.reason, p.Reason)
				assert.Equal(t, tc.keyIDVar, p.KeyIDVar)
				assert.Equal(t, tc.keyIDVar != "", p.KeyIDVarExists)

				got := manifestTrace(t, cfg.Manifest, "acme/api")
				assert.Equal(t, traceInstallA, got.InstallationID, "the installation ID is kept")
				assertPlanKeyIsActive(t, p, got)
				assert.Equal(t, []string{r.pubB64(), a.pubB64()}, got.VerificationKeys.Retained,
					"the previous active key is retained after the earlier retained key")
				assertNoForgeWrites(t, fc)
			})
		}
	}
}

// An existing install without trace_context gets keys only when the
// operator rotates; the first rotation starts its installation.
func TestEnsureTraceSigningKeys_ExistingInstallKeysOnlyWithRotate(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		t.Run(fmt.Sprintf("rotate=%t", rotate), func(t *testing.T) {
			fc, cfg := traceFixture(t, true, "acme/api")
			before := manifestSnapshot(t, cfg.Manifest)

			plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rotate: rotate})
			require.NoError(t, err)
			assertNoForgeWrites(t, fc)
			if !rotate {
				assert.Empty(t, planNames(plans))
				assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest))
				assert.False(t, findExplicitEntry(cfg.Manifest, "acme/api").Config.IsSet(), "the manifest entry gains no config")
				return
			}
			p := requireTracePlan(t, plans, "acme/api")
			assert.Equal(t, reasonRotation, p.Reason)
			tc := manifestTrace(t, cfg.Manifest, "acme/api")
			assertTraceBlockComplete(t, tc)
			assertPlanKeyIsActive(t, p, tc)
			assert.Empty(t, tc.VerificationKeys.Retained)
		})
	}
}

// From a read-only manifest (an HTTPS source, a remotely loaded manifest or
// a local file without write permission) a fresh install without Rotate
// goes ahead without keys and names the run that provisions them. Any
// other key write needs the repos.yaml edit, so the run stops before any
// edit, prints nothing and shows no generated value.
func TestEnsureTraceSigningKeys_ReadOnlyManifest(t *testing.T) {
	a, r := newTraceKey(1), newTraceKey(2)
	const url = "https://example.com/repos.yaml"
	healthy := func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
		healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, a, r), a)
	}
	cases := []struct {
		name string
		// installed puts a fullsend workflow on the default branch.
		installed bool
		// path is the manifest path; readOnlyFile is a local file without
		// write permission.
		path   string
		remote bool
		rotate bool
		setup  func(t *testing.T, m *Manifest, fc *forge.FakeClient)
		// skipped is a fresh install that goes ahead without keys.
		skipped bool
	}{
		{name: "fresh install from https", path: url, skipped: true},
		{name: "fresh install from a remotely loaded manifest", remote: true, skipped: true},
		{name: "fresh install from a file without write permission", path: readOnlyFile, skipped: true},
		{name: "recovery", installed: true, path: url, setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthy(t, m, fc)
			delete(fc.VariableValues, "acme/api/"+forge.VarTraceSigningKeyID)
			delete(fc.VariablesExist, "acme/api/"+forge.VarTraceSigningKeyID)
		}},
		{name: "rotation", installed: true, path: url, rotate: true, setup: healthy},
		{name: "rotation on a remotely loaded manifest", installed: true, remote: true, rotate: true, setup: healthy},
		{name: "fresh install with Rotate", path: url, rotate: true},
		{name: "re-enrollment after a full uninstall with Rotate", path: url, rotate: true, setup: func(_ *testing.T, _ *Manifest, fc *forge.FakeClient) {
			// The committed block belongs to the ended installation.
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc, cfg := traceFixture(t, tc.installed, "acme/api")
			if tc.setup != nil {
				tc.setup(t, cfg.Manifest, fc)
			}
			cfg.Manifest.sourceRemote = tc.remote
			before := manifestSnapshot(t, cfg.Manifest)
			path := tc.path
			if path == readOnlyFile {
				path = filepath.Join(t.TempDir(), "repos.yaml")
				require.NoError(t, os.WriteFile(path, []byte(before), 0o444))
			}
			progress := &progressLog{}

			plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{
				Manifest: cfg.Manifest, ManifestPath: path, Rotate: tc.rotate,
			}, newTestClientFactory(fc), progress.fn)
			assert.Empty(t, planNames(plans))
			assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest), "the entry is not edited")
			assertNoForgeWrites(t, fc)
			lines := progress.all()
			if tc.skipped {
				require.NoError(t, err)
				assert.Len(t, lines, 1, "one notice:\n%s", strings.Join(lines, "\n"))
				assert.Equal(t, 1, countLines(lines, "acme/api manifest Trace signing keys not provisioned: repos.yaml is read-only; "+
					"a later repos install acme/api --rotate-trace-signing-key with a writable manifest provisions them"),
					"the notice names the run that provisions the keys:\n%s", strings.Join(lines, "\n"))
				return
			}
			require.ErrorIs(t, err, ErrTraceManifestReadOnly)
			assertNoTraceValues(t, err.Error(), a, r)
			assert.Contains(t, err.Error(), "acme/api")
			assert.NotRegexp(t, `[0-9a-f]{16}`, err.Error(), "the error shows no installation ID and no key ID")
			assert.Empty(t, lines, "a refusal reports nothing")
		})
	}
}

// readOnlyFile asks a test for a local manifest file without write
// permission.
const readOnlyFile = "read-only file"

// A failed or invalid read refuses the run before any edit: the error
// names the repository and what was read, the fresh repository beside it
// gets no plan, and the GitLab repository beside them gets no notice.
func TestEnsureTraceSigningKeys_ReadErrorsRefuseBeforeAnyEdit(t *testing.T) {
	a := newTraceKey(1)
	failed := errors.New("the forge failed")
	keyed := func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
		setManifestTrace(t, m, "acme/api", traceBlock(traceInstallA, a))
		setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a))
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, m *Manifest, fc *forge.FakeClient)
		want  string
	}{
		{name: "committed config read fails", want: "acme/api: reading " + preset.OverlayPath + ": the forge failed", setup: func(_ *testing.T, _ *Manifest, fc *forge.FakeClient) {
			fc.GetFileContentErrors = map[string]error{"acme/api/" + preset.OverlayPath: failed}
		}},
		{name: "committed block invalid", want: "acme/api: committed " + preset.OverlayPath + ": trace_context.installation_id", setup: func(_ *testing.T, _ *Manifest, fc *forge.FakeClient) {
			setInstalledTrace(fc, "acme/api", traceBlock(strings.Repeat("0", 32), a))
		}},
		{name: "key ID variable read fails", want: "acme/api: reading " + forge.VarTraceSigningKeyID + ": the forge failed", setup: func(_ *testing.T, _ *Manifest, fc *forge.FakeClient) {
			fc.Errors = map[string]error{"GetRepoVariable": failed}
		}},
		{name: "workflow read fails", want: "acme/api: checking workflow file: the forge failed", setup: func(_ *testing.T, _ *Manifest, fc *forge.FakeClient) {
			fc.GetFileContentErrors = map[string]error{"acme/api/.github/workflows/fullsend.yaml": failed}
		}},
		{name: "secret read fails", want: "acme/api: checking secret " + forge.SecretTraceSigningKey + ": the forge failed", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			keyed(t, m, fc)
			fc.Errors = map[string]error{"RepoSecretExists": failed}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc, cfg := traceFixture(t, false, "acme/api", "acme/web")
			cfg.Manifest.GitLab = testGitLabManifest("acme/lab").GitLab
			tc.setup(t, cfg.Manifest, fc)

			err := refuseTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest}, []string{strings.Repeat("0", 32)}, a)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// A FULLSEND_TRACE_SIGNING_KEY_ID that is not 16 lowercase hex characters
// stops the run before any edit with its own error, which says to delete
// the variable and never shows its value. It wins over the refusal of a
// keyless entry beside a committed block.
func TestEnsureTraceSigningKeys_MalformedKeyIDVariableRefused(t *testing.T) {
	a := newTraceKey(1)
	malformed := []struct{ name, value string }{
		{"empty", ""},
		{"uppercase", "0123456789ABCDEF"},
		{"15 characters", "0123456789abcde"},
		{"trailing newline", "0123456789abcdef\n"},
		{"base64 seed", newTraceKey(9).seedB64()},
	}
	type row struct {
		name, value string
		// keyless leaves acme/api's entry without trace_context beside a
		// committed block.
		keyless bool
	}
	var rows []row
	for _, v := range malformed {
		rows = append(rows, row{name: v.name, value: v.value})
	}
	rows = append(rows, row{name: "keyless entry beside a committed block", value: malformed[1].value, keyless: true})
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			fc, cfg := traceFixture(t, false, "acme/api", "acme/web")
			markTraceInstalled(t, fc, "acme/api")
			if tc.keyless {
				setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a))
			} else {
				healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, a), a)
			}
			setKeyIDVar(fc, "acme/api", tc.value)

			err := refuseTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest}, []string{strings.TrimSpace(tc.value)}, a)
			require.ErrorIs(t, err, ErrTraceKeyIDInvalid)
			msg := err.Error()
			assert.False(t, errors.Is(err, ErrTraceManifestStale), "a malformed value is not a repos.yaml mismatch")
			assert.Contains(t, msg, "acme/api")
			assert.Contains(t, msg, forge.VarTraceSigningKeyID)
			assert.Contains(t, msg, "delete the repository variable")
			assert.NotContains(t, msg, "restore the entry's trace_context")
		})
	}
}

// refusalRemedy ends every ErrTraceManifestStale refusal.
const refusalRemedy = "; repos.yaml and the trace keys were not changed: " +
	"run install with the repos.yaml the last install wrote, or restore the entry's trace_context from it"

// refusal is the ErrTraceManifestStale refusal of name for reasons.
func refusal(name string, reasons ...string) string {
	return "repos.yaml does not match the repository's trace keys for " + name + ": " + strings.Join(reasons, "; ") + refusalRemedy
}

// An entry that contradicts the committed trace_context, the key ID
// variable or another entry stops the run before any edit, with or without
// Rotate: the error names the repository, every reason with key IDs only
// and the restore remedy, and the fresh repository beside it gets no plan.
// The committed block of an ended installation is no reason, but the other
// checks still apply.
func TestEnsureTraceSigningKeys_ContradictingStateRefused(t *testing.T) {
	a, b, n, x, z := newTraceKey(1), newTraceKey(2), newTraceKey(4), newTraceKey(5), newTraceKey(6)
	r1, r2 := newTraceKey(7), newTraceKey(8)
	twoMissing := []string{r1.id(), r2.id()}
	slices.Sort(twoMissing)
	const differs = "the committed trace_context.installation_id differs from the repos.yaml entry's"
	trusts := func(ids string) string {
		return "the committed " + preset.OverlayPath + " trusts " + ids + ", which the repos.yaml entry does not list " +
			"(if you did not add the key, the committed file may have been edited: revert that change, and do not add the key to repos.yaml)"
	}
	unlisted := forge.VarTraceSigningKeyID + " names a key the repos.yaml entry does not list"
	usedBy := func(other string) string {
		return "its trace_context.installation_id is also used by the repos.yaml entry " + other + "; every repository needs its own"
	}
	// replaced makes acme/api's entry installation B, which replaced
	// installation A, whose block is still committed; the seed and the
	// variable are B's.
	replaced := func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
		setManifestTrace(t, m, "acme/api", traceBlock(traceInstallB, b))
		setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a))
		setTraceSecret(fc, "acme/api")
		setKeyIDVar(fc, "acme/api", b.id())
	}
	cases := []struct {
		name string
		// noWorkflow leaves acme/api without a fullsend workflow on its
		// default branch, so a committed block of another installation is
		// an ended one.
		noWorkflow bool
		setup      func(t *testing.T, m *Manifest, fc *forge.FakeClient)
		// reasons are acme/api's, in order; web are acme/web's when the
		// run refuses it too.
		reasons, web []string
		// hidden is the variable's value, which the error never shows.
		hidden string
	}{
		{name: "a: committed installation_id differs", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, a), a)
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallB, a))
		}, reasons: []string{differs}},
		{name: "b: committed active key missing", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			// An older copy of repos.yaml that predates the last rotation.
			setManifestTrace(t, m, "acme/api", traceBlock(traceInstallA, a))
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, n, a))
			setTraceSecret(fc, "acme/api")
		}, reasons: []string{trusts("key ID " + n.id())}},
		{name: "c: one committed retained key missing", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, a), a)
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a, x))
		}, reasons: []string{trusts("key ID " + x.id())}},
		{name: "d: two committed keys missing", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, a), a)
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a, r1, r2))
		}, reasons: []string{trusts("key IDs " + strings.Join(twoMissing, ", "))}},
		{name: "e: committed block beside a keyless entry", setup: func(_ *testing.T, _ *Manifest, fc *forge.FakeClient) {
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a))
		}, reasons: []string{"the committed " + preset.OverlayPath + " has trace_context and the repos.yaml entry has none " +
			"(install enrolls the repository again as a new installation only once no fullsend workflow, " +
			forge.SecretTraceSigningKey + " or " + forge.VarTraceSigningKeyID + " remains, as after a finished repos uninstall)"}},
		{name: "f: key ID variable names a key the entry does not list", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, a), a)
			setKeyIDVar(fc, "acme/api", z.id())
		}, reasons: []string{unlisted}, hidden: z.id()},
		{name: "g: key ID variable beside a keyless entry", setup: func(_ *testing.T, _ *Manifest, fc *forge.FakeClient) {
			setKeyIDVar(fc, "acme/api", z.id())
		}, reasons: []string{forge.VarTraceSigningKeyID + " is set and the repos.yaml entry has no trace_context"}, hidden: z.id()},
		{name: "h: an unselected GitLab entry uses the installation ID", setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			healthyTrace(t, m, fc, "acme/api", traceBlock(traceInstallA, a), a)
			m.GitLab = &PlatformConfig{URL: "https://gitlab.example.com", Repos: []RepoEntry{
				{Name: "acme/lab", Config: mustManagedConfig(t, traceBlock(traceInstallA, b))},
			}}
		}, reasons: []string{usedBy("acme/lab")}},
		{name: "ended block: fullsend workflow on the default branch", setup: replaced,
			reasons: []string{differs, trusts("key ID " + a.id())}},
		{name: "ended block: key ID variable names a key the entry does not list", noWorkflow: true, setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			replaced(t, m, fc)
			setKeyIDVar(fc, "acme/api", a.id())
		}, reasons: []string{unlisted}, hidden: a.id()},
		{name: "ended block: another entry uses the installation ID", noWorkflow: true, setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			replaced(t, m, fc)
			setManifestTrace(t, m, "acme/web", traceBlock(traceInstallB, z))
		}, reasons: []string{usedBy("acme/web")}, web: []string{usedBy("acme/api")}},
		// After repos uninstall --uninstall-only: no workflow, but the
		// committed block is the entry's own installation, so it is compared.
		{name: "no workflow, same installation: committed key missing", noWorkflow: true, setup: func(t *testing.T, m *Manifest, fc *forge.FakeClient) {
			setManifestTrace(t, m, "acme/api", traceBlock(traceInstallA, a))
			setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, a, x))
		}, reasons: []string{trusts("key ID " + x.id())}},
	}
	for _, tc := range cases {
		for _, rotate := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/rotate=%t", tc.name, rotate), func(t *testing.T) {
				fc, cfg := traceFixture(t, false, "acme/api", "acme/web")
				if !tc.noWorkflow {
					markTraceInstalled(t, fc, "acme/api")
				}
				tc.setup(t, cfg.Manifest, fc)

				err := refuseTrace(t, fc, TraceKeysConfig{
					Manifest: cfg.Manifest, RepoFilter: []string{"acme/api", "acme/web"}, Rotate: rotate,
				}, []string{tc.hidden}, a, b, n, x, z, r1, r2)
				require.ErrorIs(t, err, ErrTraceManifestStale)
				want := refusal("acme/api", tc.reasons...)
				if tc.web != nil {
					want += "\n" + refusal("acme/web", tc.web...)
				}
				assert.Equal(t, want, err.Error())
			})
		}
	}
}

// traceInstallationUser skips the repository's own entry whatever the case
// of its name, as forges match repository names, and names another entry
// that uses the installation ID. Selection resolves an explicit entry under
// its own spelling, so only a direct call can pass another one.
func TestTraceInstallationUser_OwnEntryInAnyCase(t *testing.T) {
	m := newConvergeManifest("Acme/API", "acme/web")
	setManifestTrace(t, m, "Acme/API", traceBlock(traceInstallA, newTraceKey(1)))
	installation, err := manifestTrace(t, m, "Acme/API").Installation()
	require.NoError(t, err)
	assert.Equal(t, "", traceInstallationUser(m, "acme/api", installation), "the repository's own entry")

	setManifestTrace(t, m, "acme/web", traceBlock(traceInstallA, newTraceKey(2)))
	assert.Equal(t, "acme/web", traceInstallationUser(m, "acme/api", installation), "another entry")
}
