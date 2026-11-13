package repos

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/fullsend-ai/fullsend/internal/telemetry/tracecontext"
)

// traceFailClient fails one trace key step with an error that reflects
// echo, as a forge that echoes the value it was sent: "reread" (reading
// FULLSEND_TRACE_SIGNING_KEY_ID), "delete" (deleting it), "seed" (writing
// FULLSEND_TRACE_SIGNING_KEY) or "variable" (setting
// FULLSEND_TRACE_SIGNING_KEY_ID); "" fails none. It counts the reads of
// FULLSEND_TRACE_SIGNING_KEY_ID and the scaffold commits. Embedding the
// concrete fake keeps its optional interfaces visible. Tests plan with the
// plain fake and wrap it only for convergence, so no preflight read fails.
type traceFailClient struct {
	*forge.FakeClient
	step string
	echo string

	mu      sync.Mutex
	rereads int
	commits int
}

func (c *traceFailClient) fail(step string) error {
	if c.step != step {
		return nil
	}
	return fmt.Errorf("forge rejected %s", c.echo)
}

func (c *traceFailClient) GetRepoVariable(ctx context.Context, owner, repo, name string) (string, bool, error) {
	if name == forge.VarTraceSigningKeyID {
		c.mu.Lock()
		c.rereads++
		c.mu.Unlock()
		if err := c.fail("reread"); err != nil {
			return "", false, err
		}
	}
	return c.FakeClient.GetRepoVariable(ctx, owner, repo, name)
}

func (c *traceFailClient) DeleteRepoVariable(ctx context.Context, owner, repo, name string) error {
	if name == forge.VarTraceSigningKeyID {
		if err := c.fail("delete"); err != nil {
			return err
		}
	}
	return c.FakeClient.DeleteRepoVariable(ctx, owner, repo, name)
}

func (c *traceFailClient) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	if name == forge.SecretTraceSigningKey {
		if err := c.fail("seed"); err != nil {
			return err
		}
	}
	return c.FakeClient.CreateRepoSecret(ctx, owner, repo, name, value)
}

func (c *traceFailClient) CreateOrUpdateRepoVariable(ctx context.Context, owner, repo, name, value string) error {
	if name == forge.VarTraceSigningKeyID {
		if err := c.fail("variable"); err != nil {
			return err
		}
	}
	return c.FakeClient.CreateOrUpdateRepoVariable(ctx, owner, repo, name, value)
}

// keyIDReads returns how many times convergence read
// FULLSEND_TRACE_SIGNING_KEY_ID.
func (c *traceFailClient) keyIDReads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rereads
}

// committed returns how many scaffold commits convergence made.
func (c *traceFailClient) committed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commits
}

// commit counts each scaffold commit and returns err without landing it, as
// a pull request that is not merged.
func (c *traceFailClient) commit(err error) ScaffoldCommitFunc {
	return func(context.Context, string, string, []forge.TreeFile, bool, bool) error {
		c.mu.Lock()
		c.commits++
		c.mu.Unlock()
		return err
	}
}

// land counts each scaffold commit and lands its files on the default
// branch, so the committed config can be read back.
func (c *traceFailClient) land() ScaffoldCommitFunc {
	return func(ctx context.Context, owner, repo string, files []forge.TreeFile, _, _ bool) error {
		c.mu.Lock()
		c.commits++
		c.mu.Unlock()
		_, err := c.CommitFiles(ctx, owner, repo, "install", files)
		return err
	}
}

func convergeTrace(t *testing.T, client *traceFailClient, cfg ConvergeConfig, commit ScaffoldCommitFunc, progress *progressLog) *ConvergeBatchResult {
	t.Helper()
	result, err := Converge(context.Background(), cfg, newTestClientFactory(client), commit, progress.fn)
	require.NoError(t, err)
	return result
}

// convergeOK converges and requires every repository to succeed, after
// checking that nothing reported shows seed. It returns the result and
// everything reported.
func convergeOK(t *testing.T, client *traceFailClient, cfg ConvergeConfig, commit ScaffoldCommitFunc, seed string) (*ConvergeBatchResult, []string) {
	t.Helper()
	progress := &progressLog{}
	result := convergeTrace(t, client, cfg, commit, progress)
	reported := traceReported(result, progress)
	requireNoSeed(t, reported, seed)
	require.Zero(t, len(result.Failed()), "convergence failed:\n%s", strings.Join(reported, "\n"))
	return result, reported
}

