package steps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// fakeVaultServer records the calls the Vault steps make.
type fakeVaultServer struct {
	mu      sync.Mutex
	allowed []string
	secrets map[string]map[string]string
	stops   int
	stopErr error
	putErr  error
}

func (f *fakeVaultServer) Address() string { return "https://bao.trycloudflare.com" }

func (f *fakeVaultServer) AllowRepo(_ context.Context, org, repo string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allowed = append(f.allowed, org+"/"+repo)
	return nil
}

func (f *fakeVaultServer) PutSecret(_ context.Context, name string, data map[string]string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return "", f.putErr
	}
	if f.secrets == nil {
		f.secrets = map[string]map[string]string{}
	}
	f.secrets[name] = data
	return "secret/data/fullsend-bt/" + name, nil
}

func (f *fakeVaultServer) Stop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	return f.stopErr
}

// fakeVaultSCM holds files by path and records commits and PRs.
type fakeVaultSCM struct {
	scm.Driver
	files      map[string][]byte
	branchFile map[string][]byte
	commitErr  error
}

func (f *fakeVaultSCM) GetFileContent(_ context.Context, _, _, path string) ([]byte, error) {
	if d, ok := f.files[path]; ok {
		return d, nil
	}
	return nil, forge.ErrNotFound
}

func (f *fakeVaultSCM) CommitFile(_ context.Context, _, _, path, _ string, content []byte) error {
	if f.commitErr != nil {
		return f.commitErr
	}
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	f.files[path] = content
	return nil
}

func (f *fakeVaultSCM) CreateBranch(context.Context, string, string, string) error { return nil }

func (f *fakeVaultSCM) CommitFileToBranch(_ context.Context, _, _, branch, path, _ string, content []byte) error {
	if f.branchFile == nil {
		f.branchFile = map[string][]byte{}
	}
	f.branchFile[path] = content
	return nil
}

func (f *fakeVaultSCM) CreateChangeProposal(context.Context, string, string, string, string, string, string) (*forge.ChangeProposal, error) {
	return &forge.ChangeProposal{Number: 7}, nil
}

// fakeTraceDriver is an install.Driver with a trace inspector.
type fakeTraceDriver struct {
	install.Driver
	running bool
	headers string
	counts  []int
	err     error
	calls   int
}

func (d *fakeTraceDriver) TraceCollectorRunning() bool   { return d.running }
func (d *fakeTraceDriver) TraceCollectorHeaders() string { return d.headers }
func (d *fakeTraceDriver) TracesContaining(time.Time, ...string) (int, error) {
	d.calls++
	if d.err != nil {
		return 0, d.err
	}
	if len(d.counts) == 0 {
		return 0, nil
	}
	n := d.counts[0]
	if len(d.counts) > 1 {
		d.counts = d.counts[1:]
	}
	return n, nil
}

func withVaultRegistry(t *testing.T, start vaultServerStarter) *vaultServerRegistry {
	t.Helper()
	prev := vaultServers
	vaultServers = newVaultServerRegistry(start)
	t.Cleanup(func() { vaultServers = prev })
	return vaultServers
}

func vaultWorld(srv install.VaultServer, s scm.Driver) *world.World {
	return &world.World{
		Org: "bt-org", RepoName: "repo-1", RepoOwner: "bt-org",
		SCM: s, VaultServer: srv, VaultScenarioID: "s-1",
		Logf: func(string, ...any) {},
	}
}

func TestVaultFingerprintMatchesScript(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("sha256sum not available")
	}
	script := filepath.Join(t.TempDir(), "fp.sh")
	require.NoError(t, os.WriteFile(script, []byte(vaultFingerprintScript), 0o700))
	cmd := exec.Command(bash, script)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "JIRA_TOKEN=behaviour-vault-abc", vaultBundleEntry + "=entry-value"}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), vaultFingerprintLine("JIRA_TOKEN", "behaviour-vault-abc"))
	assert.Contains(t, string(out), vaultFingerprintLine(vaultBundleEntry, "entry-value"))
	assert.Contains(t, string(out), "vault-fingerprint OTEL_EXPORTER_OTLP_HEADERS unset")
	assert.NotContains(t, string(out), "behaviour-vault-abc", "the value itself is never printed")
	assert.NotContains(t, string(out), "vault-bundle-visible-to-script")

	cmd = exec.Command(bash, script)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "FULLSEND_GITHUB_RUNNER_SECRETS={}"}
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Contains(t, string(out), "vault-bundle-visible-to-script")
}

