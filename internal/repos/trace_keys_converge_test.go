package repos

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
	"github.com/fullsend-ai/fullsend/internal/telemetry/tracecontext"
)

// traceWriteClient adapts selectiveSecretClient (gitlab_roles_rotate_test.go):
// it records the trace secret write, the ledger write and each scaffold
// commit in call order, and fails the trace writes named in fail ("read "
// plus the ledger's name fails reading the ledger). Embedding the fake keeps
// its optional capabilities (project leases) visible.
type traceWriteClient struct {
	*forge.FakeClient
	fail map[string]error
	// beforeLedger, when set, runs once just before the ledger write: it
	// lets another install run between this run's seed and ledger writes.
	beforeLedger func()
	// leaseRefused, when set, runs once when this run's first lease request
	// is refused: it lets another installer finish while this run waits.
	leaseRefused func()

	mu     sync.Mutex
	events []string
	// leased records, for each trace write, whether acme/api's project
	// lease was held when it was made.
	leased []bool
}

func (c *traceWriteClient) record(event string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
	c.leased = append(c.leased, traceLeaseHeld(c.FakeClient))
}

func (c *traceWriteClient) log() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.events)
}

func (c *traceWriteClient) CreateRepoSecret(ctx context.Context, owner, repo, name, value string) error {
	if name == forge.SecretTraceSigningKey {
		c.record("secret")
		if err := c.fail[name]; err != nil {
			return err
		}
	}
	return c.FakeClient.CreateRepoSecret(ctx, owner, repo, name, value)
}

func (c *traceWriteClient) CreateOrUpdateRepoVariable(ctx context.Context, owner, repo, name, value string) error {
	if name == forge.VarTraceSigningKeyID {
		if f := c.beforeLedger; f != nil {
			c.beforeLedger = nil
			f()
		}
		c.record("ledger")
		if err := c.fail[name]; err != nil {
			return err
		}
	}
	return c.FakeClient.CreateOrUpdateRepoVariable(ctx, owner, repo, name, value)
}

func (c *traceWriteClient) GetRepoVariable(ctx context.Context, owner, repo, name string) (string, bool, error) {
	if err := c.fail["read "+name]; err != nil && name == forge.VarTraceSigningKeyID {
		return "", false, err
	}
	return c.FakeClient.GetRepoVariable(ctx, owner, repo, name)
}

func (c *traceWriteClient) AcquireProjectLease(ctx context.Context, owner, repo, name, holder string) (bool, error) {
	ok, err := c.FakeClient.AcquireProjectLease(ctx, owner, repo, name, holder)
	if f := c.leaseRefused; err == nil && !ok && f != nil {
		c.leaseRefused = nil
		f()
	}
	return ok, err
}

// traceLeaseHeld reports whether acme/api's project lease is held, by
// trying to take it as a probe.
func traceLeaseHeld(fc *forge.FakeClient) bool {
	ok, err := fc.AcquireProjectLease(context.Background(), "acme", "api", GitLabProjectLeaseVar, "probe")
	if err == nil && ok {
		_ = fc.ReleaseProjectLease(context.Background(), "acme", "api", GitLabProjectLeaseVar, "probe")
		return false
	}
	return true
}

// commit records each scaffold commit and returns err.
func (c *traceWriteClient) commit(err error) ScaffoldCommitFunc {
	return func(context.Context, string, string, []forge.TreeFile, bool, bool) error {
		c.record("commit")
		return err
	}
}

// land records each scaffold commit and lands its files on the default
// branch, so the committed config can be read back.
func (c *traceWriteClient) land() ScaffoldCommitFunc {
	return func(ctx context.Context, owner, repo string, files []forge.TreeFile, _, _ bool) error {
		c.record("commit")
		_, err := c.CommitFiles(ctx, owner, repo, "install", files)
		return err
	}
}

// traceWriteFailures stops a convergence at the seed write, at the ledger
// write or at the commit.
var traceWriteFailures = []struct {
	name      string
	fail      map[string]error
	commitErr error
}{
	{"seed write fails", map[string]error{forge.SecretTraceSigningKey: errors.New("seed write failed")}, nil},
	{"ledger write fails", map[string]error{forge.VarTraceSigningKeyID: errors.New("ledger write failed")}, nil},
	{"commit fails", nil, errors.New("commit failed")},
}

// traceConvergeFixture returns acme/api fresh, or installed with one
// scaffold file to repair so the established path commits.
func traceConvergeFixture(t *testing.T, forgeName string, installed bool) (*forge.FakeClient, ConvergeConfig) {
	t.Helper()
	fc, cfg := traceFixture(t, forgeName, installed, "acme/api")
	if installed {
		if forgeName == ForgeGitLab {
			delete(fc.FileContents, "acme/api/"+gitlabTrustScriptPath)
		} else {
			fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = makeWorkflow("v1.0.0")
		}
	}
	return fc, cfg
}

// planTrace leaves cfg as the trace-key preflight does for acme/api with key
// k: the manifest entry names k as active, and a plan carries its seed and
// the ledger value the preflight read from fc.
func planTrace(t *testing.T, cfg *ConvergeConfig, fc *forge.FakeClient, forgeName string, k traceKey) {
	t.Helper()
	setManifestTrace(t, cfg.Manifest, "acme/api", traceBlock(traceInstallA, k))
	key, err := tracecontext.ParseSigningSeed(k.seedB64())
	require.NoError(t, err)
	ledger, _ := traceLedger(fc)
	cfg.TraceKeyPlans = map[string]*TraceKeyPlan{"acme/api": {Owner: "acme", Repo: "api", Forge: forgeName, Key: key, Reason: "fresh install", Ledger: ledger}}
}

func convergeTrace(t *testing.T, fc *forge.FakeClient, cfg ConvergeConfig, fail map[string]error, commitErr error) (*ConvergeBatchResult, *traceWriteClient) {
	t.Helper()
	client := &traceWriteClient{FakeClient: fc, fail: fail}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(client), client.commit(commitErr), noopProgress)
	require.NoError(t, err)
	return result, client
}

// traceSecretWrites returns the values written to the trace secret, in order.
func traceSecretWrites(fc *forge.FakeClient) []string {
	var out []string
	for _, s := range fc.CreatedSecrets {
		if s.Name == forge.SecretTraceSigningKey {
			out = append(out, s.Value)
		}
	}
	return out
}

func traceLedger(fc *forge.FakeClient) (string, bool) {
	v, ok := fc.VariableValues["acme/api/"+forge.VarTraceSigningKeyID]
	return v, ok
}

// traceSecretName matches the secret's name but not the ledger's, which
// extends it.
var traceSecretName = regexp.MustCompile(regexp.QuoteMeta(forge.SecretTraceSigningKey) + `\b`)

// traceReported is everything convergence reported: errors, actions and
// progress lines.
func traceReported(result *ConvergeBatchResult, progress *progressLog) []string {
	var out []string
	for _, r := range result.Results {
		if r.Error != nil {
			out = append(out, r.Error.Error())
		}
		for _, a := range r.Actions {
			out = append(out, a.Component+" "+a.Detail)
		}
	}
	return append(out, progress.all()...)
}