// traceSecretWrites returns the values written to FULLSEND_TRACE_SIGNING_KEY,
// in order. They are seeds: compare them, never print them.
func traceSecretWrites(fc *forge.FakeClient) []string {
	var out []string
	for _, s := range fc.CreatedSecrets {
		if s.Name == forge.SecretTraceSigningKey {
			out = append(out, s.Value)
		}
	}
	return out
}

// traceVarRecords returns how many records, such as fc.DeletedVariables or
// fc.Variables, name FULLSEND_TRACE_SIGNING_KEY_ID.
func traceVarRecords(records []forge.VariableRecord) int {
	n := 0
	for _, v := range records {
		if v.Name == forge.VarTraceSigningKeyID {
			n++
		}
	}
	return n
}

// keyIDVar returns acme/api's FULLSEND_TRACE_SIGNING_KEY_ID.
func keyIDVar(fc *forge.FakeClient) (string, bool) {
	v, ok := fc.VariableValues["acme/api/"+forge.VarTraceSigningKeyID]
	return v, ok
}

// traceReported is everything convergence reported: errors, actions and
// progress lines.
func traceReported(result *ConvergeBatchResult, progress *progressLog) []string {
	var out []string
	for _, r := range result.Results {
		if r.Error != nil {
			out = append(out, r.Error.Error())
		}
		for _, a := range r.Actions {
			out = append(out, a.Component+" "+a.Action+" "+a.Detail)
		}
	}
	return append(out, progress.all()...)
}

// requireNoSeed fails, without printing anything reported, when a report
// shows seed.
func requireNoSeed(t *testing.T, reported []string, seed string) {
	t.Helper()
	for _, s := range reported {
		require.False(t, strings.Contains(s, seed), "nothing reported shows the seed")
	}
}

// traceActions returns the actions of the trace key step, as "component
// action detail".
func traceActions(result *ConvergeBatchResult) []string {
	var out []string
	for _, r := range result.Results {
		for _, a := range r.Actions {
			if a.Action == "orphan" {
				continue
			}
			if a.Component == "secret:"+forge.SecretTraceSigningKey || a.Component == "var:"+forge.VarTraceSigningKeyID {
				out = append(out, a.Component+" "+a.Action+" "+a.Detail)
			}
		}
	}
	return out
}

// assertTraceConverged checks the forge against tc: the key ID variable
// names the active key and the last seed written expands to it.
func assertTraceConverged(t *testing.T, fc *forge.FakeClient, tc *config.TraceContextConfig) {
	t.Helper()
	active, err := base64.StdEncoding.DecodeString(tc.VerificationKeys.Active)
	require.NoError(t, err)
	v, _ := keyIDVar(fc)
	assert.Equal(t, traceKeyID(active), v, "the key ID variable names the active key")
	writes := traceSecretWrites(fc)
	require.True(t, len(writes) > 0, "no seed was written")
	seed, err := base64.StdEncoding.DecodeString(writes[len(writes)-1])
	require.NoError(t, err)
	require.True(t, len(seed) == ed25519.SeedSize, "the secret holds a 32-byte seed")
	assert.True(t, bytes.Equal(active, ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)), "the secret holds the active key's seed")
}

// traceEligible returns tc's active and retained keys.
func traceEligible(tc *config.TraceContextConfig) []string {
	return append([]string{tc.VerificationKeys.Active}, tc.VerificationKeys.Retained...)
}

