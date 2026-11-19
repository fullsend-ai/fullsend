package cli

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/dispatch/cf"
	"github.com/fullsend-ai/fullsend/internal/dispatch/gcf"
)

// fakeCFAPIClient records the Cloudflare API calls of a deploy.
type fakeCFAPIClient struct {
	schedules [][]string
	domains   []string
}

func (f *fakeCFAPIClient) AttachCustomDomain(_ context.Context, _, _, _, hostname string) error {
	f.domains = append(f.domains, hostname)
	return nil
}

func (f *fakeCFAPIClient) RemoveCustomDomain(context.Context, string, string) error { return nil }

func (f *fakeCFAPIClient) LookupZoneID(context.Context, string) (string, error) {
	return "zone-1", nil
}

func (f *fakeCFAPIClient) SetWorkerSchedules(_ context.Context, _, _ string, crons []string) error {
	f.schedules = append(f.schedules, crons)
	return nil
}

func withMintCFAPI(t *testing.T, client *fakeCFAPIClient) {
	t.Helper()
	old := mintCFAPIFactory
	mintCFAPIFactory = func() cf.CloudflareAPIClient { return client }
	t.Cleanup(func() { mintCFAPIFactory = old })
}

// deployOutput runs mint deploy with args and returns its stdout.
func deployOutput(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = runMintDeployArgs(args...) })
	return out, err
}

func runMintDeployArgs(args ...string) error {
	cmd := newRootCmd()
	cmd.SetArgs(append([]string{"mint", "deploy"}, args...))
	return cmd.Execute()
}

func TestMintDeployCmd_CloudflareSetsTickCron(t *testing.T) {
	for flag, want := range map[string][2]string{
		"":                   {"*/5 * * * *", "5m"},
		"--tick-interval=1m": {"* * * * *", "1m"},
		"--tick-interval=3m": {"*/3 * * * *", "3m"},
	} {
		t.Run(flag, func(t *testing.T) {
			withCFEnvVars(t)
			wrangler := &fakeCFWranglerRunner{deployURL: "https://fullsend-mint.workers.dev"}
			withMintCFWrangler(t, wrangler)
			api := &fakeCFAPIClient{}
			withMintCFAPI(t, api)

			args := []string{"--platform=cloudflare", "--source-dir=" + createMinimalWorkerSourceDir(t)}
			if flag != "" {
				args = append(args, flag)
			}
			require.NoError(t, runMintDeployArgs(args...))
			assert.Equal(t, [][]string{{want[0]}}, api.schedules)
			require.NotEmpty(t, wrangler.deployCalls)
			assert.Equal(t, want[1], wrangler.deployCalls[len(wrangler.deployCalls)-1].envVars["FULLSEND_MINT_TICK_INTERVAL"])
		})
	}
}

func TestMintDeployCmd_CloudflarePreviewSetsNoTickCron(t *testing.T) {
	withCFEnvVars(t)
	withMintCFWrangler(t, &fakeCFWranglerRunner{deployURL: "https://bt-1-fullsend-mint.workers.dev"})
	api := &fakeCFAPIClient{}
	withMintCFAPI(t, api)

	require.NoError(t, runMintDeployArgs("--platform=cloudflare", "--preview=bt-1", "--source-dir="+createMinimalWorkerSourceDir(t)))
	assert.Empty(t, api.schedules)
}

func TestMintDeployCmd_TickIntervalValidation(t *testing.T) {
	cases := map[string][]string{
		"invalid value": {"--platform=cloudflare", "--tick-interval=90s"},
		"preview":       {"--platform=cloudflare", "--preview=bt-1", "--tick-interval=2m"},
		"skip-deploy":   {"--platform=gcp", "--project=my-project-id", "--skip-deploy", "--tick-interval=2m"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			withCFEnvVars(t)
			api := &fakeCFAPIClient{}
			withMintCFAPI(t, api)
			err := runMintDeployArgs(args...)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "--tick-interval")
			assert.Empty(t, api.schedules)
		})
	}
}