// assertTraceConverged checks the forge against tc: the ledger names
// the active key and the last seed written expands to it.
func assertTraceConverged(t *testing.T, fc *forge.FakeClient, tc *config.TraceContextConfig) {
	t.Helper()
	active, err := base64.StdEncoding.DecodeString(tc.VerificationKeys.Active)
	require.NoError(t, err)
	ledger, _ := traceLedger(fc)
	assert.Equal(t, traceKeyID(active), ledger, "the ledger names the active key")
	writes := traceSecretWrites(fc)
	require.NotEmpty(t, writes, "no seed was written")
	seed, err := base64.StdEncoding.DecodeString(writes[len(writes)-1])
	require.NoError(t, err)
	require.Equal(t, ed25519.SeedSize, len(seed), "the secret holds a 32-byte seed")
	assert.Equal(t, ed25519.PublicKey(active), ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey),
		"the secret holds the active key's seed")
}

func traceEligible(tc *config.TraceContextConfig) []string {
	return append([]string{tc.VerificationKeys.Active}, tc.VerificationKeys.Retained...)
}

func traceConvergeName(forgeName string, installed bool) string {
	if installed {
		return forgeName + "/established"
	}
	return forgeName + "/fresh"
}

// The planned seed, then its ledger, land before the commit that delivers
// the config naming the key, on the fresh-install path (before Install) and
// the established path (before the 2e commit).
func TestConverge_TraceKeyWrittenBeforeCommit(t *testing.T) {
	k := newTraceKey(7)
	for _, forgeName := range layerForges {
		for _, installed := range []bool{false, true} {
			t.Run(traceConvergeName(forgeName, installed), func(t *testing.T) {
				fc, cfg := traceConvergeFixture(t, forgeName, installed)
				planTrace(t, &cfg, fc, forgeName, k)
				client := &traceWriteClient{FakeClient: fc}
				progress := &progressLog{}
				result, err := Converge(context.Background(), cfg, newTestClientFactory(client), client.commit(nil), progress.fn)
				require.NoError(t, err)
				require.Empty(t, result.Failed(), "results: %+v", result.Results)

				assert.Equal(t, []string{"secret", "ledger", "commit"}, client.log())
				assert.True(t, slices.Equal([]string{k.seedB64()}, traceSecretWrites(fc)), "the secret holds the plan's EncodeSeed()")
				ledger, _ := traceLedger(fc)
				assert.Equal(t, k.id(), ledger, "the ledger holds FormatKeyID(plan.Key.KeyID())")
				// Progress names the secret it set, never its value.
				assert.True(t, slices.ContainsFunc(progress.all(), traceSecretName.MatchString),
					"progress names %s: %v", forge.SecretTraceSigningKey, progress.all())
				for _, s := range traceReported(result, progress) {
					assert.False(t, strings.Contains(s, k.seedB64()), "nothing reported shows the seed")
				}
			})
		}
	}
}

// The seed is written even when the install reuses its inference
// secrets (ReuseSecrets skips Install's secret step).
func TestConverge_TraceKeyWrittenWhenInferenceSecretsAreReused(t *testing.T) {
	k := newTraceKey(7)
	cases := []struct {
		name      string
		forgeName string
		setup     func(fc *forge.FakeClient) ConvergeConfig
	}{
		{"github openai-wif without a GCP project", ForgeGitHub, func(fc *forge.FakeClient) ConvergeConfig {
			setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
			return openAIWIFConvergeCfg("acme/api")
		}},
		{"github existing Vertex secrets", ForgeGitHub, func(fc *forge.FakeClient) ConvergeConfig {
			fc.Secrets["acme/api/"+forge.SecretGCPProjectID] = true
			fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider] = true
			fc.VariableValues["acme/api/"+forge.VarGCPRegion] = "us-central1"
			return withoutInferenceInputs(convergeCfgWithDefaults(newConvergeManifest("acme/api")))
		}},
		{"gitlab existing OpenAI key", ForgeGitLab, func(fc *forge.FakeClient) ConvergeConfig {
			fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
			cfg := gitlabConvergeCfg("acme/api")
			cfg.OpenAIAPIKey = ""
			return cfg
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			cfg := tc.setup(fc)
			planTrace(t, &cfg, fc, tc.forgeName, k)
			result, client := convergeTrace(t, fc, cfg, nil, nil)
			require.Empty(t, result.Failed(), "results: %+v", result.Results)
			require.Len(t, result.Installed(), 1)
			for _, s := range fc.CreatedSecrets {
				assert.NotContains(t, managedInferenceSecrets(), s.Name, "inference secrets are reused, not written")
			}

			assert.Equal(t, []string{"secret", "ledger", "commit"}, client.log())
			assert.True(t, slices.Equal([]string{k.seedB64()}, traceSecretWrites(fc)), "the secret holds the plan's EncodeSeed()")
			ledger, _ := traceLedger(fc)
			assert.Equal(t, k.id(), ledger)
		})
	}
}

// A failed trace write stops the repository before its commit, and
// the ledger never gets ahead of the seed. On the established path the
// ledger still names the previous key, so the next plain run regenerates.
func TestConverge_TraceKeyWriteFailureStopsBeforeCommit(t *testing.T) {
	k, old := newTraceKey(7), newTraceKey(3)
	for _, forgeName := range layerForges {
		for _, installed := range []bool{false, true} {
			for _, failing := range []string{forge.SecretTraceSigningKey, forge.VarTraceSigningKeyID} {
				t.Run(traceConvergeName(forgeName, installed)+"/"+failing, func(t *testing.T) {
					fc, cfg := traceConvergeFixture(t, forgeName, installed)
					if installed {
						setTraceSecret(fc, "acme/api")
						setTraceLedger(fc, "acme/api", old.id())
					}
					// The forge echoes the value it was sent.
					echoed := k.seedB64()
					if failing == forge.VarTraceSigningKeyID {
						echoed = k.id()
					}
					client := &traceWriteClient{FakeClient: fc, fail: map[string]error{
						failing: fmt.Errorf("forge rejected value %s", echoed),
					}}
					planTrace(t, &cfg, fc, forgeName, k)
					progress := &progressLog{}

					result, err := Converge(context.Background(), cfg, newTestClientFactory(client), client.commit(nil), progress.fn)
					require.NoError(t, err)
					failed := result.Failed()
					require.Len(t, failed, 1, "results: %+v", result.Results)
					assert.Regexp(t, "failed to set "+regexp.QuoteMeta(failing)+`\b`, failed[0].Error.Error(), "the error names the write that failed")
					assert.NotContains(t, client.log(), "commit", "the repository stops before its commit")
					if failing == forge.SecretTraceSigningKey {
						assert.NotContains(t, client.log(), "ledger", "the ledger is never written ahead of the seed")
					}
					ledger, exists := traceLedger(fc)
					if installed {
						assert.Equal(t, old.id(), ledger, "the ledger still names the previous key")
					} else {
						assert.False(t, exists, "no ledger is written")
					}
					for _, s := range traceReported(result, progress) {
						assert.False(t, strings.Contains(s, k.seedB64()), "nothing reported shows the seed")
					}
				})
			}
		}
	}
}