// A fresh install's key is written before Install, deleting the variable
// first. A dry run reports both writes and reads, deletes, writes and
// commits nothing. The real run deletes FULLSEND_TRACE_SIGNING_KEY_ID, writes
// the plan's seed (Install reuses the Vertex secrets, so it writes no other
// secret), sets the variable to the plan's key ID and commits. A second run
// plans nothing and, with the scaffold landed, is already current. A plan
// for another repository writes nothing for this one.
func TestTraceKeys_FreshThenReinstall(t *testing.T) {
	fixture := func(t *testing.T) (*forge.FakeClient, ConvergeConfig) {
		fc := newFakeClientForBatch("acme/api")
		fc.Secrets["acme/api/"+forge.SecretGCPProjectID] = true
		fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider] = true
		fc.VariableValues["acme/api/"+forge.VarGCPRegion] = "us-central1"
		return fc, withoutInferenceInputs(convergeCfgWithDefaults(newConvergeManifest("acme/api")))
	}

	t.Run("plan for this repository", func(t *testing.T) {
		fc, cfg := fixture(t)
		plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest})
		require.NoError(t, err)
		p := requireTracePlan(t, plans, "acme/api")
		seed := p.Key.EncodeSeed()
		id := traceKeyID(p.Key.Public())
		tc := manifestTrace(t, cfg.Manifest, "acme/api")
		cfg.TraceKeyPlans = plans

		dry := cfg
		dry.DryRun = true
		client := &traceFailClient{FakeClient: fc}
		result, reported := convergeOK(t, client, dry, client.commit(nil), seed)
		assert.Equal(t, wouldSetTrace(id), traceActions(result))
		assert.Equal(t, 1, countLines(reported, "acme/api dry-run Would set secret "+forge.SecretTraceSigningKey), strings.Join(reported, "\n"))
		assert.Equal(t, 1, countLines(reported, "acme/api dry-run Would set variable "+forge.VarTraceSigningKeyID), strings.Join(reported, "\n"))
		assert.Zero(t, client.keyIDReads(), "a dry run does not read the variable again")
		assert.Zero(t, client.committed(), "a dry run commits nothing")
		assertNoForgeWrites(t, fc)

		client = &traceFailClient{FakeClient: fc}
		_, reported = convergeOK(t, client, cfg, client.land(), seed)
		assert.Equal(t, 1, traceVarRecords(fc.DeletedVariables), "the variable is deleted before the seed write")
		writes := traceSecretWrites(fc)
		require.True(t, len(writes) == 1, "one seed write")
		assert.True(t, writes[0] == seed, "the secret holds the plan's seed")
		v, ok := keyIDVar(fc)
		assert.True(t, ok, "the variable is set")
		assert.Equal(t, id, v, "the variable names the plan's key")
		for _, s := range fc.CreatedSecrets {
			assert.False(t, slices.Contains(managedInferenceSecrets(), s.Name), "%s is reused, not written", s.Name)
		}
		assert.Equal(t, 1, client.committed())
		assert.Equal(t, 1, countLines(reported, "acme/api sync Set secret "+forge.SecretTraceSigningKey), strings.Join(reported, "\n"))
		assert.Equal(t, 1, countLines(reported, "acme/api sync Set variable "+forge.VarTraceSigningKeyID), strings.Join(reported, "\n"))
		assertTraceConverged(t, fc, tc)

		again, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest})
		require.NoError(t, err)
		assert.Empty(t, planNames(again), "the install is healthy")
		cfg.TraceKeyPlans = again
		client = &traceFailClient{FakeClient: fc}
		progress := &progressLog{}
		result = convergeTrace(t, client, cfg, client.land(), progress)
		reported = traceReported(result, progress)
		requireNoSeed(t, reported, seed)
		require.Equal(t, 1, len(result.Results))
		assert.True(t, result.Results[0].AlreadyCurrent, "the second run is already current:\n%s", strings.Join(reported, "\n"))
		assert.Zero(t, client.committed(), "the second run commits nothing")
		assert.Equal(t, 1, traceVarRecords(fc.DeletedVariables), "the second run deletes no variable")
		assert.Equal(t, 1, len(traceSecretWrites(fc)), "the second run writes no seed")
		assert.Equal(t, tc.InstallationID, manifestTrace(t, cfg.Manifest, "acme/api").InstallationID)
	})

	t.Run("plan for another repository only", func(t *testing.T) {
		fc, cfg := fixture(t)
		other, err := tracecontext.ParseSigningSeed(newTraceKey(9).seedB64())
		require.NoError(t, err)
		cfg.TraceKeyPlans = map[string]*TraceKeyPlan{"acme/other": {Key: other, Reason: reasonFreshInstall}}
		client := &traceFailClient{FakeClient: fc}
		result, _ := convergeOK(t, client, cfg, client.land(), newTraceKey(9).seedB64())
		assert.Empty(t, traceActions(result))
		assert.Zero(t, client.keyIDReads())
		assert.Zero(t, traceVarRecords(fc.DeletedVariables))
		assert.Zero(t, len(traceSecretWrites(fc)), "no seed write")
		_, ok := keyIDVar(fc)
		assert.False(t, ok, "no variable")
		assert.Equal(t, 1, client.committed(), "the install still commits")
	})
}

