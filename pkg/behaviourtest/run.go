//go:build behaviour

package behaviourtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/google/uuid"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/env"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/suite"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

const fullsendModulePath = "github.com/fullsend-ai/fullsend"

// RunSuite bootstraps and executes the behaviour test suite.
//
// It loads runner and e2e config from the environment, counts the
// scenario instances the run will execute, acquires as many pool orgs
// as the provisioning instances need (at most maxProvisioningPerOrg per
// DEV org, fewer when an org's GitHub primary budget is low; STAGE uses
// its single org for everything), sets each org up concurrently with its
// own token, install driver and SCM/CI drivers, and runs one godog suite
// whose Before hook routes every scenario to an org. It builds the
// fullsend CLI from module github.com/fullsend-ai/fullsend and shares
// step registration with external consumers. Callers supply only
// FeaturePaths and FixturesRoot; tags, concurrency, and driver selection
// continue to come from env vars (GODOG_TAGS, GODOG_CONCURRENCY,
// BEHAVIOUR_*, ENVIRONMENT). By default each acquired org runs up to its
// driver capacity (12 repo slots) of scenarios at once; an explicit
// GODOG_CONCURRENCY bounds the active scenarios across all orgs together.
//
// With BEHAVIOUR_SHARD_INDEX and BEHAVIOUR_SHARD_TOTAL set (a DEV CI
// matrix job), the run plans the selected instances into shards exactly
// as PlanSuiteShards does, executes only its own shard's instances,
// sizes its org acquisition from them, defaults to at most 12 concurrent
// scenarios, and fails if it did not start exactly its planned
// instances. Each run logs and writes per-shard timing and quota
// metrics to BEHAVIOUR_ARTIFACT_DIR.
//
// The test file that calls RunSuite must be built with `-tags behaviour`.
func RunSuite(t *testing.T, opts SuiteOptions) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping behaviour tests in short mode")
	}
	if err := opts.validate(); err != nil {
		t.Fatalf("invalid suite options: %v", err)
	}

	cfg := env.LoadRunnerConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid behaviour runner config: %v", err)
	}

	e2eCfg := e2etest.LoadEnvConfig(t)
	ctx := context.Background()

	metrics := newSuiteMetrics(nil)
	defer func() {
		if err := metrics.write(os.Getenv("BEHAVIOUR_ARTIFACT_DIR"), t.Logf); err != nil {
			t.Logf("[shard-metrics] %v", err)
		}
	}()

	policy, err := quotaPolicyFromEnv()
	if err != nil {
		t.Fatal(err)
	}

	// base carries everything that does not depend on the org. Tag skips
	// are evaluated against it, and each org's template is cloned from it.
	base := &world.World{
		Config:       cfg,
		Logf:         t.Logf,
		FixturesRoot: opts.FixturesRoot,
	}

	tagFilter := os.Getenv("GODOG_TAGS")
	instances, err := loadScenarioInstances(opts.FeaturePaths, tagFilter)
	if err != nil {
		t.Fatalf("reading feature files: %v", err)
	}
	skip := func(tags []string) error {
		return suite.SkipErrorForTagNames(tags, base)
	}
	all := summarizeDemand(instances, skip)
	t.Logf("[org-shard] %d scenario instance(s) selected, %d skipped by compatibility tags, %d will run, %d provision a fresh repository",
		all.Selected, all.Skipped, all.Eligible, all.Provisioning)

	shard, err := resolveRunShard(cfg, instances, skip)
	if err != nil {
		t.Fatal(err)
	}
	demand := shard.Demand
	if shard.Matrix {
		metrics.setShard(shard.Spec, shard.Planned)
	} else {
		metrics.setPlanned(shard.Planned)
	}
	if shard.Sharded {
		t.Logf("[shard] shard %s runs %d of the %d selected scenario instance(s) (%d provisioning, %d other); the other shards run the rest",
			shard.Spec, len(shard.Planned.Instances), all.Selected, shard.Planned.Provisioning, shard.Planned.Other)
	}

	// Build from the fullsend module, not the caller's module root, so
	// external consumers (fullsend-ai/agents) get this repo's cmd/fullsend.
	buildStart := time.Now()
	binary := e2etest.BuildModuleBinary(t, fullsendModulePath)
	metrics.setBuild(time.Since(buildStart))
	base.CLIBinary = binary

	resolveActors(ctx, t, cfg, base)

	runID := uuid.New().String()
	sharded := &shardedSuite{
		Deps: orgPoolDeps{
			Acquire: func(ctx context.Context, pool []string, wait bool) (string, string, error) {
				timeout := time.Duration(0)
				if wait {
					timeout = e2eCfg.LockTimeout
				}
				return e2etest.AcquireOrg(ctx, e2eCfg, runID, pool, timeout, t.Logf)
			},
			Release: func(org, token string) {
				e2etest.ReleaseLock(context.Background(), e2etest.NewLiveClient(token), org, runID, t)
			},
			SampleQuota: func(ctx context.Context, org, token string) (forge.RateLimit, bool) {
				return e2etest.SampleRateLimit(ctx, e2etest.NewLiveClient(token), org)
			},
			Logf: t.Logf,
		},
		Config: orgPlanConfig{
			Pool:      orgPoolForEnvironment(cfg.Environment),
			SingleOrg: cfg.Environment == "stage",
			PerOrgCap: maxProvisioningPerOrg,
			Policy:    policy,

			ReserveTimeout: e2eCfg.LockTimeout,
			RetryBackoff:   reserveRetryBackoff,
		},
		Setup: func(ctx context.Context, lease orgLease, st *shardSetup) error {
			org, token := lease.Org, lease.Token
			logf := orgLogf(t.Logf, org)
			client := e2etest.NewLiveClient(token)

			e2etest.CleanupStaleResources(ctx, client, token, org, t)

			driver, err := installFactoryFor(cfg.Environment)(org, client, token, binary, e2eCfg.GCPProjectID, logf)
			if err != nil {
				return fmt.Errorf("creating install driver (env=%s): %w", cfg.Environment, err)
			}
			// Registered in st at once, even if a later step fails or
			// panics, so Run finalizes the driver (mint teardown) before
			// releasing the org's lock.
			st.Driver = driver
			st.Quota = client.RateLimit

			scmDriver, err := newSCMDriver(cfg.SCM, client)
			if err != nil {
				return err
			}
			ciDriver, err := newCIDriver(cfg.CI, client, token)
			if err != nil {
				return err
			}

			template := base.Clone()
			template.SCM = scmDriver
			template.CI = ciDriver
			template.Driver = driver
			template.Org = org
			template.Token = token
			template.RepoOwner = org
			template.Logf = logf
			st.Template = template
			return nil
		},
		Metrics: metrics,
	}

	err = sharded.Run(ctx, demand, func(assigner *shardAssigner) error {
		concurrency := 1
		if capacity := assigner.driverCapacity(); capacity > 0 {
			n, overCapacity, cerr := shardConcurrency(os.Getenv("GODOG_CONCURRENCY"), capacity, shard.Sharded)
			if cerr != nil {
				return cerr
			}
			concurrency = n
			if overCapacity {
				t.Logf("WARNING: GODOG_CONCURRENCY=%d exceeds the orgs' combined driver capacity %d; excess workers will block in AllocateRepo", concurrency, capacity)
			}
		}
		t.Logf("[org-shard] running with %d concurrent scenario(s) across all orgs", concurrency)
		metrics.setConcurrency(concurrency)

		router := assigner.Router()
		gate := shard.scenarioGate()
		suiteRunner := godog.TestSuite{
			Name: "behaviour",
			ScenarioInitializer: func(sc *godog.ScenarioContext) {
				suite.InitGatedRoutedScenario(sc, base, gate, router)
				registerMetricsHooks(sc, metrics)
			},
			Options: &godog.Options{
				Format:      "pretty",
				Paths:       opts.FeaturePaths,
				TestingT:    t,
				Tags:        tagFilter,
				Concurrency: concurrency,
			},
		}
		st := suiteRunner.Run()
		// A shard that did not run exactly its planned scenarios fails
		// even when every scenario it ran passed: coverage was lost.
		verifyErr := shard.verify()
		if st != 0 {
			return errors.Join(fmt.Errorf("behaviour suite failed with status %d", st), verifyErr)
		}
		return verifyErr
	})
	if err != nil {
		t.Fatal(err)
	}
}