func TestConverge_TraceKeyDryRunWritesNothing(t *testing.T) {
	shortLease(t, 50*time.Millisecond)
	k := newTraceKey(7)
	for _, forgeName := range layerForges {
		for _, installed := range []bool{false, true} {
			t.Run(traceConvergeName(forgeName, installed), func(t *testing.T) {
				fc, cfg := traceConvergeFixture(t, forgeName, installed)
				cfg.DryRun = true
				planTrace(t, &cfg, fc, forgeName, k)
				// A dry run takes no project lease, so another installer's
				// GitLab lease neither blocks it nor is touched by it.
				leaseKey := "acme/api/" + GitLabProjectLeaseVar
				fc.ProjectLeases[leaseKey] = leaseTestForeignHolder
				client := &traceWriteClient{FakeClient: fc}
				progress := &progressLog{}

				result, err := Converge(context.Background(), cfg, newTestClientFactory(client), client.commit(nil), progress.fn)
				require.NoError(t, err)
				require.Empty(t, result.Failed(), "results: %+v", result.Results)

				var would bool
				for _, a := range result.Results[0].Actions {
					if traceSecretName.MatchString(a.Component+" "+a.Detail) && strings.Contains(strings.ToLower(a.Detail), "would") {
						would = true
					}
				}
				assert.True(t, would, "a dry run reports the %s write it would make: %+v", forge.SecretTraceSigningKey, result.Results[0].Actions)
				// The CLI prints progress, not actions (the
				// plannedInferenceSecretActions convention emits both).
				assert.True(t, slices.ContainsFunc(progress.all(), func(s string) bool {
					return traceSecretName.MatchString(s) && strings.Contains(strings.ToLower(s), "would")
				}), "dry-run progress names the %s write: %v", forge.SecretTraceSigningKey, progress.all())
				assert.Empty(t, client.log(), "a dry run writes no trace value and commits nothing")
				assert.Equal(t, leaseTestForeignHolder, fc.ProjectLeases[leaseKey], "a dry run takes no project lease")
				assert.Zero(t, len(traceSecretWrites(fc)), "no seed write")
				_, exists := traceLedger(fc)
				assert.False(t, exists)
				for _, s := range traceReported(result, progress) {
					assert.False(t, strings.Contains(s, k.seedB64()), "nothing reported shows the seed")
				}
			})
		}
	}
}

// Install refuses some GitLab fresh installs before its own writes, such as
// one whose pipeline-variable overrides are still allowed or whose push rule
// rejects the poll-state branches. A fresh install checks those gates before it
// writes the trace key, so a refused install leaves no seed and no ledger,
// and a dry run previews no trace write the install would refuse.
func TestConverge_GitLabFreshInstallRefusalWritesNoTraceKey(t *testing.T) {
	k := newTraceKey(7)
	refusals := []struct {
		name  string
		setup func(fc *forge.FakeClient)
		want  string
	}{
		{"pipeline-variable overrides allowed", func(fc *forge.FakeClient) {
			fc.PipelineVarOverrideRoles["acme/api"] = forge.PipelineVarOverrideDeveloper
		}, "refusing to deliver runnable typed GitLab jobs"},
		{"push rule rejects the poll-state branches", func(fc *forge.FakeClient) {
			fc.PushRuleBranchNameRegexes = map[string]string{"acme/api": "master"}
		}, "push rule"},
	}
	for _, r := range refusals {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dryRun=%t", r.name, dryRun), func(t *testing.T) {
				fc, cfg := traceConvergeFixture(t, ForgeGitLab, false)
				r.setup(fc)
				cfg.DryRun = dryRun
				planTrace(t, &cfg, fc, ForgeGitLab, k)
				client := &traceWriteClient{FakeClient: fc}
				progress := &progressLog{}

				result, err := Converge(context.Background(), cfg, newTestClientFactory(client), client.commit(nil), progress.fn)
				require.NoError(t, err)
				failed := result.Failed()
				require.Len(t, failed, 1, "results: %+v", result.Results)
				assert.ErrorContains(t, failed[0].Error, r.want)
				assert.Empty(t, client.log(), "the refused install writes no trace value and commits nothing")
				// Counted, not printed: a failure must not echo a seed.
				assert.Zero(t, len(traceSecretWrites(fc)), "no seed is written")
				_, exists := traceLedger(fc)
				assert.False(t, exists, "no ledger is written")
				for _, s := range traceReported(result, progress) {
					assert.NotContains(t, s, forge.SecretTraceSigningKey, "nothing reports or previews a trace write")
				}
			})
		}
	}
}

// Guard: a repository without a plan gets no trace write.
func TestConverge_NoTracePlanNoTraceWrites(t *testing.T) {
	other := map[string]*TraceKeyPlan{"acme/other": {Owner: "acme", Repo: "other", Forge: ForgeGitHub}}
	for _, forgeName := range layerForges {
		for _, installed := range []bool{false, true} {
			for name, plans := range map[string]map[string]*TraceKeyPlan{"no plans": nil, "another repository's plan": other} {
				t.Run(traceConvergeName(forgeName, installed)+"/"+name, func(t *testing.T) {
					fc, cfg := traceConvergeFixture(t, forgeName, installed)
					cfg.TraceKeyPlans = plans
					result, client := convergeTrace(t, fc, cfg, nil, nil)
					require.Empty(t, result.Failed(), "results: %+v", result.Results)
					assert.Equal(t, []string{"commit"}, client.log())
					assert.Zero(t, len(traceSecretWrites(fc)), "no seed write")
					_, exists := traceLedger(fc)
					assert.False(t, exists)
				})
			}
		}
	}
}

// Guard: provisioning the trace key leaves
// FULLSEND_DISPATCH_SECRET exactly as an install without a plan writes it.
func TestConverge_TracePlanLeavesDispatchSecretAlone(t *testing.T) {
	k := newTraceKey(7)
	dispatchWrites := func(fc *forge.FakeClient) []string {
		var out []string
		for _, s := range fc.CreatedSecrets {
			if s.Name == forge.SecretDispatch {
				out = append(out, s.Value)
			}
		}
		return out
	}
	for _, forgeName := range layerForges {
		t.Run(forgeName, func(t *testing.T) {
			plainFC, plainCfg := traceConvergeFixture(t, forgeName, false)
			result, _ := convergeTrace(t, plainFC, plainCfg, nil, nil)
			require.Empty(t, result.Failed(), "results: %+v", result.Results)

			fc, cfg := traceConvergeFixture(t, forgeName, false)
			planTrace(t, &cfg, fc, forgeName, k)
			result, _ = convergeTrace(t, fc, cfg, nil, nil)
			require.Empty(t, result.Failed(), "results: %+v", result.Results)

			assert.Equal(t, len(dispatchWrites(plainFC)), len(dispatchWrites(fc)), "dispatch secret writes")
			assert.False(t, slices.Contains(dispatchWrites(fc), k.seedB64()), "the dispatch secret is not the seed")
		})
	}
}