// wouldSetTrace is what a dry run reports for a plan with key ID id.
func wouldSetTrace(id string) []string {
	return []string{
		"secret:" + forge.SecretTraceSigningKey + " update would set " + forge.SecretTraceSigningKey + " (key ID " + id + ")",
		"var:" + forge.VarTraceSigningKeyID + " update would set " + forge.VarTraceSigningKeyID + " = " + id,
	}
}

// traceStepFixture is a repository whose key the preflight plans, with
// the Rotate flag that plans it.
type traceStepFixture struct {
	name  string
	setup func(t *testing.T) (fc *forge.FakeClient, cfg ConvergeConfig, rotate bool)
	// reenroll reports a re-enrollment after a full uninstall of
	// installation traceInstallA, whose block stays committed until the
	// install's commit lands.
	reenroll bool
}

// A failure at any trace key step, or at the commit, fails the repository
// before its commit and leaves the forge as that step leaves it: a failed
// read deletes nothing, a failed delete writes no seed, and after the delete
// FULLSEND_TRACE_SIGNING_KEY_ID stays absent until the new key's seed is
// written and the variable set. Nothing reported shows the seed, which the
// forge's error echoes. After a failed or unmerged commit, which leaves the
// key written, the next preflight plans nothing; while a re-enrollment's
// commit has not landed, it names the ended installation once. One plain
// retry converges, keeps the installation and every trusted key, and
// delivers the entry's block; a third preflight plans nothing and names no
// ended installation.
func TestTraceKeys_WriteStepFailureThenRetry(t *testing.T) {
	old := newTraceKey(1)
	fixtures := []traceStepFixture{
		{name: "fresh", setup: func(t *testing.T) (*forge.FakeClient, ConvergeConfig, bool) {
			fc, cfg := traceFixture(t, false, "acme/api")
			return fc, cfg, false
		}},
		{name: "rotation", setup: func(t *testing.T) (*forge.FakeClient, ConvergeConfig, bool) {
			fc, cfg := traceFixture(t, true, "acme/api")
			healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, old), old)
			return fc, cfg, true
		}},
		{name: "re-enrollment", reenroll: true, setup: func(t *testing.T) (*forge.FakeClient, ConvergeConfig, bool) {
			fc, cfg := githubFullUninstall(t, old)
			return fc, cfg, false
		}},
	}
	// wants is what the error of each failing step names.
	wants := map[string]string{
		"reread":   forge.SecretTraceSigningKey + " not set: reading " + forge.VarTraceSigningKeyID + ": forge rejected [redacted]",
		"delete":   forge.SecretTraceSigningKey + " not set: deleting " + forge.VarTraceSigningKeyID + ": forge rejected [redacted]",
		"seed":     "failed to set " + forge.SecretTraceSigningKey + ": forge rejected [redacted]; " + forge.SecretTraceSigningKey + " may now hold a key that only the repos.yaml this run wrote lists: keep and commit that repos.yaml, and run repos install again with it",
		"variable": "failed to set " + forge.VarTraceSigningKeyID + ": forge rejected [redacted]; " + forge.SecretTraceSigningKey + " now holds a key that only the repos.yaml this run wrote lists: keep and commit that repos.yaml, and run repos install again with it",
		"commit":   "commit failed",
	}
	for _, fx := range fixtures {
		for _, step := range []string{"none", "reread", "delete", "seed", "variable", "commit"} {
			t.Run(fx.name+"/"+step, func(t *testing.T) {
				fc, cfg, rotate := fx.setup(t)
				first, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rotate: rotate})
				require.NoError(t, err)
				p := requireTracePlan(t, first, "acme/api")
				seed, id := p.Key.EncodeSeed(), traceKeyID(p.Key.Public())
				before := manifestTrace(t, cfg.Manifest, "acme/api")
				installation, eligible := before.InstallationID, traceEligible(before)
				oldVar, oldVarSet := keyIDVar(fc)
				cfg.TraceKeyPlans = first

				if rotate {
					// The established path writes nothing in a dry run.
					dry := cfg
					dry.DryRun = true
					client := &traceFailClient{FakeClient: fc}
					result, _ := convergeOK(t, client, dry, client.land(), seed)
					assert.Equal(t, wouldSetTrace(id), traceActions(result))
					assert.Zero(t, client.keyIDReads())
					assert.Zero(t, traceVarRecords(fc.DeletedVariables))
					assert.Zero(t, len(traceSecretWrites(fc)), "no seed write")
					assert.Zero(t, client.committed())
					v, _ := keyIDVar(fc)
					assert.Equal(t, old.id(), v)
				}

				var commitErr error
				if step == "commit" {
					commitErr = errors.New("commit failed")
				}
				client := &traceFailClient{FakeClient: fc, step: step, echo: seed}
				progress := &progressLog{}
				result := convergeTrace(t, client, cfg, client.commit(commitErr), progress)
				reported := traceReported(result, progress)
				requireNoSeed(t, reported, seed)
				v, set := keyIDVar(fc)
				switch step {
				case "reread", "delete":
					if step == "reread" {
						assert.Zero(t, traceVarRecords(fc.DeletedVariables), "nothing is deleted")
					}
					assert.Zero(t, len(traceSecretWrites(fc)), "no seed write")
					assert.Equal(t, oldVarSet, set, "the variable is untouched")
					assert.Equal(t, oldVar, v, "the variable is untouched")
				case "seed":
					assert.Zero(t, len(traceSecretWrites(fc)), "no seed write")
					assert.False(t, set, "the variable stays absent")
				case "variable":
					assert.Equal(t, 1, len(traceSecretWrites(fc)), "one seed write")
					assert.False(t, set, "the variable stays absent")
				default:
					assert.True(t, set, "the variable is set")
					assert.Equal(t, id, v, "the variable names the new key")
				}
				if step == "none" {
					require.Zero(t, len(result.Failed()), "install:\n%s", strings.Join(reported, "\n"))
				} else {
					failed := result.Failed()
					require.Equal(t, 1, len(failed), "one failed repository:\n%s", strings.Join(reported, "\n"))
					assert.Contains(t, failed[0].Error.Error(), wants[step])
				}
				if step != "none" && step != "commit" {
					assert.Zero(t, client.committed(), "the repository stops before its commit")
				}

				progress = &progressLog{}
				second, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{Manifest: cfg.Manifest},
					newTestClientFactory(fc), progress.fn)
				require.NoError(t, err)
				lines := progress.all()
				requireNoSeed(t, lines, seed)
				if step == "none" || step == "commit" {
					assert.Empty(t, planNames(second), "the written key is healthy")
				}
				wantEnded := 0
				if fx.reenroll {
					// The install's commit has not landed, so the retry
					// still sees the ended installation's block.
					wantEnded = 1
				}
				assert.Equal(t, wantEnded, endedLines(lines, old), "ended installation lines:\n%s", strings.Join(lines, "\n"))
				cfg.TraceKeyPlans = second
				client = &traceFailClient{FakeClient: fc}
				convergeOK(t, client, cfg, client.land(), seed)
				after := manifestTrace(t, cfg.Manifest, "acme/api")
				assert.Equal(t, installation, after.InstallationID, "the installation is kept")
				assertTraceConverged(t, fc, after)
				assert.Subset(t, traceEligible(after), eligible, "no key leaves the eligible set")
				committed, err := installedTraceContext(context.Background(), fc, "acme", "api", ForgeGitHub)
				require.NoError(t, err)
				assert.Equal(t, after, committed, "the commit delivers the entry's block")

				progress = &progressLog{}
				third, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{Manifest: cfg.Manifest},
					newTestClientFactory(fc), progress.fn)
				require.NoError(t, err)
				assert.Empty(t, planNames(third), "the install is healthy after one retry")
				assert.Zero(t, countLines(progress.all(), "ended installation"), "the landed commit leaves no ended installation")
			})
		}
	}
}