func TestVaultServerRegistry(t *testing.T) {
	srv := &fakeVaultServer{}
	starts := 0
	r := newVaultServerRegistry(func(func(string, ...any)) (install.VaultServer, string, error) {
		starts++
		return srv, "", nil
	})
	a, _, err := r.forFeature("features/a.feature", nil)
	require.NoError(t, err)
	b, _, err := r.forFeature("features/a.feature", nil)
	require.NoError(t, err)
	assert.Same(t, a, b)
	assert.Equal(t, 1, starts, "a feature's scenarios share one server")
	_, _, err = r.forFeature("features/b.feature", nil)
	require.NoError(t, err)
	assert.Equal(t, 2, starts, "each feature has its own server")

	require.NoError(t, r.stopAll(context.Background()))
	assert.Equal(t, 2, srv.stops)
	require.NoError(t, r.stopAll(context.Background()))
	assert.Equal(t, 2, srv.stops, "a stopped server is not stopped again")
}

func TestVaultServerRegistry_StartErrorAndRetryStop(t *testing.T) {
	partial := &fakeVaultServer{stopErr: errors.New("busy")}
	starts := 0
	r := newVaultServerRegistry(func(func(string, ...any)) (install.VaultServer, string, error) {
		starts++
		return partial, "", errors.New("tunnel failed")
	})
	var logs []string
	logf := func(f string, a ...any) { logs = append(logs, f) }
	_, _, err := r.forFeature("f", logf)
	require.ErrorContains(t, err, "tunnel failed")
	_, _, err = r.forFeature("f", logf)
	require.ErrorContains(t, err, "tunnel failed")
	assert.Equal(t, 1, starts, "a failed start is not retried by every scenario")
	assert.NotEmpty(t, logs)

	require.ErrorContains(t, r.stopAll(context.Background()), "busy")
	partial.stopErr = nil
	require.NoError(t, r.stopAll(context.Background()))
	assert.Equal(t, 2, partial.stops, "a failed Stop is retried")
}

func TestGivenVaultServer(t *testing.T) {
	t.Run("skip", func(t *testing.T) {
		withVaultRegistry(t, func(func(string, ...any)) (install.VaultServer, string, error) {
			return nil, "podman not found", nil
		})
		w := &world.World{ScenarioName: "s", Logf: func(string, ...any) {}}
		require.ErrorIs(t, givenVaultServer(w), godog.ErrSkip)
		assert.Nil(t, w.VaultServer)
	})
	t.Run("error", func(t *testing.T) {
		withVaultRegistry(t, func(func(string, ...any)) (install.VaultServer, string, error) {
			return nil, "", errors.New("boom")
		})
		require.ErrorContains(t, givenVaultServer(&world.World{}), "boom")
	})
	t.Run("started", func(t *testing.T) {
		srv := &fakeVaultServer{}
		withVaultRegistry(t, func(func(string, ...any)) (install.VaultServer, string, error) {
			return srv, "", nil
		})
		w := &world.World{ScenarioURI: "features/platform/vault.feature", Logf: func(string, ...any) {}}
		require.NoError(t, givenVaultServer(w))
		assert.Same(t, srv, w.VaultServer)
		assert.Regexp(t, `^s-[0-9a-f]{16}$`, w.VaultScenarioID)
		require.NoError(t, StopVaultServers(context.Background()))
		assert.Equal(t, 1, srv.stops)
	})
}

func TestVaultStepsRequireServerAndRepo(t *testing.T) {
	ctx := context.Background()
	require.ErrorContains(t, givenVaultAllowsRepository(ctx, &world.World{}), "no Vault test server")
	require.ErrorContains(t, givenVaultAllowsRepository(ctx, &world.World{VaultServer: &fakeVaultServer{}}), "no repo configured")
	require.ErrorContains(t, givenVaultSyntheticSecret(ctx, &world.World{}, "JIRA_TOKEN"), "no Vault test server")
	require.ErrorContains(t, givenVaultTraceCollectorHeaders(ctx, &world.World{}), "no Vault test server")
	require.ErrorContains(t, givenTrustedVaultSelection(&world.World{}, "JIRA_TOKEN", false), "no Vault test server")
	require.ErrorContains(t, givenUntrustedVaultOverridePR(ctx, &world.World{}, "JIRA_TOKEN"), "no Vault test server")
	require.ErrorContains(t, givenNativeTracesHeadersWrong(&world.World{}), "no repo configured")
	require.ErrorContains(t, givenNativeTracesHeadersWrong(&world.World{Org: "o", RepoName: "r", SCM: &fakeVaultSCM{}}), "GitHub SCM driver")

	srv := &fakeVaultServer{}
	w := vaultWorld(srv, &fakeVaultSCM{})
	require.NoError(t, givenVaultAllowsRepository(ctx, w))
	assert.Equal(t, []string{"bt-org/repo-1"}, srv.allowed)
}