// The manifest entry is recorded before the first forge write, the
// seed and ledger match the active key, and a second run writes nothing.
func TestTraceKeys_FreshThenReinstall(t *testing.T) {
	for _, forgeName := range layerForges {
		t.Run(forgeName, func(t *testing.T) {
			fc, cfg := traceConvergeFixture(t, forgeName, false)
			plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest})
			require.NoError(t, err)
			p := requireTracePlan(t, plans, "acme/api", forgeName)
			assertNoForgeWrites(t, fc)
			tc := manifestTrace(t, cfg.Manifest, "acme/api")
			assertPlanKeyIsActive(t, p, tc)

			cfg.TraceKeyPlans = plans
			result, client := convergeTrace(t, fc, cfg, nil, nil)
			require.Empty(t, result.Failed(), "results: %+v", result.Results)
			assert.Equal(t, []string{"secret", "ledger", "commit"}, client.log())
			assertTraceConverged(t, fc, tc)

			// Fresh entropy: a wrong regeneration shows as a plan, not as a
			// duplicate of the first key.
			again, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rand: traceRand(2)})
			require.NoError(t, err)
			assert.Empty(t, again)
			cfg.TraceKeyPlans = again
			result, client = convergeTrace(t, fc, cfg, nil, nil)
			require.Empty(t, result.Failed(), "results: %+v", result.Results)
			assert.Equal(t, []string{"commit"}, client.log(), "the second run writes no trace value")
			assert.Equal(t, tc.InstallationID, manifestTrace(t, cfg.Manifest, "acme/api").InstallationID)
		})
	}
}

// After a failure at the seed write, the ledger write or the commit of a
// fresh install or a rotation, one plain retry converges: the ledger names
// the active key, the secret holds its seed, the installation ID is kept and
// no key leaves the eligible set.
func TestTraceKeys_RetryAfterFailure(t *testing.T) {
	old := newTraceKey(1)
	for _, forgeName := range layerForges {
		for _, installed := range []bool{false, true} {
			for _, f := range traceWriteFailures {
				t.Run(traceConvergeName(forgeName, installed)+"/"+f.name, func(t *testing.T) {
					fc, cfg := traceConvergeFixture(t, forgeName, installed)
					if installed {
						healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, old), old)
						if forgeName == ForgeGitLab {
							// The fullsend jobs on the default branch unset the seed.
							commitGitLabJobTemplates(t, fc, "acme/api", nil)
						}
					}
					// The installed repository rotates; the fresh one provisions.
					first, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rotate: installed})
					require.NoError(t, err)
					requireTracePlan(t, first, "acme/api", forgeName)
					before := manifestTrace(t, cfg.Manifest, "acme/api")
					installation, eligible := before.InstallationID, traceEligible(before)

					cfg.TraceKeyPlans = first
					result, _ := convergeTrace(t, fc, cfg, f.fail, f.commitErr)
					require.Len(t, result.Failed(), 1, "results: %+v", result.Results)
					if installed && f.commitErr == nil {
						ledger, _ := traceLedger(fc)
						assert.Equal(t, old.id(), ledger, "the ledger still names the previous key")
					}

					// Each preflight run draws its own entropy, as
					// crypto/rand does: reusing traceRand(1) would
					// regenerate the failed key as a duplicate.
					second, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rand: traceRand(2)})
					require.NoError(t, err)
					cfg.TraceKeyPlans = second
					result, _ = convergeTrace(t, fc, cfg, nil, nil)
					require.Empty(t, result.Failed(), "results: %+v", result.Results)

					after := manifestTrace(t, cfg.Manifest, "acme/api")
					assert.Equal(t, installation, after.InstallationID)
					assertTraceConverged(t, fc, after)
					assert.Subset(t, traceEligible(after), eligible, "no key leaves the eligible set")
					third, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rand: traceRand(3)})
					require.NoError(t, err)
					assert.Empty(t, third, "the install is healthy after one retry")
				})
			}
		}
	}
}

// The real uninstall deletes the seed and the ledger (and, on
// GitLab, the managed config.yaml), and --uninstall-only keeps the manifest
// entry. The next install takes the recovery path: it keeps the
// installation ID, retains the old active key, and writes a new seed and
// its ledger before the commit.
func TestTraceKeys_UninstallOnlyThenInstall(t *testing.T) {
	old := newTraceKey(1)
	for _, forgeName := range layerForges {
		t.Run(forgeName, func(t *testing.T) {
			fc, cfg := traceFixture(t, forgeName, true, "acme/api")
			healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, old), old)
			results, err := Uninstall(context.Background(), UninstallConfig{
				Manifest: cfg.Manifest, Repos: []string{"acme/api"}, Direct: true, MaxConcurrency: 1,
			}, newTestClientFactory(fc), uninstallCommitFn(fc), nil)
			require.NoError(t, err)
			require.Len(t, results, 1)
			require.True(t, results[0].Success, "uninstall: %v", results[0].Error)
			assert.False(t, fc.Secrets["acme/api/"+forge.SecretTraceSigningKey], "uninstall deletes the seed")
			_, ledgerLeft := traceLedger(fc)
			assert.False(t, ledgerLeft, "uninstall deletes the ledger")

			plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rand: traceRand(2)})
			require.NoError(t, err)
			p := requireTracePlan(t, plans, "acme/api", forgeName)
			assert.Equal(t, "recovery", p.Reason)
			tc := manifestTrace(t, cfg.Manifest, "acme/api")
			assert.Equal(t, traceInstallA, tc.InstallationID, "the installation ID is kept")
			assert.Equal(t, []string{old.pubB64()}, tc.VerificationKeys.Retained, "the old active key is retained")
			assertPlanKeyIsActive(t, p, tc)

			if forgeName == ForgeGitHub {
				// Uninstall deleted the inference secrets too, so the
				// reinstall supplies them again.
				cfg = convergeCfgWithDefaults(cfg.Manifest)
			}
			cfg.TraceKeyPlans = plans
			result, client := convergeTrace(t, fc, cfg, nil, nil)
			require.Empty(t, result.Failed(), "results: %+v", result.Results)
			assert.Equal(t, []string{"secret", "ledger", "commit"}, client.log())
			assertTraceConverged(t, fc, tc)
		})
	}
}

// githubFullUninstall installs acme/api on GitHub as installation
// traceInstallA with active key old, runs the real uninstall, and declares
// the repository again with no trace_context, as after a full uninstall,
// which also removes the repos.yaml entry. The returned config supplies the
// inference secrets again, since uninstall deleted them.
func githubFullUninstall(t *testing.T, old traceKey) (*forge.FakeClient, ConvergeConfig) {
	t.Helper()
	fc, cfg := traceFixture(t, ForgeGitHub, true, "acme/api")
	healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, old), old)
	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest: cfg.Manifest, Repos: []string{"acme/api"}, Direct: true, MaxConcurrency: 1,
	}, newTestClientFactory(fc), uninstallCommitFn(fc), nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].Success, "uninstall: %v", results[0].Error)
	require.Contains(t, fc.FileContents, "acme/api/"+preset.OverlayPath, "GitHub uninstall leaves the committed config")
	assert.False(t, fc.Secrets["acme/api/"+forge.SecretTraceSigningKey], "uninstall deletes the seed")

	entry := findExplicitEntry(cfg.Manifest, "acme/api")
	entry.Config = entry.Config.WithTraceContext(nil)
	return fc, convergeCfgWithDefaults(cfg.Manifest)
}

// endedLines counts the progress lines that name the ended installation
// traceInstallA and old's key ID as one it stops trusting.
func endedLines(lines []string, old traceKey) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, traceInstallA) && strings.Contains(l, "stops trusting key ID "+old.id()) {
			n++
		}
	}
	return n
}