// Convergence reads FULLSEND_TRACE_SIGNING_KEY_ID again before its writes
// and writes nothing unless the variable is still absent, or still present
// with the same value, as the preflight read it: another install or
// uninstall that ran meanwhile fails the repository before any delete,
// write or commit, and the variable is left as that run left it.
func TestConverge_TraceKeyRereadComparesPresenceAndValue(t *testing.T) {
	old, z := newTraceKey(1), newTraceKey(6)
	set := func(value string) func(*forge.FakeClient) {
		return func(fc *forge.FakeClient) { setKeyIDVar(fc, "acme/api", value) }
	}
	cases := []struct {
		name string
		// installed plans a rotation of a healthy install whose variable
		// names old; otherwise a fresh install plans with it absent.
		installed bool
		change    func(fc *forge.FakeClient)
		// want is the variable after the run; "" with wantSet false is
		// absent.
		want    string
		wantSet bool
	}{
		{name: "absent, then another key ID", change: set(z.id()), want: z.id(), wantSet: true},
		{name: "old key ID, then another", installed: true, change: set(z.id()), want: z.id(), wantSet: true},
		{name: "old key ID, then absent", installed: true, change: func(fc *forge.FakeClient) {
			delete(fc.VariableValues, "acme/api/"+forge.VarTraceSigningKeyID)
			delete(fc.VariablesExist, "acme/api/"+forge.VarTraceSigningKeyID)
		}},
		{name: "absent, then present but empty", change: set(""), want: "", wantSet: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc, cfg := traceFixture(t, tc.installed, "acme/api")
			if tc.installed {
				healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, old), old)
			}
			plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rotate: tc.installed})
			require.NoError(t, err)
			seed := requireTracePlan(t, plans, "acme/api").Key.EncodeSeed()
			cfg.TraceKeyPlans = plans
			tc.change(fc)

			client := &traceFailClient{FakeClient: fc}
			progress := &progressLog{}
			result := convergeTrace(t, client, cfg, client.commit(nil), progress)
			reported := traceReported(result, progress)
			requireNoSeed(t, reported, seed)
			failed := result.Failed()
			require.Equal(t, 1, len(failed), "one failed repository:\n%s", strings.Join(reported, "\n"))
			assert.Contains(t, failed[0].Error.Error(), forge.SecretTraceSigningKey+" not set: "+forge.VarTraceSigningKeyID+
				" changed after this run read it, as when another install or uninstall of acme/api runs at the same time; "+
				"once that run has finished, run repos install again (after another install, with the repos.yaml that install wrote)")
			assert.Zero(t, traceVarRecords(fc.DeletedVariables), "nothing is deleted")
			assert.Zero(t, len(traceSecretWrites(fc)), "no seed write")
			assert.Zero(t, traceVarRecords(fc.Variables), "no variable write")
			assert.Zero(t, client.committed(), "nothing is committed")
			v, ok := keyIDVar(fc)
			assert.Equal(t, tc.wantSet, ok, "the variable is left as the other run left it")
			assert.Equal(t, tc.want, v, "the variable is left as the other run left it")
		})
	}
}