func TestMintDeployCmd_DryRunShowsMintTimer(t *testing.T) {
	t.Run("cloudflare", func(t *testing.T) {
		withCFEnvVars(t)
		out, err := deployOutput(t, "--platform=cloudflare", "--dry-run", "--tick-interval=2m")
		require.NoError(t, err)
		assert.Contains(t, out, `FULLSEND_MINT_TICK_INTERVAL=2m and the Worker's Cron Trigger to "*/2 * * * *"`)
	})
	t.Run("gcp", func(t *testing.T) {
		out, err := deployOutput(t, "--platform=gcp", "--project=my-project-id", "--dry-run")
		require.NoError(t, err)
		assert.Contains(t, out, gcf.TickServiceAccountEmail("my-project-id"))
		assert.Contains(t, out, `Cloud Scheduler job fullsend-mint-tick in us-central1: POST <function URL>/v1/tick on "*/5 * * * *"`)
	})
	t.Run("gcp skip-deploy", func(t *testing.T) {
		out, err := deployOutput(t, "--platform=gcp", "--project=my-project-id", "--dry-run", "--skip-deploy")
		require.NoError(t, err)
		assert.Contains(t, out, "Would leave the mint timer unchanged (--skip-deploy)")
	})
}

func mintDeleteFakeClient(errs map[string]error) gcf.GCFClient {
	env := map[string]string{"ROLE_APP_IDS": `{"coder":"123"}`, "ALLOWED_ROLES": "coder"}
	return gcf.NewFakeGCFClient(
		gcf.WithFakeFunctionInfo(&gcf.FunctionInfo{State: "ACTIVE", URI: "https://fullsend-mint-abc123.a.run.app", EnvVars: env}),
		gcf.WithFakeTrafficEnvVars(env),
		gcf.WithFakeErrors(errs),
	)
}

func TestMintDeleteGCP_DeletesMintTimerFirst(t *testing.T) {
	client := mintDeleteFakeClient(nil)
	withMintGCFClient(t, client)

	require.NoError(t, runMintDeleteGCP(context.Background(), "test-project1", "us-central1", false, true, os.Stdin))

	calls := gcf.RecordedCalls(client)
	jobAt, fnAt := -1, -1
	for i, c := range calls {
		switch c {
		case "DeleteSchedulerJob":
			jobAt = i
		case "DeleteFunction":
			fnAt = i
		}
	}
	require.GreaterOrEqual(t, jobAt, 0, "DeleteSchedulerJob not called: %v", calls)
	assert.Less(t, jobAt, fnAt, "the job must be deleted before the function")
	// The timer's service account and the mint's are both deleted.
	n := 0
	for _, c := range calls {
		if c == "DeleteServiceAccount" {
			n++
		}
	}
	assert.Equal(t, 2, n)
}

func TestMintDeleteGCP_MintTimerFailureIsAWarning(t *testing.T) {
	client := mintDeleteFakeClient(map[string]error{"DeleteSchedulerJob": errors.New("PERMISSION_DENIED")})
	withMintGCFClient(t, client)

	out := captureStdout(t, func() {
		require.NoError(t, runMintDeleteGCP(context.Background(), "test-project1", "us-central1", false, true, os.Stdin))
	})
	assert.Contains(t, out, "gcloud scheduler jobs delete fullsend-mint-tick --location us-central1")
	assert.Contains(t, gcf.RecordedCalls(client), "DeleteFunction")
}

func TestMintDeleteGCP_DryRunListsMintTimer(t *testing.T) {
	withMintGCFClient(t, mintDeleteFakeClient(nil))
	out := captureStdout(t, func() {
		require.NoError(t, runMintDeleteGCP(context.Background(), "test-project1", "us-central1", true, true, os.Stdin))
	})
	assert.Contains(t, out, "Would delete Cloud Scheduler job: fullsend-mint-tick")
	assert.Contains(t, out, gcf.TickServiceAccountEmail("test-project1"))
}