// On GitHub, uninstall deletes the workflows, the seed and the
// ledger but leaves the committed .fullsend/config.yaml, trace_context
// included, and a full uninstall also removes the repos.yaml entry. Declared
// again, the repository is a fresh install: the preflight plans a new
// installation ID and key pair and names, once, the ended installation and
// the key IDs it stops trusting, and convergence writes the seed, then its
// ledger, and replaces the committed block. While the workflow, the seed or
// the ledger remains, the uninstall is unfinished and the entry without a
// block is still refused as stale.
func TestTraceKeys_GitHubReenrollAfterFullUninstall(t *testing.T) {
	old := newTraceKey(1)
	cases := []struct {
		name string
		// leftover puts back what an unfinished uninstall leaves; nil is
		// a full uninstall.
		leftover func(fc *forge.FakeClient)
	}{
		{name: "full uninstall"},
		{name: "workflow left", leftover: func(fc *forge.FakeClient) {
			fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = []byte("name: fullsend")
		}},
		{name: "seed left", leftover: func(fc *forge.FakeClient) { setTraceSecret(fc, "acme/api") }},
		{name: "ledger left", leftover: func(fc *forge.FakeClient) { setTraceLedger(fc, "acme/api", old.id()) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc, cfg := githubFullUninstall(t, old)
			if tc.leftover != nil {
				tc.leftover(fc)
				before := manifestSnapshot(t, cfg.Manifest)
				plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rand: traceRand(2)})
				require.ErrorIs(t, err, ErrTraceManifestStale)
				assert.Empty(t, plans)
				assert.Contains(t, err.Error(), "has trace_context and the repos.yaml entry has none", "the committed block still makes the entry stale")
				assert.Contains(t, err.Error(), "new installation only once", "the refusal says when the repository is enrolled again")
				assert.Contains(t, err.Error(), "restore the entry's trace_context", "the refusal names keeping the installation")
				assertNoTraceValues(t, err.Error(), old)
				assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest))
				return
			}

			progress := &progressLog{}
			plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{Manifest: cfg.Manifest, Rand: traceRand(2)}, newTestClientFactory(fc), progress.fn)
			require.NoError(t, err)
			p := requireTracePlan(t, plans, "acme/api", ForgeGitHub)
			assert.Equal(t, "fresh install", p.Reason)
			got := manifestTrace(t, cfg.Manifest, "acme/api")
			assertTraceBlockComplete(t, got)
			assert.NotEqual(t, traceInstallA, got.InstallationID, "a new installation starts")
			assert.Empty(t, got.VerificationKeys.Retained, "the old installation's key is not carried over")
			assertPlanKeyIsActive(t, p, got)
			assert.Equal(t, 1, endedLines(progress.all(), old),
				"progress names the ended installation and the key it stops trusting, once:\n%s", strings.Join(progress.all(), "\n"))
			assert.Contains(t, strings.Join(progress.all(), "\n"), "the repos.yaml entry's installation replaces it", "the new installation replaces the ended one")
			for _, l := range progress.all() {
				assert.False(t, strings.Contains(l, p.Key.EncodeSeed()), "progress never shows the seed")
			}

			cfg.TraceKeyPlans = plans
			client := &traceWriteClient{FakeClient: fc}
			result, err := Converge(context.Background(), cfg, newTestClientFactory(client), client.land(), noopProgress)
			require.NoError(t, err)
			require.Empty(t, result.Failed(), "results: %+v", result.Results)
			assert.Equal(t, []string{"secret", "ledger", "commit"}, client.log())
			assertTraceConverged(t, fc, got)
			committed, err := installedTraceContext(context.Background(), fc, "acme", "api", ForgeGitHub)
			require.NoError(t, err)
			assert.Equal(t, got, committed, "convergence replaces the committed block")
		})
	}
}

// A re-enrollment after a full uninstall that stops at the seed write, the
// ledger write or the commit converges on one plain retry. Until install's
// commit lands, the default branch keeps the ended installation's block and
// has no fullsend workflow, so the retry treats that block as ended and
// names it once. After the retry, the ledger names the active key, the
// secret holds its seed, the installation ID is the entry's new one, and
// the committed block equals the entry's.
func TestTraceKeys_GitHubReenrollRetryAfterFailure(t *testing.T) {
	old := newTraceKey(1)
	for _, f := range traceWriteFailures {
		t.Run(f.name, func(t *testing.T) {
			fc, cfg := githubFullUninstall(t, old)
			first, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rand: traceRand(2)})
			require.NoError(t, err)
			requireTracePlan(t, first, "acme/api", ForgeGitHub)
			installation := manifestTrace(t, cfg.Manifest, "acme/api").InstallationID
			require.NotEqual(t, traceInstallA, installation, "a new installation starts")

			cfg.TraceKeyPlans = first
			result, _ := convergeTrace(t, fc, cfg, f.fail, f.commitErr)
			require.Len(t, result.Failed(), 1, "results: %+v", result.Results)

			progress := &progressLog{}
			second, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{Manifest: cfg.Manifest, Rand: traceRand(3)}, newTestClientFactory(fc), progress.fn)
			require.NoError(t, err, "the committed block is the ended installation's")
			if f.commitErr != nil {
				assert.Empty(t, second, "the seed and the ledger were written: the key is healthy")
			} else {
				assert.Equal(t, "recovery", requireTracePlan(t, second, "acme/api", ForgeGitHub).Reason)
			}
			assert.Equal(t, 1, endedLines(progress.all(), old),
				"progress names the ended installation once:\n%s", strings.Join(progress.all(), "\n"))

			cfg.TraceKeyPlans = second
			client := &traceWriteClient{FakeClient: fc}
			result, err = Converge(context.Background(), cfg, newTestClientFactory(client), client.land(), noopProgress)
			require.NoError(t, err)
			require.Empty(t, result.Failed(), "results: %+v", result.Results)

			after := manifestTrace(t, cfg.Manifest, "acme/api")
			assert.Equal(t, installation, after.InstallationID, "the entry's installation is kept")
			assertTraceConverged(t, fc, after)
			committed, err := installedTraceContext(context.Background(), fc, "acme", "api", ForgeGitHub)
			require.NoError(t, err)
			assert.Equal(t, after, committed, "the commit replaces the committed block")
			third, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rand: traceRand(4)})
			require.NoError(t, err)
			assert.Empty(t, third, "the install is healthy after one retry")
		})
	}
}