// The real uninstall keeps the repos.yaml entry, as repos uninstall
// --uninstall-only does, and on GitHub the committed .fullsend/config.yaml;
// it deletes the seed. The next install keeps the installation, retains
// the old active key and writes a new key before its commit.
func TestTraceKeys_UninstallOnlyThenInstall(t *testing.T) {
	old := newTraceKey(1)
	fc, cfg := traceFixture(t, true, "acme/api")
	healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, old), old)
	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest: cfg.Manifest, Repos: []string{"acme/api"}, Direct: true, MaxConcurrency: 1,
	}, newTestClientFactory(fc), uninstallCommitFn(fc), nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].Success, "uninstall: %v", results[0].Error)
	assert.False(t, fc.Secrets["acme/api/"+forge.SecretTraceSigningKey], "uninstall deletes the seed")

	progress := &progressLog{}
	plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{Manifest: cfg.Manifest}, newTestClientFactory(fc), progress.fn)
	require.NoError(t, err)
	p := requireTracePlan(t, plans, "acme/api")
	assert.Equal(t, reasonRecovery, p.Reason)
	// The committed block is the entry's own installation, so it has not
	// ended although no fullsend workflow remains.
	assert.Zero(t, countLines(progress.all(), "ended installation"), "the installation has not ended")
	tc := manifestTrace(t, cfg.Manifest, "acme/api")
	assert.Equal(t, traceInstallA, tc.InstallationID, "the installation ID is kept")
	assert.Equal(t, []string{old.pubB64()}, tc.VerificationKeys.Retained, "the old active key is retained")
	assertPlanKeyIsActive(t, p, tc)

	// Uninstall deleted the inference secrets too, so the reinstall supplies
	// them again.
	cfg = convergeCfgWithDefaults(cfg.Manifest)
	cfg.TraceKeyPlans = plans
	client := &traceFailClient{FakeClient: fc}
	convergeOK(t, client, cfg, client.land(), p.Key.EncodeSeed())
	assert.Equal(t, 1, client.committed())
	assertTraceConverged(t, fc, tc)
}