func TestSyntheticVaultValue(t *testing.T) {
	v, entry, err := syntheticVaultValue("JIRA_TOKEN")
	require.NoError(t, err)
	assert.Regexp(t, `^behaviour-vault-[0-9a-f]{32}$`, v)
	assert.Empty(t, entry)

	for _, name := range []string{"OTEL_EXPORTER_OTLP_HEADERS", "OTEL_EXPORTER_OTLP_TRACES_HEADERS"} {
		v, _, err := syntheticVaultValue(name)
		require.NoError(t, err)
		assert.Regexp(t, `^x-behaviour-vault=[0-9a-f]{32}$`, v)
	}

	v, entry, err = syntheticVaultValue(forge.SecretGitHubRunnerSecrets)
	require.NoError(t, err)
	var bundle map[string]string
	require.NoError(t, json.Unmarshal([]byte(v), &bundle))
	assert.Equal(t, map[string]string{vaultBundleEntry: entry}, bundle)
}

func TestGivenVaultSyntheticSecretAndSelection(t *testing.T) {
	ctx := context.Background()
	srv := &fakeVaultServer{}
	s := &fakeVaultSCM{files: map[string][]byte{".fullsend/config.base.yaml": []byte("kill_switch: false\n")}}
	w := vaultWorld(srv, s)

	require.ErrorContains(t, givenTrustedVaultSelection(w, "JIRA_TOKEN", false), "stored no Vault secret")
	require.NoError(t, givenVaultSyntheticSecret(ctx, w, "JIRA_TOKEN"))
	require.NoError(t, givenVaultSyntheticSecret(ctx, w, forge.SecretGitHubRunnerSecrets))
	jira := w.VaultSecrets["JIRA_TOKEN"]
	assert.Equal(t, "secret/data/fullsend-bt/s-1/trusted-jira_token", jira.Path)
	assert.Equal(t, map[string]string{"JIRA_TOKEN": jira.Value}, srv.secrets["s-1/trusted-jira_token"])
	assert.Contains(t, w.VaultSecrets, vaultBundleEntry)

	require.NoError(t, givenTrustedVaultSelection(w, "JIRA_TOKEN", false))
	require.NoError(t, givenTrustedVaultSelection(w, forge.SecretGitHubRunnerSecrets, true))
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal(s.files[".fullsend/config.base.yaml"], &doc))
	assert.Equal(t, false, doc["kill_switch"], "other keys are kept")
	assert.Equal(t, map[string]any{"bt-openbao": map[string]any{"kind": "vault", "vault_address": "https://bao.trycloudflare.com"}}, doc["connections"])
	secrets := doc["secrets"].(map[string]any)
	assert.Equal(t, map[string]any{"connection": "bt-openbao", "vault_secrets": []any{jira.Path}}, secrets["JIRA_TOKEN"])
	assert.Equal(t, map[string]any{"connection": "bt-openbao", "vault_secrets": []any{"secret/data/fullsend-bt/s-1/missing-fullsend_github_runner_secrets"}},
		secrets[forge.SecretGitHubRunnerSecrets])

	srv.putErr = errors.New("denied")
	require.ErrorContains(t, givenVaultSyntheticSecret(ctx, w, "JIRA_TOKEN"), "denied")
	s.commitErr = errors.New("conflict")
	require.ErrorContains(t, givenTrustedVaultSelection(w, "JIRA_TOKEN", false), "conflict")
}

func TestWithVaultSelection_Errors(t *testing.T) {
	_, err := withVaultSelection([]byte("connections: [1]\n"), "https://a", "JIRA_TOKEN", "p")
	require.ErrorContains(t, err, "connections is not a mapping")
	_, err = withVaultSelection([]byte("secrets: x\n"), "https://a", "JIRA_TOKEN", "p")
	require.ErrorContains(t, err, "secrets is not a mapping")
	_, err = withVaultSelection([]byte(":\n\t- bad"), "https://a", "JIRA_TOKEN", "p")
	require.ErrorContains(t, err, "parsing config")
	out, err := withVaultSelection([]byte("# empty\n"), "https://a", "JIRA_TOKEN", "p")
	require.NoError(t, err)
	assert.Contains(t, string(out), "bt-openbao")
}