// Without --direct, install delivers a re-enrollment through a pull
// request. Until it merges, the default branch keeps the ended
// installation's block and has no fullsend workflow, so a re-run treats the
// block as ended: it plans no key, leaves the entry alone, names the ended
// installation once and commits again. Once that commit lands, a re-run is
// healthy and names no ended installation.
func TestTraceKeys_GitHubReenrollPullRequestWindow(t *testing.T) {
	old := newTraceKey(1)
	fc, cfg := githubFullUninstall(t, old)
	cfg.Direct = false
	first, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rand: traceRand(2)})
	require.NoError(t, err)
	requireTracePlan(t, first, "acme/api", ForgeGitHub)
	tc := manifestTrace(t, cfg.Manifest, "acme/api")

	// The commit opens the pull request: nothing lands on the default branch.
	cfg.TraceKeyPlans = first
	result, client := convergeTrace(t, fc, cfg, nil, nil)
	require.Empty(t, result.Failed(), "results: %+v", result.Results)
	assert.Equal(t, []string{"secret", "ledger", "commit"}, client.log())

	rerun := func(seed int64) (map[string]*TraceKeyPlan, []string) {
		t.Helper()
		progress := &progressLog{}
		plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{Manifest: cfg.Manifest, Rand: traceRand(seed)}, newTestClientFactory(fc), progress.fn)
		require.NoError(t, err)
		return plans, progress.all()
	}
	plans, lines := rerun(3)
	assert.Empty(t, plans, "the entry's key is healthy")
	assert.Equal(t, tc, manifestTrace(t, cfg.Manifest, "acme/api"), "the entry is unchanged")
	assert.Equal(t, 1, endedLines(lines, old), "progress names the ended installation once:\n%s", strings.Join(lines, "\n"))

	// The re-run commits again, and its pull request merges: the commit
	// lands on the default branch.
	cfg.TraceKeyPlans = plans
	client = &traceWriteClient{FakeClient: fc}
	result, err = Converge(context.Background(), cfg, newTestClientFactory(client), client.land(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed(), "results: %+v", result.Results)
	assert.Equal(t, []string{"commit"}, client.log(), "the re-run writes no trace value and commits again")

	plans, lines = rerun(4)
	assert.Empty(t, plans, "the install is healthy")
	assert.Zero(t, endedLines(lines, old), "no ended installation is left:\n%s", strings.Join(lines, "\n"))
	assertTraceConverged(t, fc, tc)
	committed, err := installedTraceContext(context.Background(), fc, "acme", "api", ForgeGitHub)
	require.NoError(t, err)
	assert.Equal(t, tc, committed, "the landed commit replaced the committed block")
}

// A fully uninstalled GitHub repository enrolled again from a manifest that
// install cannot write is a fresh install like any other: without a
// rotation it goes ahead without keys. The preflight plans nothing, edits
// nothing and writes nothing, and prints once that the keys were not
// provisioned and once that install's commit removes the ended
// installation's block, naming its ID and the key it stops trusting: the
// entry records no installation to replace it. A rotation still needs the
// repos.yaml edit and is refused. Install's commit leaves a
// .fullsend/config.yaml without trace_context, and a later rotation from a
// writable manifest provisions the keys as for any existing install.
func TestTraceKeys_GitHubReenrollFromReadOnlyManifest(t *testing.T) {
	const url = "https://example.com/repos.yaml"
	old := newTraceKey(1)
	fc, cfg := githubFullUninstall(t, old)
	// Forget the uninstall's writes, so that the preflights' would show.
	fc.CommittedFiles, fc.DeletedSecrets, fc.DeletedVariables = nil, nil, nil
	before := manifestSnapshot(t, cfg.Manifest)

	_, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, ManifestPath: url, Rotate: true})
	require.ErrorIs(t, err, ErrTraceManifestReadOnly, "a rotation needs the repos.yaml edit")
	assertNoTraceValues(t, err.Error(), old)

	progress := &progressLog{}
	plans, err := EnsureTraceSigningKeys(context.Background(), TraceKeysConfig{
		Manifest: cfg.Manifest, ManifestPath: url, Rand: traceRand(2),
	}, newTestClientFactory(fc), progress.fn)
	require.NoError(t, err)
	assert.Empty(t, plans)
	assert.Equal(t, before, manifestSnapshot(t, cfg.Manifest), "the entry is not edited")
	assertNoForgeWrites(t, fc)
	lines := progress.all()
	out := strings.Join(lines, "\n")
	assert.Len(t, lines, 2, "the notice and the ended installation:\n%s", out)
	assert.Equal(t, 1, strings.Count(out, "Trace signing keys not provisioned: repos.yaml is read-only"), out)
	assert.Equal(t, 1, endedLines(lines, old), "progress names the ended installation once:\n%s", out)
	assert.Equal(t, 1, strings.Count(out, "install's commit removes it"), "the entry records no installation to replace it:\n%s", out)
	assert.NotContains(t, out, "entry's installation replaces it")

	client := &traceWriteClient{FakeClient: fc}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(client), client.land(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed(), "results: %+v", result.Results)
	assert.Equal(t, []string{"commit"}, client.log(), "install writes no trace value")
	committed, err := installedTraceContext(context.Background(), fc, "acme", "api", ForgeGitHub)
	require.NoError(t, err)
	assert.Nil(t, committed, "install's commit removes the ended block")

	plans, err = ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rotate: true, Rand: traceRand(3)})
	require.NoError(t, err)
	p := requireTracePlan(t, plans, "acme/api", ForgeGitHub)
	assert.Equal(t, "rotation", p.Reason, "the first rotation provisions an existing install")
	got := manifestTrace(t, cfg.Manifest, "acme/api")
	assertTraceBlockComplete(t, got)
	assert.NotEqual(t, traceInstallA, got.InstallationID, "a new installation starts")
	assert.Empty(t, got.VerificationKeys.Retained, "the ended installation's key is not carried over")
	assertPlanKeyIsActive(t, p, got)
}

// Without --direct, install delivers a removal through a pull or merge
// request, so until it merges the committed config still trusts the removed
// key. Install cannot tell that from a key added to the committed config by
// hand: a plain run refuses the entry as stale and names the pending removal
// first. The same run naming the key again passes and drops nothing more,
// and once its commit lands a plain run is healthy.
func TestTraceKeys_RemovalPullRequestWindow(t *testing.T) {
	a, r1, r2 := newTraceKey(1), newTraceKey(2), newTraceKey(3)
	for _, forgeName := range layerForges {
		t.Run(forgeName, func(t *testing.T) {
			fc, cfg := traceConvergeFixture(t, forgeName, true)
			healthyTrace(t, cfg.Manifest, fc, "acme/api", traceBlock(traceInstallA, a, r1, r2), a)
			cfg.Direct = false
			removal := TraceKeysConfig{Manifest: cfg.Manifest, RemoveKeyIDs: []string{r1.id()}}
			plans, err := ensureTrace(t, fc, removal)
			require.NoError(t, err)
			require.Empty(t, plans, "a removal writes no key")
			require.Equal(t, []string{r2.pubB64()}, manifestTrace(t, cfg.Manifest, "acme/api").VerificationKeys.Retained)
			removed := manifestSnapshot(t, cfg.Manifest)

			// The commit opens the pull request: nothing lands on the
			// default branch.
			result, client := convergeTrace(t, fc, cfg, nil, nil)
			require.Empty(t, result.Failed(), "results: %+v", result.Results)
			assert.Equal(t, []string{"commit"}, client.log(), "a removal writes no trace value")

			_, err = ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rand: traceRand(2)})
			require.ErrorIs(t, err, ErrTraceManifestStale, "the committed config still trusts the removed key")
			assert.Contains(t, err.Error(), "trusts key ID "+r1.id()+", which")
			assertPendingRemovalFirst(t, err.Error())
			assertNoTraceValues(t, err.Error(), a, r1, r2)
			assert.Equal(t, removed, manifestSnapshot(t, cfg.Manifest), "the refusal edits nothing")

			progress := &progressLog{}
			removal.Rand = traceRand(3)
			plans, err = EnsureTraceSigningKeys(context.Background(), removal, newTestClientFactory(fc), progress.fn)
			require.NoError(t, err, "naming the key again passes")
			assert.Empty(t, plans)
			assert.Equal(t, removed, manifestSnapshot(t, cfg.Manifest), "the run drops nothing more")
			assert.Empty(t, progress.all(), "the run reports no removal")

			// That run commits again, and its pull request merges: the commit
			// lands on the default branch.
			cfg.TraceKeyPlans = plans
			client = &traceWriteClient{FakeClient: fc}
			result, err = Converge(context.Background(), cfg, newTestClientFactory(client), client.land(), noopProgress)
			require.NoError(t, err)
			require.Empty(t, result.Failed(), "results: %+v", result.Results)
			assert.Equal(t, []string{"commit"}, client.log(), "the run writes no trace value and commits again")
			committed, err := installedTraceContext(context.Background(), fc, "acme", "api", forgeName)
			require.NoError(t, err)
			assert.Equal(t, manifestTrace(t, cfg.Manifest, "acme/api"), committed, "the landed commit drops the key")

			plans, err = ensureTrace(t, fc, TraceKeysConfig{Manifest: cfg.Manifest, Rand: traceRand(4)})
			require.NoError(t, err, "a plain run passes once the removal is on the default branch")
			assert.Empty(t, plans, "the install is healthy")
			assert.Equal(t, removed, manifestSnapshot(t, cfg.Manifest))
		})
	}
}