// resolveActors sets the org-independent test actor drivers (outsider,
// write, triage) on base from their PATs, when set.
func resolveActors(ctx context.Context, t *testing.T, cfg env.RunnerConfig, base *world.World) {
	t.Helper()
	if outsiderPAT := os.Getenv("TEST_ACTOR_OUTSIDER_PAT"); outsiderPAT != "" {
		outsiderClient := e2etest.NewLiveClient(outsiderPAT)
		outsiderSCM, err := newSCMDriver(cfg.SCM, outsiderClient)
		if err != nil {
			t.Fatal(err)
		}
		base.OutsiderSCM = outsiderSCM
		login, err := outsiderClient.GetAuthenticatedUser(ctx)
		if err != nil {
			t.Fatalf("resolving outsider login: %v", err)
		}
		base.OutsiderLogin = login
	}

	if writePAT := os.Getenv("TEST_ACTOR_WRITE_PAT"); writePAT != "" {
		writeClient := e2etest.NewLiveClient(writePAT)
		writeSCM, err := newSCMDriver(cfg.SCM, writeClient)
		if err != nil {
			t.Fatal(err)
		}
		base.WriteSCM = writeSCM
		base.WriteToken = writePAT
		login, err := writeClient.GetAuthenticatedUser(ctx)
		if err != nil {
			t.Fatalf("resolving write actor login: %v", err)
		}
		base.WriteLogin = login
	}

	if triagePAT := os.Getenv("TEST_ACTOR_TRIAGE_PAT"); triagePAT != "" {
		triageClient := e2etest.NewLiveClient(triagePAT)
		triageSCM, err := newSCMDriver(cfg.SCM, triageClient)
		if err != nil {
			t.Fatal(err)
		}
		base.TriageSCM = triageSCM
		login, err := triageClient.GetAuthenticatedUser(ctx)
		if err != nil {
			t.Fatalf("resolving triage actor login: %v", err)
		}
		base.TriageLogin = login
	}
}