// githubFullUninstall returns acme/api as a finished full uninstall leaves
// it on GitHub after installation traceInstallA with active key old: no
// fullsend workflow, FULLSEND_TRACE_SIGNING_KEY or
// FULLSEND_TRACE_SIGNING_KEY_ID, the committed .fullsend/config.yaml with its
// trace_context still in place, and the repository declared again in
// repos.yaml without trace_context. The config supplies the inference
// secrets.
func githubFullUninstall(t *testing.T, old traceKey) (*forge.FakeClient, ConvergeConfig) {
	t.Helper()
	fc, cfg := traceFixture(t, false, "acme/api")
	setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, old))
	return fc, cfg
}

// endedLines counts the progress lines that name the ended installation
// traceInstallA as one the entry's installation replaces, and old's key ID
// as one the repository stops trusting.
func endedLines(lines []string, old traceKey) int {
	return countLines(lines, traceInstallA+": the repos.yaml entry's installation replaces it and stops trusting key ID "+old.id())
}

// After a full uninstall, which on GitHub leaves the committed
// .fullsend/config.yaml in place, the repository declared again without
// trace_context is a fresh install of a new installation: the preflight
// plans a new installation ID and key pair and names, once, the ended
// installation and the key IDs it stops trusting, sorted (the re-enrollment
// rows of TestTraceKeys_WriteStepFailureThenRetry converge it). While the
// workflow, the seed or the key ID variable remains, the uninstall is
// unfinished and the entry is refused.
func TestTraceKeys_GitHubReenrollAfterFullUninstall(t *testing.T) {
	old, k4, k7 := newTraceKey(1), newTraceKey(4), newTraceKey(7)
	cases := []struct {
		name string
		// leftover puts back what an unfinished uninstall leaves; nil is a
		// finished one.
		leftover func(fc *forge.FakeClient)
		// ended, when set, replaces the ended block's keys, active first.
		// stops is how the notice lists the keys.
		ended []traceKey
		stops string
	}{
		{name: "full uninstall", stops: "key ID " + old.id()},
		// The keys go in descending key ID order, so a notice that lists
		// them unsorted never matches, whatever the map order.
		{name: "full uninstall with retained keys", ended: []traceKey{k7, k4, old},
			stops: "key IDs " + old.id() + ", " + k4.id() + ", " + k7.id()},
		{name: "workflow left", leftover: func(fc *forge.FakeClient) {
			fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = []byte("name: fullsend")
		}},
		{name: "seed left", leftover: func(fc *forge.FakeClient) { setTraceSecret(fc, "acme/api") }},
		{name: "variable left", leftover: func(fc *forge.FakeClient) { setKeyIDVar(fc, "acme/api", old.id()) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc, cfg := githubFullUninstall(t, old)
			if tc.ended != nil {
				setInstalledTrace(fc, "acme/api", traceBlock(traceInstallA, tc.ended[0], tc.ended[1:]...))
			}
			if tc.leftover != nil {
				tc.leftover(fc)
				err := refuseTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest}, nil, old)
				require.ErrorIs(t, err, ErrTraceManifestStale)
				msg := err.Error()
				assert.Contains(t, msg, "has trace_context and the repos.yaml entry has none (install enrolls the repository again "+
					"as a new installation only once no fullsend workflow, "+forge.SecretTraceSigningKey+" or "+forge.VarTraceSigningKeyID+" remains")
				assert.True(t, strings.HasSuffix(msg, refusalRemedy), msg)
				assert.NotContains(t, msg, old.id())
				return
			}

			progress := &progressLog{}
			plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{Manifest: cfg.Manifest},
				newTestClientFactory(fc), progress.fn)
			require.NoError(t, err)
			p := requireTracePlan(t, plans, "acme/api")
			lines := progress.all()
			requireNoSeed(t, lines, p.Key.EncodeSeed())
			assert.Equal(t, reasonFreshInstall, p.Reason)
			got := manifestTrace(t, cfg.Manifest, "acme/api")
			assertTraceBlockComplete(t, got)
			assert.NotEqual(t, traceInstallA, got.InstallationID, "a new installation starts")
			assert.Empty(t, got.VerificationKeys.Retained, "the ended installation's key is not carried over")
			assertPlanKeyIsActive(t, p, got)
			assert.Equal(t, 1, countLines(lines, "acme/api manifest The committed "+preset.OverlayPath+" has the trace_context of the ended installation "+
				traceInstallA+": the repos.yaml entry's installation replaces it and stops trusting "+tc.stops),
				"progress names the ended installation once:\n%s", strings.Join(lines, "\n"))
		})
	}
}