func TestGivenUntrustedVaultOverridePR(t *testing.T) {
	ctx := context.Background()
	srv := &fakeVaultServer{}
	s := &fakeVaultSCM{files: map[string][]byte{".fullsend/config.yaml": []byte("agents: []\n")}}
	w := vaultWorld(srv, s)
	require.NoError(t, givenUntrustedVaultOverridePR(ctx, w, "JIRA_TOKEN"))
	assert.Equal(t, 7, w.PRNumber)
	assert.Equal(t, []int{7}, w.CreatedPRNumbers)
	require.Len(t, w.CreatedBranches, 1)
	override := w.VaultOverrideValues["JIRA_TOKEN"]
	assert.Equal(t, map[string]string{"JIRA_TOKEN": override}, srv.secrets["s-1/override-jira_token"])
	head := string(s.branchFile[".fullsend/config.yaml"])
	assert.Contains(t, head, "secret/data/fullsend-bt/s-1/override-jira_token")
	assert.Contains(t, head, "agents: []")

	require.ErrorContains(t, givenUntrustedVaultOverridePR(ctx, vaultWorld(srv, &fakeVaultSCM{}), "JIRA_TOKEN"), "reading config")
}

func TestGivenVaultTraceCollectorHeaders(t *testing.T) {
	ctx := context.Background()
	srv := &fakeVaultServer{}
	w := vaultWorld(srv, &fakeVaultSCM{})
	require.ErrorIs(t, givenVaultTraceCollectorHeaders(ctx, w), godog.ErrSkip, "no inspector")
	w.Driver = &fakeTraceDriver{}
	require.ErrorIs(t, givenVaultTraceCollectorHeaders(ctx, w), godog.ErrSkip, "collector not running")
	w.Driver = &fakeTraceDriver{running: true, headers: "Authorization=Bearer%20tok-123456789012345678"}
	require.NoError(t, givenVaultTraceCollectorHeaders(ctx, w))
	assert.Equal(t, "Authorization=Bearer%20tok-123456789012345678", w.VaultSecrets[vaultTracesHeadersName].Value)
}

func TestThenTraceCollectorReceived(t *testing.T) {
	prevWait, prevTick := vaultTraceWait, vaultTracePollTick
	t.Cleanup(func() { vaultTraceWait, vaultTracePollTick = prevWait, prevTick })
	vaultTraceWait, vaultTracePollTick = 50*time.Millisecond, time.Millisecond
	ctx := context.Background()

	w := vaultWorld(nil, nil)
	require.ErrorContains(t, thenTraceCollectorReceived(ctx, w, "a"), "no trace collector")
	d := &fakeTraceDriver{running: true, counts: []int{0, 0, 1}}
	w.Driver = d
	require.ErrorContains(t, thenTraceCollectorReceived(ctx, w, "a"), "no workflow trigger time")
	w.ScenarioStart = time.Now()
	require.NoError(t, thenTraceCollectorReceived(ctx, w, "a"))
	assert.Equal(t, 3, d.calls)

	w.Driver = &fakeTraceDriver{running: true}
	err := thenTraceCollectorReceived(ctx, w, "a")
	require.ErrorContains(t, err, "received no trace")
	assert.Contains(t, err.Error(), "openbao")

	w.Driver = &fakeTraceDriver{running: true, err: errors.New("io")}
	require.ErrorContains(t, thenTraceCollectorReceived(ctx, w, "a"), "io")

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	vaultTraceWait = time.Minute
	w.Driver = &fakeTraceDriver{running: true}
	require.ErrorIs(t, thenTraceCollectorReceived(cctx, w, "a"), context.Canceled)
}

func TestVaultFingerprintStepsRequireRecordedValues(t *testing.T) {
	ctx := context.Background()
	w := &world.World{}
	require.ErrorContains(t, thenVaultFingerprintInLogs(ctx, w, "a", "JIRA_TOKEN"), "stored no Vault secret")
	require.ErrorContains(t, thenVaultFingerprintNotInLogsAs(ctx, w, "a", "JIRA_TOKEN", "X"), "stored no Vault secret")
	require.ErrorContains(t, thenVaultOverrideFingerprintNotInLogs(ctx, w, "a", "JIRA_TOKEN"), "no pull request overriding")
	assert.True(t, strings.HasPrefix(vaultFingerprintLine("A", "v"), "vault-fingerprint A="))
	assert.Len(t, vaultFingerprint("v"), 16)
}
