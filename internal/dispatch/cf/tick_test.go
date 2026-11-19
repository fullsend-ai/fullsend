package cf

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain keeps every test in this package off the Cloudflare API: a
// provisioner without SetCloudflareAPI gets a recording fake instead of
// the live client (which would also fall back to `wrangler auth token`).
func TestMain(m *testing.M) {
	newCloudflareAPIClient = func() CloudflareAPIClient { return &fakeCloudflareAPIClient{} }
	os.Exit(m.Run())
}

func TestProvisioner_Provision_DurableSetsTickCron(t *testing.T) {
	cases := map[time.Duration]struct{ cron, env string }{
		0:               {"*/5 * * * *", "5m"},
		time.Minute:     {"* * * * *", "1m"},
		2 * time.Minute: {"*/2 * * * *", "2m"},
		5 * time.Minute: {"*/5 * * * *", "5m"},
	}
	for interval, want := range cases {
		t.Run(interval.String(), func(t *testing.T) {
			stubWASMBuild(t)
			fake := &fakeWranglerRunner{}
			api := &fakeCloudflareAPIClient{}
			p := NewProvisioner(Config{
				AccountID:    "abc123",
				WorkerName:   "test-mint",
				DeployMode:   DeployDurable,
				SourceDir:    createFakeWorkerSourceDir(t),
				TickInterval: interval,
			}, fake)
			p.SetCloudflareAPI(api)

			_, err := p.Provision(context.Background())
			require.NoError(t, err)

			require.Len(t, fake.deployCalls, 1)
			assert.Equal(t, want.env, fake.deployCalls[0].envVars["FULLSEND_MINT_TICK_INTERVAL"])
			require.Len(t, api.scheduleCalls, 1)
			assert.Equal(t, scheduleCall{accountID: "abc123", workerName: "test-mint", crons: []string{want.cron}}, api.scheduleCalls[0])
		})
	}
}

func TestProvisioner_Provision_PreviewSetsNoTickCron(t *testing.T) {
	stubWASMBuild(t)
	notExists := false
	fake := &fakeWranglerRunner{workerExists: &notExists}
	api := &fakeCloudflareAPIClient{}
	p := NewProvisioner(Config{
		AccountID:    "abc123",
		WorkerName:   "test-mint",
		DeployMode:   DeployPreview,
		PreviewAlias: "bt-run-1",
		SourceDir:    createFakeWorkerSourceDir(t),
		EnvVars:      map[string]string{"ROLE_APP_IDS": `{"fullsend":"1"}`},
	}, fake)
	p.SetCloudflareAPI(api)

	_, err := p.Provision(context.Background())
	require.NoError(t, err)

	// The bootstrap durable deploy and the preview upload both run, and
	// neither sets the cron or the interval.
	require.Len(t, fake.deployCalls, 2)
	for _, c := range fake.deployCalls {
		assert.NotContains(t, c.envVars, "FULLSEND_MINT_TICK_INTERVAL")
	}
	assert.Empty(t, api.scheduleCalls)
}

func TestProvisioner_Provision_TickCronError(t *testing.T) {
	stubWASMBuild(t)
	api := &fakeCloudflareAPIClient{scheduleErr: errors.New("Cloudflare API returned 403")}
	p := NewProvisioner(Config{
		AccountID:  "abc123",
		WorkerName: "test-mint",
		DeployMode: DeployDurable,
		SourceDir:  createFakeWorkerSourceDir(t),
	}, &fakeWranglerRunner{})
	p.SetCloudflareAPI(api)

	_, err := p.Provision(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setting the mint timer Cron Trigger")
	assert.Contains(t, err.Error(), "403")
}

func TestProvisioner_Validate_TickInterval(t *testing.T) {
	for name, cfg := range map[string]Config{
		"preview":      {AccountID: "a", WorkerName: "test-mint", DeployMode: DeployPreview, PreviewAlias: "p1", TickInterval: 5 * time.Minute},
		"too short":    {AccountID: "a", WorkerName: "test-mint", TickInterval: 30 * time.Second},
		"too long":     {AccountID: "a", WorkerName: "test-mint", TickInterval: 10 * time.Minute},
		"not a minute": {AccountID: "a", WorkerName: "test-mint", TickInterval: 90 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeWranglerRunner{}
			_, err := NewProvisioner(cfg, fake).Provision(context.Background())
			require.Error(t, err)
			assert.Empty(t, fake.deployCalls)
		})
	}
}

func TestLiveCloudflareAPIClient_SetWorkerSchedules(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody []map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.EscapedPath()
		data, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(data, &gotBody))
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":true,"result":{"schedules":[{"cron":"*/5 * * * *"}]}}`))
	}))
	defer srv.Close()

	err := newTestClient(t, srv).SetWorkerSchedules(context.Background(), "acct/1", "fullsend-mint", []string{"*/5 * * * *"})
	require.NoError(t, err)
	assert.Equal(t, http.MethodPut, gotMethod)
	assert.Equal(t, "/accounts/acct%2F1/workers/scripts/fullsend-mint/schedules", gotPath)
	assert.Equal(t, []map[string]string{{"cron": "*/5 * * * *"}}, gotBody)
}

func TestLiveCloudflareAPIClient_SetWorkerSchedules_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"success":false,"errors":[{"message":"invalid cron"}]}`))
	}))
	defer srv.Close()

	err := newTestClient(t, srv).SetWorkerSchedules(context.Background(), "acct", "fullsend-mint", []string{"bad"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setting Cron Triggers of Worker fullsend-mint")
	assert.Contains(t, err.Error(), "invalid cron")
}