// A fully uninstalled repository enrolled again from a manifest install
// cannot write is a fresh install like any other. The preflight plans
// nothing, edits nothing and prints two lines: the read-only notice, and
// that install's commit removes the ended installation's block, since the
// entry records no installation to replace it. Install's commit leaves a
// .fullsend/config.yaml without trace_context.
func TestTraceKeys_GitHubReenrollFromReadOnlyManifest(t *testing.T) {
	const url = "https://example.com/repos.yaml"
	old := newTraceKey(1)
	fc, cfg := githubFullUninstall(t, old)
	before := manifestSnapshot(t, cfg.Manifest)

	progress := &progressLog{}
	plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{Manifest: cfg.Manifest, ManifestPath: url},
		newTestClientFactory(fc), progress.fn)
	require.NoError(t, err)
	assert.Empty(t, planNames(plans))
	assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest), "the entry is not edited")
	assertNoForgeWrites(t, fc)
	lines := progress.all()
	out := strings.Join(lines, "\n")
	assert.Len(t, lines, 2, "the notice and the ended installation:\n%s", out)
	assert.Equal(t, 1, countLines(lines, "acme/api manifest Trace signing keys not provisioned: repos.yaml is read-only"), out)
	assert.Equal(t, 1, countLines(lines, "acme/api manifest The committed "+preset.OverlayPath+" has the trace_context of the ended installation "+
		traceInstallA+": install's commit removes it, so the repository stops trusting key ID "+old.id()), out)

	client := &traceFailClient{FakeClient: fc}
	progress = &progressLog{}
	result := convergeTrace(t, client, cfg, client.land(), progress)
	reported := traceReported(result, progress)
	require.Zero(t, len(result.Failed()), "install:\n%s", strings.Join(reported, "\n"))
	assert.Equal(t, 1, client.committed())
	assert.Empty(t, traceActions(result), "install writes no trace value")
	committed, err := installedTraceContext(context.Background(), fc, "acme", "api", ForgeGitHub)
	require.NoError(t, err)
	assert.Nil(t, committed, "install's commit removes the ended block")
}