// Another install of the repository can write a key, or an uninstall can
// delete the ledger, between this run's preflight and its writes. The run
// then writes no key, since writing would leave the secret and
// FULLSEND_TRACE_SIGNING_KEY_ID naming different keys, and it stops before
// its commit.
func TestConverge_TraceKeyNotWrittenWhenLedgerChanged(t *testing.T) {
	k, old, other := newTraceKey(7), newTraceKey(3), newTraceKey(5)
	cases := []struct {
		name      string
		installed bool
		// before is the ledger the preflight read, after the one the write
		// finds; "" means absent.
		before, after string
	}{
		{"another install wrote a key", false, "", other.id()},
		{"another install replaced the key", true, old.id(), other.id()},
		{"the ledger was deleted", true, old.id(), ""},
	}
	for _, forgeName := range layerForges {
		for _, tc := range cases {
			t.Run(forgeName+"/"+tc.name, func(t *testing.T) {
				fc, cfg := traceConvergeFixture(t, forgeName, tc.installed)
				if tc.before != "" {
					setTraceSecret(fc, "acme/api")
					setTraceLedger(fc, "acme/api", tc.before)
				}
				planTrace(t, &cfg, fc, forgeName, k)
				if tc.after == "" {
					delete(fc.VariableValues, "acme/api/"+forge.VarTraceSigningKeyID)
					delete(fc.VariablesExist, "acme/api/"+forge.VarTraceSigningKeyID)
				} else {
					setTraceLedger(fc, "acme/api", tc.after)
				}
				client := &traceWriteClient{FakeClient: fc}
				progress := &progressLog{}

				result, err := Converge(context.Background(), cfg, newTestClientFactory(client), client.commit(nil), progress.fn)
				require.NoError(t, err)
				failed := result.Failed()
				require.Len(t, failed, 1, "results: %+v", result.Results)
				assert.ErrorContains(t, failed[0].Error, forge.VarTraceSigningKeyID)
				assert.ErrorContains(t, failed[0].Error, "another install")
				assert.ErrorContains(t, failed[0].Error, "after another install, with the repos.yaml that install wrote")
				assert.Empty(t, client.log(), "no key is written and nothing is committed")
				ledger, exists := traceLedger(fc)
				assert.Equal(t, tc.after != "", exists)
				assert.Equal(t, tc.after, ledger, "the other install's ledger is left alone")
				for _, s := range traceReported(result, progress) {
					assert.False(t, strings.Contains(s, k.seedB64()), "nothing reported shows the seed")
				}
			})
		}
	}
}

// A failed read of FULLSEND_TRACE_SIGNING_KEY_ID before the writes writes
// nothing, and the error does not show the seed.
func TestConverge_TraceKeyLedgerReadFailureWritesNothing(t *testing.T) {
	k := newTraceKey(7)
	for _, forgeName := range layerForges {
		t.Run(forgeName, func(t *testing.T) {
			fc, cfg := traceConvergeFixture(t, forgeName, false)
			planTrace(t, &cfg, fc, forgeName, k)
			client := &traceWriteClient{FakeClient: fc, fail: map[string]error{
				"read " + forge.VarTraceSigningKeyID: fmt.Errorf("forge error echoing %s", k.seedB64()),
			}}
			progress := &progressLog{}

			result, err := Converge(context.Background(), cfg, newTestClientFactory(client), client.commit(nil), progress.fn)
			require.NoError(t, err)
			failed := result.Failed()
			require.Len(t, failed, 1, "results: %+v", result.Results)
			assert.ErrorContains(t, failed[0].Error, forge.VarTraceSigningKeyID)
			assert.Empty(t, client.log(), "nothing is written or committed")
			for _, s := range traceReported(result, progress) {
				assert.False(t, strings.Contains(s, k.seedB64()), "nothing reported shows the seed")
			}
		})
	}
}

// On GitLab the seed and its ledger are written under the project lease
// that serializes installers, and the lease is released afterwards. GitHub
// has no lease.
func TestConverge_TraceKeyWritesHoldGitLabLease(t *testing.T) {
	k := newTraceKey(7)
	for _, forgeName := range layerForges {
		for _, installed := range []bool{false, true} {
			t.Run(traceConvergeName(forgeName, installed), func(t *testing.T) {
				fc, cfg := traceConvergeFixture(t, forgeName, installed)
				planTrace(t, &cfg, fc, forgeName, k)
				result, client := convergeTrace(t, fc, cfg, nil, nil)
				require.Empty(t, result.Failed(), "results: %+v", result.Results)
				require.Equal(t, []string{"secret", "ledger", "commit"}, client.log())
				leased := forgeName == ForgeGitLab
				assert.Equal(t, []bool{leased, leased, false}, client.leased, "the lease is held for the seed and ledger writes only")
				assert.Empty(t, fc.ProjectLeases, "no lease is left behind")
			})
		}
	}
}

// Another installer holding the GitLab project lease stops the key write
// before anything is written, and its lease is left alone.
func TestConverge_GitLabTraceKeyWaitsForAnotherInstallersLease(t *testing.T) {
	shortLease(t, 50*time.Millisecond)
	k, old := newTraceKey(7), newTraceKey(3)
	for _, installed := range []bool{false, true} {
		t.Run(traceConvergeName(ForgeGitLab, installed), func(t *testing.T) {
			fc, cfg := traceConvergeFixture(t, ForgeGitLab, installed)
			if installed {
				setTraceSecret(fc, "acme/api")
				setTraceLedger(fc, "acme/api", old.id())
			}
			planTrace(t, &cfg, fc, ForgeGitLab, k)
			leaseKey := "acme/api/" + GitLabProjectLeaseVar
			fc.ProjectLeases[leaseKey] = leaseTestForeignHolder
			client := &traceWriteClient{FakeClient: fc}
			progress := &progressLog{}

			result, err := Converge(context.Background(), cfg, newTestClientFactory(client), client.commit(nil), progress.fn)
			require.NoError(t, err)
			failed := result.Failed()
			require.Len(t, failed, 1, "results: %+v", result.Results)
			assert.ErrorContains(t, failed[0].Error, forge.SecretTraceSigningKey+" not set")
			assert.ErrorContains(t, failed[0].Error, GitLabProjectLeaseVar)
			assert.Empty(t, client.log(), "no key is written and nothing is committed")
			assert.Equal(t, leaseTestForeignHolder, fc.ProjectLeases[leaseKey], "the other installer's lease is left alone")
			ledger, _ := traceLedger(fc)
			if installed {
				assert.Equal(t, old.id(), ledger, "the ledger still names the previous key")
			} else {
				assert.Empty(t, ledger, "no ledger is written")
			}
			for _, s := range traceReported(result, progress) {
				assert.False(t, strings.Contains(s, k.seedB64()), "nothing reported shows the seed")
			}
		})
	}
}

// overlappingTracePlans plans acme/api twice from the same forge state, as
// two installs started together do, each from its own copy of repos.yaml.
func overlappingTracePlans(t *testing.T, forgeName string) (*forge.FakeClient, ConvergeConfig, ConvergeConfig) {
	t.Helper()
	fc, first := traceConvergeFixture(t, forgeName, false)
	_, second := traceConvergeFixture(t, forgeName, false)
	plans, err := ensureTrace(t, fc, TraceKeysConfig{Manifest: first.Manifest, Rand: traceRand(1)})
	require.NoError(t, err)
	requireTracePlan(t, plans, "acme/api", forgeName)
	first.TraceKeyPlans = plans
	plans, err = ensureTrace(t, fc, TraceKeysConfig{Manifest: second.Manifest, Rand: traceRand(2)})
	require.NoError(t, err)
	requireTracePlan(t, plans, "acme/api", forgeName)
	second.TraceKeyPlans = plans
	return fc, first, second
}

// Two installs of one repository plan their keys from the same state. The
// first writes its key; the second, whose preflight did not see that key,
// writes nothing, so the secret, the ledger and the first run's repos.yaml
// still agree.
func TestTraceKeys_OverlappingInstallsStayConsistent(t *testing.T) {
	for _, forgeName := range layerForges {
		t.Run(forgeName, func(t *testing.T) {
			fc, first, second := overlappingTracePlans(t, forgeName)
			result, _ := convergeTrace(t, fc, first, nil, nil)
			require.Empty(t, result.Failed(), "results: %+v", result.Results)

			result, client := convergeTrace(t, fc, second, nil, nil)
			failed := result.Failed()
			require.Len(t, failed, 1, "results: %+v", result.Results)
			assert.ErrorContains(t, failed[0].Error, forge.VarTraceSigningKeyID)
			assert.Empty(t, client.log(), "the second install writes nothing")
			assertTraceConverged(t, fc, manifestTrace(t, first.Manifest, "acme/api"))
		})
	}
}

// On GitLab, an install that starts between another install's seed and
// ledger writes waits for that install's project lock, so it cannot write
// its seed in between.
func TestTraceKeys_GitLabInstallBetweenSeedAndLedgerWaits(t *testing.T) {
	shortLease(t, 50*time.Millisecond)
	fc, first, second := overlappingTracePlans(t, ForgeGitLab)
	inner := &traceWriteClient{FakeClient: fc}
	var innerResult *ConvergeBatchResult
	var innerErr error
	outer := &traceWriteClient{FakeClient: fc, beforeLedger: func() {
		innerResult, innerErr = Converge(context.Background(), second, newTestClientFactory(inner), inner.commit(nil), noopProgress)
	}}

	result, err := Converge(context.Background(), first, newTestClientFactory(outer), outer.commit(nil), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed(), "results: %+v", result.Results)
	require.NoError(t, innerErr)
	require.NotNil(t, innerResult, "the second install ran between the first one's seed and ledger writes")
	require.Len(t, innerResult.Failed(), 1, "results: %+v", innerResult.Results)
	assert.ErrorContains(t, innerResult.Failed()[0].Error, "in this process holds the project lock")
	assert.Empty(t, inner.log(), "the second install writes nothing while the first holds the lock")
	assertTraceConverged(t, fc, manifestTrace(t, first.Manifest, "acme/api"))
}

// A GitLab project lease that cannot be released after the key writes is
// reported as a failure before the commit, so a stuck lease is not silent.
// The seed and its ledger were written and stay consistent.
func TestConverge_GitLabTraceKeyLeaseReleaseFailureIsReported(t *testing.T) {
	k := newTraceKey(7)
	fc, cfg := traceConvergeFixture(t, ForgeGitLab, false)
	planTrace(t, &cfg, fc, ForgeGitLab, k)
	fc.Errors["ReleaseProjectLease"] = fmt.Errorf("forge error echoing %s", k.seedB64())
	client := &traceWriteClient{FakeClient: fc}
	progress := &progressLog{}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(client), client.commit(nil), progress.fn)
	require.NoError(t, err)
	failed := result.Failed()
	require.Len(t, failed, 1, "results: %+v", result.Results)
	assert.ErrorContains(t, failed[0].Error, GitLabProjectLeaseVar)
	assert.Equal(t, []string{"secret", "ledger"}, client.log(), "the key is written, and the repository stops before its commit")
	assertTraceConverged(t, fc, manifestTrace(t, cfg.Manifest, "acme/api"))
	for _, s := range traceReported(result, progress) {
		assert.False(t, strings.Contains(s, k.seedB64()), "nothing reported shows the seed")
	}
}

// An install that waits for another installer's GitLab project lease reads
// FULLSEND_TRACE_SIGNING_KEY_ID again once it holds the lease, so a key the
// other installer wrote meanwhile stops it before it writes a key.
func TestConverge_GitLabTraceKeyRereadsLedgerAfterLeaseWait(t *testing.T) {
	shortLease(t, time.Second)
	k, other := newTraceKey(7), newTraceKey(5)
	fc, cfg := traceConvergeFixture(t, ForgeGitLab, false)
	planTrace(t, &cfg, fc, ForgeGitLab, k)
	fc.ProjectLeases["acme/api/"+GitLabProjectLeaseVar] = leaseTestForeignHolder
	client := &traceWriteClient{FakeClient: fc, leaseRefused: func() {
		ctx := context.Background()
		assert.NoError(t, fc.CreateOrUpdateRepoVariable(ctx, "acme", "api", forge.VarTraceSigningKeyID, other.id()))
		assert.NoError(t, fc.ReleaseProjectLease(ctx, "acme", "api", GitLabProjectLeaseVar, leaseTestForeignHolder))
	}}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(client), client.commit(nil), noopProgress)
	require.NoError(t, err)
	failed := result.Failed()
	require.Len(t, failed, 1, "results: %+v", result.Results)
	assert.ErrorContains(t, failed[0].Error, "changed after this run read it")
	assert.Empty(t, client.log(), "nothing is written or committed")
	ledger, _ := traceLedger(fc)
	assert.Equal(t, other.id(), ledger, "the other installer's ledger is left alone")
	assert.Empty(t, fc.ProjectLeases, "no lease is left behind")
}
