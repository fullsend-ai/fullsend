package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge/jira"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/sandbox"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/security"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	testJiraClientID     = "jira-oauth-client-id-0001"
	testJiraClientSecret = "jira-oauth-client-secret-never-leaves-the-runner"
)

func testJiraCreds() jira.Credentials {
	return jira.Credentials{ClientID: testJiraClientID, ClientSecret: testJiraClientSecret}
}

func jiraEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// unsetJiraToken leaves FULLSEND_JIRA_TOKEN unset for the test, as the
// composite action does when the repository has no token secret.
func unsetJiraToken(t *testing.T) {
	t.Helper()
	t.Setenv(jira.TokenEnvVar, "")
	require.NoError(t, os.Unsetenv(jira.TokenEnvVar))
}

func TestJiraMintCredentials(t *testing.T) {
	unsetJiraToken(t)
	t.Setenv("JIRA_TOKEN_WS", "  \n")
	t.Setenv("JIRA_TOKEN_STATIC", "static-jira-token-value")
	both := map[string]string{jira.ClientIDEnvVar: testJiraClientID, jira.ClientSecretEnvVar: testJiraClientSecret}
	def := func(v string) harness.ProviderDef {
		return harness.ProviderDef{Name: "jira", Type: jiraProviderType, Credentials: map[string]string{jiraCredentialKey: v}}
	}

	cases := []struct {
		name    string
		pd      harness.ProviderDef
		env     map[string]string
		mint    bool
		wantErr string
	}{
		{"static token: unchanged", def("${JIRA_TOKEN_STATIC}"), both, false, ""},
		{"no token, client credentials: mint", def("${FULLSEND_JIRA_TOKEN}"), both, true, ""},
		{"whitespace token counts as empty", def("${JIRA_TOKEN_WS}"), both, true, ""},
		{"literal empty value, client credentials: mint", def(""), both, true, ""},
		{"no credential at all: unchanged", def("${FULLSEND_JIRA_TOKEN}"), map[string]string{}, false, ""},
		{"whitespace client credentials count as unset", def("${FULLSEND_JIRA_TOKEN}"), map[string]string{jira.ClientIDEnvVar: " ", jira.ClientSecretEnvVar: "\n"}, false, ""},
		{"only the client ID", def("${FULLSEND_JIRA_TOKEN}"), map[string]string{jira.ClientIDEnvVar: testJiraClientID}, false, "must both be set"},
		{"only the client secret", def("${FULLSEND_JIRA_TOKEN}"), map[string]string{jira.ClientSecretEnvVar: testJiraClientSecret}, false, "must both be set"},
		{"no JIRA_TOKEN declared: unchanged", harness.ProviderDef{Name: "jira", Type: jiraProviderType, Credentials: map[string]string{"OTHER": ""}}, both, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			creds, mint, err := jiraMintCredentials(tc.pd, jiraEnv(tc.env))
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				assert.NotContains(t, err.Error(), testJiraClientSecret)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.mint, mint)
			if mint {
				assert.Equal(t, testJiraCreds(), creds)
			} else {
				assert.Equal(t, jira.Credentials{}, creds)
			}
		})
	}

	t.Run("builtin definition with FULLSEND_JIRA_TOKEN set: static", func(t *testing.T) {
		t.Setenv(jira.TokenEnvVar, "static-jira-token-value")
		data, err := scaffold.FullsendRepoFile("providers/jira.yaml")
		require.NoError(t, err)
		pd, err := harness.ParseProviderDef(data)
		require.NoError(t, err)
		_, mint, err := jiraMintCredentials(pd, jiraEnv(both))
		require.NoError(t, err)
		assert.False(t, mint, "a static token wins")
	})
	t.Run("builtin definition with only client credentials: mint", func(t *testing.T) {
		data, err := scaffold.FullsendRepoFile("providers/jira.yaml")
		require.NoError(t, err)
		pd, err := harness.ParseProviderDef(data)
		require.NoError(t, err)
		_, mint, err := jiraMintCredentials(pd, jiraEnv(both))
		require.NoError(t, err)
		assert.True(t, mint)
	})
}

// jiraProviderCalls records what the provider seams were asked to do.
type jiraProviderCalls struct {
	mu      sync.Mutex
	creates []map[string]string
	updates []jiraUpdateCall
	expiry  []time.Time
	mints   atomic.Int32
}

type jiraUpdateCall struct {
	name      string
	values    map[string]string
	expiresAt time.Time
}

func (c *jiraProviderCalls) updateList() []jiraUpdateCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]jiraUpdateCall(nil), c.updates...)
}

func (c *jiraProviderCalls) expiryList() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Time(nil), c.expiry...)
}

// stubJiraProvider replaces the mint and provider seams. mint returns
// tokens named jira-minted-access-token-N; mintErr, when it returns
// non-nil for an attempt number (1-based), fails that mint instead.
func stubJiraProvider(t *testing.T, lifetime time.Duration, mintErr func(n int32) error) *jiraProviderCalls {
	t.Helper()
	calls := &jiraProviderCalls{}
	prevMint, prevEnsure, prevUpdate, prevExpiry, prevBaseline := jiraMintFn, ensureJiraProviderLiteralFn, updateJiraProviderFn, setProviderCredentialExpiryFn, jiraBaselineFn
	t.Cleanup(func() {
		jiraMintFn, ensureJiraProviderLiteralFn, updateJiraProviderFn, setProviderCredentialExpiryFn, jiraBaselineFn = prevMint, prevEnsure, prevUpdate, prevExpiry, prevBaseline
	})
	// The sandbox hands out the generation the tests' token files name.
	jiraBaselineFn = func(context.Context, string, string) (string, error) { return ph("v1_JIRA_TOKEN"), nil }
	jiraMintFn = func(_ context.Context, creds jira.Credentials) (jira.AccessToken, error) {
		n := calls.mints.Add(1)
		if creds != testJiraCreds() {
			return jira.AccessToken{}, errors.New("unexpected credentials")
		}
		if mintErr != nil {
			if err := mintErr(n); err != nil {
				return jira.AccessToken{}, err
			}
		}
		return jira.AccessToken{Value: "jira-minted-access-token-" + string(rune('0'+n)), ExpiresAt: time.Now().Add(lifetime).Round(0)}, nil
	}
	ensureJiraProviderLiteralFn = func(_ context.Context, name, providerType string, values map[string]string) error {
		calls.mu.Lock()
		defer calls.mu.Unlock()
		if providerType != jiraProviderType {
			return errors.New("unexpected provider type " + providerType)
		}
		calls.creates = append(calls.creates, values)
		return nil
	}
	updateJiraProviderFn = func(_ context.Context, name string, values map[string]string, expiresAt time.Time) error {
		calls.mu.Lock()
		defer calls.mu.Unlock()
		calls.updates = append(calls.updates, jiraUpdateCall{name: name, values: values, expiresAt: expiresAt})
		return nil
	}
	setProviderCredentialExpiryFn = func(_ context.Context, name, key string, expiresAt time.Time) error {
		calls.mu.Lock()
		defer calls.mu.Unlock()
		calls.expiry = append(calls.expiry, expiresAt)
		return nil
	}
	return calls
}

func TestEnsureJiraProvider_MintsAndCreatesRunScopedProvider(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	calls := stubJiraProvider(t, time.Hour, nil)
	pd := harness.ProviderDef{Name: "jira", Type: jiraProviderType,
		Credentials: map[string]string{jiraCredentialKey: "${FULLSEND_JIRA_TOKEN}"}, Config: map[string]string{"x": "y"}}
	var buf syncBuffer
	h, err := ensureJiraProvider(context.Background(), pd, "fs-tri-0123456789abcdef", testJiraCreds(), ui.New(&buf))
	require.NoError(t, err)

	assert.Equal(t, "jira-0123456789ab", h.name, "run-scoped name")
	assert.Equal(t, "fs-tri-0123456789abcdef", h.sandbox)
	assert.WithinDuration(t, time.Now().Add(time.Hour), h.expiresAt, 5*time.Second)
	assert.False(t, h.sandboxReady(), "no re-seed before the sandbox is up")

	require.Len(t, calls.creates, 1)
	assert.Equal(t, map[string]string{jiraCredentialKey: "jira-minted-access-token-1"}, calls.creates[0], "only the minted token, under JIRA_TOKEN")
	updates := calls.updateList()
	require.Len(t, updates, 1, "the expiry follows in the next call")
	assert.Equal(t, h.name, updates[0].name)
	assert.Equal(t, h.expiresAt, updates[0].expiresAt, "the provider expires with the token")
	for _, v := range append([]map[string]string{calls.creates[0]}, updates[0].values) {
		for _, val := range v {
			assert.NotContains(t, val, testJiraClientSecret, "the client secret never reaches a provider value")
		}
	}

	r := security.NewSecretRedactor()
	assert.NotContains(t, r.Scan("token jira-minted-access-token-1 here").Sanitized, "jira-minted-access-token-1", "the minted token is redacted")
	assert.NotContains(t, r.Scan("secret "+testJiraClientSecret).Sanitized, testJiraClientSecret, "so is the client secret")
	assert.Contains(t, buf.String(), "ignored", "config keys are reported as ignored")
	assert.NotContains(t, buf.String(), "jira-minted-access-token-1")
}

func TestEnsureJiraProvider_ActionsMask(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	stubJiraProvider(t, time.Hour, nil)
	r, w, err := os.Pipe()
	require.NoError(t, err)
	prev := os.Stderr
	os.Stderr = w
	h, ensureErr := ensureJiraProvider(context.Background(), harness.ProviderDef{Name: "jira", Type: jiraProviderType}, "fs-x-0123456789abcdef", testJiraCreds(), ui.New(io.Discard))
	os.Stderr = prev
	require.NoError(t, w.Close())
	out, _ := io.ReadAll(r)
	require.NoError(t, ensureErr)
	assert.True(t, h.maskActions)
	assert.Contains(t, string(out), "::add-mask::jira-minted-access-token-1")
	assert.NotContains(t, string(out), testJiraClientSecret)
}

// TestEnsureJiraProvider_RefusesTokenThatCouldInjectAWorkflowCommand: a
// minted token with a line break would end the ::add-mask:: line, so it
// is refused before anything is printed or stored.
func TestEnsureJiraProvider_RefusesTokenThatCouldInjectAWorkflowCommand(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	for name, value := range map[string]string{
		"embedded newline":          "jira-minted-access-token\nsecond-line",
		"embedded workflow command": "jira-minted-access-token\r\n::set-env name=X::y",
		"embedded space":            "jira-minted access-token",
		"non-ASCII":                 "jira-minted-access-tökenvalue",
	} {
		t.Run(name, func(t *testing.T) {
			calls := stubJiraProvider(t, time.Hour, nil)
			jiraMintFn = func(context.Context, jira.Credentials) (jira.AccessToken, error) {
				return jira.AccessToken{Value: value, ExpiresAt: time.Now().Add(time.Hour)}, nil
			}
			r, w, err := os.Pipe()
			require.NoError(t, err)
			prev := os.Stderr
			os.Stderr = w
			_, ensureErr := ensureJiraProvider(context.Background(), harness.ProviderDef{Name: "jira", Type: jiraProviderType}, "fs-x-0123456789abcdef", testJiraCreds(), ui.New(io.Discard))
			os.Stderr = prev
			require.NoError(t, w.Close())
			out, _ := io.ReadAll(r)
			require.ErrorIs(t, ensureErr, errJiraUnsafeToken)
			assert.NotContains(t, string(out), "::add-mask::")
			assert.NotContains(t, string(out), "::set-env")
			assert.Empty(t, calls.creates)
		})
	}
}

func TestRunJiraRefresh_UnsafeTokenFailsClosed(t *testing.T) {
	shrinkOpenAIRefreshSchedule(t)
	calls := stubJiraProvider(t, time.Hour, nil)
	jiraMintFn = func(context.Context, jira.Credentials) (jira.AccessToken, error) {
		return jira.AccessToken{Value: "jira-minted-access-token\n::stop-commands::x", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	held := time.Now().Add(20 * time.Millisecond).Round(0)
	h := jiraProviderHandle{name: "jira-abc", sandbox: "fs-x", creds: testJiraCreds(), expiresAt: held,
		sandboxUp: &atomic.Bool{}, state: &jiraSeedState{}, unrecorded: &seedGenerations{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runJiraRefresh(context.Background(), h, ui.New(io.Discard))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("refresher did not stop on an unsafe token")
	}
	assert.Empty(t, calls.updateList(), "the token never reached the provider")
	assert.Equal(t, []time.Time{held}, calls.expiryList())
}

// TestEnsureJiraProvider_MaskIsEscaped: the mask goes through
// maskActionsValue, so a % in a printable token is percent-escaped.
func TestEnsureJiraProvider_MaskIsEscaped(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")
	stubJiraProvider(t, time.Hour, nil)
	jiraMintFn = func(context.Context, jira.Credentials) (jira.AccessToken, error) {
		return jira.AccessToken{Value: "jira-minted-access%0Atoken-value", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	r, w, err := os.Pipe()
	require.NoError(t, err)
	prev := os.Stderr
	os.Stderr = w
	_, ensureErr := ensureJiraProvider(context.Background(), harness.ProviderDef{Name: "jira", Type: jiraProviderType}, "fs-x-0123456789abcdef", testJiraCreds(), ui.New(io.Discard))
	os.Stderr = prev
	require.NoError(t, w.Close())
	out, _ := io.ReadAll(r)
	require.NoError(t, ensureErr)
	assert.Contains(t, string(out), "::add-mask::jira-minted-access%250Atoken-value\n")
}

func TestEnsureJiraProvider_MintFailureCreatesNothing(t *testing.T) {
	calls := stubJiraProvider(t, time.Hour, func(int32) error { return errors.New("token endpoint said no") })
	_, err := ensureJiraProvider(context.Background(), harness.ProviderDef{Name: "jira", Type: jiraProviderType}, "fs-x-0123456789abcdef", testJiraCreds(), ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "token endpoint said no")
	assert.Empty(t, calls.creates)
	assert.Empty(t, calls.updateList())
}

func TestEnsureJiraProvider_RejectedCredentialsNamed(t *testing.T) {
	calls := stubJiraProvider(t, time.Hour, func(int32) error { return &jira.OAuthError{Status: 401, Code: "invalid_client"} })
	var buf syncBuffer
	_, err := ensureJiraProvider(context.Background(), harness.ProviderDef{Name: "jira", Type: jiraProviderType}, "fs-x-0123456789abcdef", testJiraCreds(), ui.New(&buf))
	require.Error(t, err)
	assert.True(t, jira.IsPermanentOAuthError(err))
	assert.Contains(t, err.Error(), "token endpoint rejected the client credentials (invalid_client)")
	assert.Contains(t, buf.String(), "rejected the client credentials (invalid_client) for provider jira")
	assert.Equal(t, int32(1), calls.mints.Load())
	assert.Empty(t, calls.creates)

	stubJiraProvider(t, time.Hour, func(int32) error { return &jira.OAuthError{Status: 400} })
	_, err = ensureJiraProvider(context.Background(), harness.ProviderDef{Name: "jira", Type: jiraProviderType}, "fs-x-0123456789abcdef", testJiraCreds(), ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "(status 400)")
}

func TestEnsureJiraProvider_StoreFailureDiscardsProvider(t *testing.T) {
	stubJiraProvider(t, time.Hour, nil)
	updateJiraProviderFn = func(context.Context, string, map[string]string, time.Time) error { return errors.New("update refused") }
	argsLog, _ := fakeOpenshellRecorder(t)
	_, err := ensureJiraProvider(context.Background(), harness.ProviderDef{Name: "jira", Type: jiraProviderType}, "fs-x-0123456789abcdef", testJiraCreds(), ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "update refused")
	assert.Contains(t, readArgLines(t, argsLog), "provider delete jira-0123456789ab", "the half-created provider is removed")
}

func TestRefreshJiraProvider_BeforeSandboxUpOnlyUpdatesProvider(t *testing.T) {
	calls := stubJiraProvider(t, time.Hour, nil)
	prevReseed := reseedJiraFn
	t.Cleanup(func() { reseedJiraFn = prevReseed })
	reseedJiraFn = func(context.Context, string, string, runtime.CredentialSeed, string, *seedGenerations, []string, *ui.Printer) (string, error) {
		t.Fatal("no re-seed before the sandbox is up")
		return "", nil
	}
	h := jiraProviderHandle{name: "jira-abc", sandbox: "fs-x", creds: testJiraCreds(), sandboxUp: &atomic.Bool{}, state: &jiraSeedState{}, unrecorded: &seedGenerations{}}
	exp, err := refreshJiraProvider(context.Background(), h, time.Minute, ui.New(io.Discard))
	require.NoError(t, err)
	updates := calls.updateList()
	require.Len(t, updates, 1)
	assert.Equal(t, map[string]string{jiraCredentialKey: "jira-minted-access-token-1"}, updates[0].values)
	assert.Equal(t, exp, updates[0].expiresAt, "value and expiry in one update")
}

func TestRefreshJiraProvider_ReseedsTokenFileOnceSandboxIsUp(t *testing.T) {
	stubJiraProvider(t, time.Hour, nil)
	prevReseed := reseedJiraFn
	t.Cleanup(func() { reseedJiraFn = prevReseed })
	var gotPrev, gotFile string
	reseedJiraFn = func(_ context.Context, sb, label string, seed runtime.CredentialSeed, previous string, _ *seedGenerations, _ []string, _ *ui.Printer) (string, error) {
		gotPrev, gotFile = previous, seed.File
		assert.Equal(t, "fs-x", sb)
		assert.Equal(t, "JIRA_TOKEN", seed.PlaceholderEnv)
		return ph("v2_JIRA_TOKEN"), nil
	}
	up := &atomic.Bool{}
	up.Store(true)
	h := jiraProviderHandle{name: "jira-abc", sandbox: "fs-x", creds: testJiraCreds(), sandboxUp: up, state: &jiraSeedState{placeholder: ph("v1_JIRA_TOKEN")}, unrecorded: &seedGenerations{}}
	_, err := refreshJiraProvider(context.Background(), h, time.Minute, ui.New(io.Discard))
	require.NoError(t, err)
	assert.Equal(t, ph("v1_JIRA_TOKEN"), gotPrev, "the hand-off replaces the generation the file names")
	assert.Equal(t, sandbox.SandboxJiraTokenFile, gotFile)
	assert.Equal(t, ph("v2_JIRA_TOKEN"), h.state.get(), "and the file now names the new one")
}

// TestRefreshJiraProvider_SeedDuringMintIsReseeded: the sandbox comes up
// and its token file is seeded while a refresh is minting. The refresh
// must notice once it takes the rotation lock and re-seed, so the file
// names the new generation, not the one the seed read.
func TestRefreshJiraProvider_SeedDuringMintIsReseeded(t *testing.T) {
	calls := stubJiraProvider(t, time.Hour, nil)
	minting, release := make(chan struct{}), make(chan struct{})
	mint := jiraMintFn
	jiraMintFn = func(ctx context.Context, creds jira.Credentials) (jira.AccessToken, error) {
		close(minting)
		<-release
		return mint(ctx, creds)
	}
	prevReseed, prevSeed := reseedJiraFn, seedJiraTokenFileFn
	t.Cleanup(func() { reseedJiraFn, seedJiraTokenFileFn = prevReseed, prevSeed })
	seedJiraTokenFileFn = func(_ context.Context, h jiraProviderHandle, _ *ui.Printer) error {
		h.state.set(ph("v1_JIRA_TOKEN"))
		return nil
	}
	var reseedPrev string
	reseedJiraFn = func(_ context.Context, _, _ string, _ runtime.CredentialSeed, previous string, _ *seedGenerations, _ []string, _ *ui.Printer) (string, error) {
		reseedPrev = previous
		return ph("v2_JIRA_TOKEN"), nil
	}
	h := jiraProviderHandle{name: "jira-abc", sandbox: "fs-x", creds: testJiraCreds(), sandboxUp: &atomic.Bool{}, state: &jiraSeedState{}, unrecorded: &seedGenerations{}}

	errc := make(chan error, 1)
	go func() {
		_, err := refreshJiraProvider(context.Background(), h, time.Minute, ui.New(io.Discard))
		errc <- err
	}()
	<-minting
	require.NoError(t, seedJiraAndMarkReady(context.Background(), h, ui.New(io.Discard)))
	assert.True(t, h.sandboxReady())
	close(release)
	require.NoError(t, <-errc)

	require.Len(t, calls.updateList(), 1)
	assert.Equal(t, ph("v1_JIRA_TOKEN"), reseedPrev, "the re-seed replaces what the seed wrote")
	assert.Equal(t, ph("v2_JIRA_TOKEN"), h.state.get(), "the file names the new generation")
}

// TestRefreshJiraProvider_StaleSeedDoesNotCarryForward: the first seed
// read the placeholder from before an update had reached the sandbox, so
// the file names v1 while the sandbox already hands out v2. The next
// refresh waits for a generation newer than v2, retires v1 too, and the
// file lands on the newest generation.
func TestRefreshJiraProvider_StaleSeedDoesNotCarryForward(t *testing.T) {
	calls := stubJiraProvider(t, time.Hour, nil)
	var baselineAt, updatedAt int
	step := 0
	jiraBaselineFn = func(_ context.Context, sb, key string) (string, error) {
		step++
		baselineAt = step
		assert.Equal(t, "fs-x", sb)
		assert.Equal(t, jiraCredentialKey, key)
		return ph("v2_JIRA_TOKEN"), nil
	}
	update := updateJiraProviderFn
	updateJiraProviderFn = func(ctx context.Context, name string, values map[string]string, expiresAt time.Time) error {
		step++
		updatedAt = step
		return update(ctx, name, values, expiresAt)
	}
	prevReseed := reseedJiraFn
	t.Cleanup(func() { reseedJiraFn = prevReseed })
	var gotPrev string
	var gotSuperseded []string
	reseedJiraFn = func(_ context.Context, _, _ string, _ runtime.CredentialSeed, previous string, _ *seedGenerations, superseded []string, _ *ui.Printer) (string, error) {
		gotPrev, gotSuperseded = previous, superseded
		return ph("v3_JIRA_TOKEN"), nil
	}
	up := &atomic.Bool{}
	up.Store(true)
	h := jiraProviderHandle{name: "jira-abc", sandbox: "fs-x", creds: testJiraCreds(), sandboxUp: up, state: &jiraSeedState{placeholder: ph("v1_JIRA_TOKEN")}, unrecorded: &seedGenerations{}}
	_, err := refreshJiraProvider(context.Background(), h, time.Minute, ui.New(io.Discard))
	require.NoError(t, err)
	require.Len(t, calls.updateList(), 1)
	assert.Less(t, baselineAt, updatedAt, "the baseline is read before the update")
	assert.Equal(t, ph("v2_JIRA_TOKEN"), gotPrev, "the wait is for a generation newer than the sandbox's")
	assert.Equal(t, []string{ph("v1_JIRA_TOKEN")}, gotSuperseded, "what the file names is retired too")
	assert.Equal(t, ph("v3_JIRA_TOKEN"), h.state.get(), "the file names the newest generation")
}

func TestRefreshJiraProvider_BaselineFailureIsAnError(t *testing.T) {
	calls := stubJiraProvider(t, time.Hour, nil)
	jiraBaselineFn = func(context.Context, string, string) (string, error) { return "", errors.New("exec failed") }
	up := &atomic.Bool{}
	up.Store(true)
	h := jiraProviderHandle{name: "jira-abc", sandbox: "fs-x", creds: testJiraCreds(), sandboxUp: up, state: &jiraSeedState{placeholder: ph("v1_JIRA_TOKEN")}, unrecorded: &seedGenerations{}}
	_, err := refreshJiraProvider(context.Background(), h, time.Minute, ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "before rotating")
	assert.Empty(t, calls.updateList(), "no rotation without a baseline")
}

func TestRefreshJiraProvider_ReseedOntoHeldGenerationIsAnError(t *testing.T) {
	stubJiraProvider(t, time.Hour, nil)
	jiraBaselineFn = func(context.Context, string, string) (string, error) { return ph("v2_JIRA_TOKEN"), nil }
	prevReseed := reseedJiraFn
	t.Cleanup(func() { reseedJiraFn = prevReseed })
	reseedJiraFn = func(context.Context, string, string, runtime.CredentialSeed, string, *seedGenerations, []string, *ui.Printer) (string, error) {
		return ph("v1_JIRA_TOKEN"), nil
	}
	up := &atomic.Bool{}
	up.Store(true)
	h := jiraProviderHandle{name: "jira-abc", sandbox: "fs-x", creds: testJiraCreds(), sandboxUp: up, state: &jiraSeedState{placeholder: ph("v1_JIRA_TOKEN")}, unrecorded: &seedGenerations{}}
	_, err := refreshJiraProvider(context.Background(), h, time.Minute, ui.New(io.Discard))
	require.Error(t, err)
	assert.Equal(t, ph("v1_JIRA_TOKEN"), h.state.get())
}

// TestSeedJiraAndMarkReady_WaitsForAnUpdateInFlight: a seed that starts
// while a refresh is storing its token waits for the update, so it reads
// the sandbox after the rotation instead of racing it.
func TestSeedJiraAndMarkReady_WaitsForAnUpdateInFlight(t *testing.T) {
	stubJiraProvider(t, time.Hour, nil)
	updating, release := make(chan struct{}), make(chan struct{})
	updateJiraProviderFn = func(context.Context, string, map[string]string, time.Time) error {
		close(updating)
		<-release
		return nil
	}
	prevSeed := seedJiraTokenFileFn
	t.Cleanup(func() { seedJiraTokenFileFn = prevSeed })
	var seeded atomic.Bool
	seedJiraTokenFileFn = func(_ context.Context, h jiraProviderHandle, _ *ui.Printer) error {
		seeded.Store(true)
		h.state.set(ph("v2_JIRA_TOKEN"))
		return nil
	}
	h := jiraProviderHandle{name: "jira-abc", sandbox: "fs-x", creds: testJiraCreds(), sandboxUp: &atomic.Bool{}, state: &jiraSeedState{}, unrecorded: &seedGenerations{}}

	refreshErr := make(chan error, 1)
	go func() {
		_, err := refreshJiraProvider(context.Background(), h, time.Minute, ui.New(io.Discard))
		refreshErr <- err
	}()
	<-updating
	seedErr := make(chan error, 1)
	go func() { seedErr <- seedJiraAndMarkReady(context.Background(), h, ui.New(io.Discard)) }()
	time.Sleep(50 * time.Millisecond)
	assert.False(t, seeded.Load(), "the seed waits for the update in flight")
	close(release)
	require.NoError(t, <-refreshErr)
	require.NoError(t, <-seedErr)
	assert.True(t, seeded.Load())
	assert.True(t, h.sandboxReady())
}

func TestSeedJiraAndMarkReady_FailureLeavesSandboxDown(t *testing.T) {
	prevSeed := seedJiraTokenFileFn
	t.Cleanup(func() { seedJiraTokenFileFn = prevSeed })
	seedJiraTokenFileFn = func(context.Context, jiraProviderHandle, *ui.Printer) error { return errors.New("seed failed") }
	h := jiraProviderHandle{name: "jira-abc", sandbox: "fs-x", sandboxUp: &atomic.Bool{}, state: &jiraSeedState{}}
	require.Error(t, seedJiraAndMarkReady(context.Background(), h, ui.New(io.Discard)))
	assert.False(t, h.sandboxUp.Load())
}

// TestRunJiraRefresh_RefreshesBeforeExpiry runs the refresher on a token
// that expires almost at once: it mints again, stores the new token with
// its own expiry, and keeps going until cancelled.
func TestRunJiraRefresh_RefreshesBeforeExpiry(t *testing.T) {
	shrinkOpenAIRefreshSchedule(t)
	calls := stubJiraProvider(t, 50*time.Millisecond, nil)
	var buf syncBuffer
	h := jiraProviderHandle{name: "jira-abc", sandbox: "fs-x", creds: testJiraCreds(), expiresAt: time.Now().Add(50 * time.Millisecond),
		sandboxUp: &atomic.Bool{}, state: &jiraSeedState{}, unrecorded: &seedGenerations{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runJiraRefresh(ctx, h, ui.New(&buf))
	}()
	require.Eventually(t, func() bool { return len(calls.updateList()) >= 2 }, 5*time.Second, 10*time.Millisecond, "two refreshes")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("refresher did not stop on cancel")
	}
	updates := calls.updateList()
	assert.NotEqual(t, updates[0].values[jiraCredentialKey], updates[1].values[jiraCredentialKey], "each refresh mints a new token")
	assert.True(t, updates[1].expiresAt.After(updates[0].expiresAt), "and records its later expiry")
	assert.Contains(t, buf.String(), "Jira access token refreshed for jira-abc")
	assert.Empty(t, calls.expiryList(), "a healthy refresher never moves the expiry back")
}

// TestRunJiraRefresh_MintFailureKeepsRetrying: a token endpoint outage
// (a 5xx) is retried in rounds, never given up on, and the provider keeps
// the held token's expiry, so Jira access ends then if the outage lasts.
func TestRunJiraRefresh_MintFailureKeepsRetrying(t *testing.T) {
	shrinkOpenAIRefreshSchedule(t)
	calls := stubJiraProvider(t, time.Hour, func(int32) error { return &jira.OAuthError{Status: 503} })
	var buf syncBuffer
	h := jiraProviderHandle{name: "jira-abc", sandbox: "fs-x", creds: testJiraCreds(), expiresAt: time.Now(),
		sandboxUp: &atomic.Bool{}, state: &jiraSeedState{}, unrecorded: &seedGenerations{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runJiraRefresh(ctx, h, ui.New(&buf))
	}()
	require.Eventually(t, func() bool { return calls.mints.Load() >= int32(2*openAIRefreshRetries) }, 10*time.Second, 10*time.Millisecond, "a second round follows the first")
	select {
	case <-done:
		t.Fatal("refresher returned after one failed round")
	default:
	}
	cancel()
	<-done
	assert.Contains(t, buf.String(), "retrying in")
	assert.Empty(t, calls.updateList(), "nothing was stored")
	assert.NotContains(t, buf.String(), testJiraClientSecret)
}

// TestRunJiraRefresh_PermanentMintErrorFailsClosed: a token endpoint that
// rejects the client credentials stops the refresher on the first answer,
// with no retry, and moves the expiry back to the held token's.
func TestRunJiraRefresh_PermanentMintErrorFailsClosed(t *testing.T) {
	shrinkOpenAIRefreshSchedule(t)
	calls := stubJiraProvider(t, time.Hour, func(int32) error {
		return &jira.OAuthError{Status: 401, Code: "invalid_client"}
	})
	held := time.Now().Add(20 * time.Millisecond).Round(0)
	h := jiraProviderHandle{name: "jira-abc", sandbox: "fs-x", creds: testJiraCreds(), expiresAt: held,
		sandboxUp: &atomic.Bool{}, state: &jiraSeedState{}, unrecorded: &seedGenerations{}}
	var buf syncBuffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		runJiraRefresh(context.Background(), h, ui.New(&buf))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("refresher kept going after the client credentials were rejected")
	}
	assert.Equal(t, int32(1), calls.mints.Load(), "exactly one mint")
	assert.NotContains(t, buf.String(), "retrying in")
	assert.Contains(t, buf.String(), "token endpoint rejected the client credentials (invalid_client)")
	assert.Contains(t, buf.String(), "Jira access token refresh for jira-abc stopped")
	assert.Equal(t, []time.Time{held}, calls.expiryList())
}

// TestRunJiraRefresh_PermanentVsTransientMintErrors: only answers no retry
// can fix stop the refresher; the rest keep it retrying.
func TestRunJiraRefresh_PermanentVsTransientMintErrors(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		permanent bool
	}{
		{"invalid_client", &jira.OAuthError{Status: 401, Code: "invalid_client"}, true},
		{"access_denied", &jira.OAuthError{Status: 403, Code: "access_denied"}, true},
		{"unauthorized_client", &jira.OAuthError{Status: 400, Code: "unauthorized_client"}, true},
		{"invalid_scope", &jira.OAuthError{Status: 400, Code: "invalid_scope"}, true},
		{"invalid_grant", &jira.OAuthError{Status: 400, Code: "invalid_grant"}, true},
		{"401 with no code", &jira.OAuthError{Status: 401}, true},
		{"429", &jira.OAuthError{Status: 429}, false},
		{"500", &jira.OAuthError{Status: 500, Code: "server_error"}, false},
		{"transport error", errors.New("dial tcp: connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shrinkOpenAIRefreshSchedule(t)
			calls := stubJiraProvider(t, time.Hour, func(int32) error { return tc.err })
			h := jiraProviderHandle{name: "jira-abc", sandbox: "fs-x", creds: testJiraCreds(), expiresAt: time.Now(),
				sandboxUp: &atomic.Bool{}, state: &jiraSeedState{}, unrecorded: &seedGenerations{}}
			var buf syncBuffer
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				runJiraRefresh(ctx, h, ui.New(&buf))
			}()
			if tc.permanent {
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("refresher did not stop")
				}
				assert.Equal(t, int32(1), calls.mints.Load())
				assert.Len(t, calls.expiryList(), 1, "fails closed")
				return
			}
			require.Eventually(t, func() bool { return calls.mints.Load() > int32(openAIRefreshRetries) }, 10*time.Second, 10*time.Millisecond, "a transient error is retried past one round")
			cancel()
			<-done
			assert.Empty(t, calls.expiryList(), "a transient error never fails closed")
		})
	}
}

// TestRunJiraRefresh_UnobservedGenerationFailsClosed: when the sandbox
// never hands out the new generation, the refresher stops and moves the
// provider's expiry back to the token the agent holds.
func TestRunJiraRefresh_UnobservedGenerationFailsClosed(t *testing.T) {
	shrinkOpenAIRefreshSchedule(t)
	calls := stubJiraProvider(t, time.Hour, nil)
	prevReseed := reseedJiraFn
	t.Cleanup(func() { reseedJiraFn = prevReseed })
	reseedJiraFn = func(context.Context, string, string, runtime.CredentialSeed, string, *seedGenerations, []string, *ui.Printer) (string, error) {
		return "", &generationNotObservedError{label: "Jira", settle: time.Second}
	}
	held := time.Now().Add(30 * time.Millisecond).Round(0)
	up := &atomic.Bool{}
	up.Store(true)
	h := jiraProviderHandle{name: "jira-abc", sandbox: "fs-x", creds: testJiraCreds(), expiresAt: held,
		sandboxUp: up, state: &jiraSeedState{placeholder: ph("v1_JIRA_TOKEN")}, unrecorded: &seedGenerations{}}
	var buf syncBuffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		runJiraRefresh(context.Background(), h, ui.New(&buf))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("refresher did not stop after an unobserved generation")
	}
	assert.Equal(t, []time.Time{held}, calls.expiryList(), "the expiry moves back to the held token's")
	assert.Equal(t, ph("v1_JIRA_TOKEN"), h.state.get(), "the file still names the held generation")
	assert.Contains(t, buf.String(), "Jira access token refresh for jira-abc stopped")
}

func TestRunJiraRefresh_UnredactableTokenFailsClosed(t *testing.T) {
	shrinkOpenAIRefreshSchedule(t)
	calls := stubJiraProvider(t, time.Hour, nil)
	jiraMintFn = func(context.Context, jira.Credentials) (jira.AccessToken, error) {
		return jira.AccessToken{Value: "ab", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	held := time.Now().Add(20 * time.Millisecond).Round(0)
	h := jiraProviderHandle{name: "jira-abc", sandbox: "fs-x", creds: testJiraCreds(), expiresAt: held,
		sandboxUp: &atomic.Bool{}, state: &jiraSeedState{}, unrecorded: &seedGenerations{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runJiraRefresh(context.Background(), h, ui.New(io.Discard))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("refresher did not stop on an unredactable token")
	}
	assert.Empty(t, calls.updateList(), "the token never reached the provider")
	assert.Equal(t, []time.Time{held}, calls.expiryList())
}

func TestSeedJiraTokenFile_RecordsWhatTheFileHolds(t *testing.T) {
	binDir := t.TempDir()
	log := filepath.Join(binDir, "log")
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		// The seed takes the flock; the read-back is a plain cat.
		"  *flock*) echo seeded >> " + shellQuoteForTest(log) + "; exit 0 ;;\n" +
		"  *'command -p cat'*) printf '" + ph("v9_JIRA_TOKEN") + "\\n'; exit 0 ;;\n" +
		"esac\necho \"unhandled: $*\" >&2; exit 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	h := jiraProviderHandle{name: "jira-abc", sandbox: "fs-x", sandboxUp: &atomic.Bool{}, state: &jiraSeedState{}}
	require.NoError(t, seedJiraTokenFile(context.Background(), h, ui.New(io.Discard)))
	assert.Equal(t, ph("v9_JIRA_TOKEN"), h.state.get())
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	assert.Equal(t, "seeded\n", string(data))
}

func TestSeedJiraTokenFile_SeedFailureIsAnError(t *testing.T) {
	stubOpenshell(t, "echo 'fullsend: JIRA_TOKEN in the sandbox is not a gateway placeholder' >&2; exit 1")
	h := jiraProviderHandle{name: "jira-abc", sandbox: "fs-x", sandboxUp: &atomic.Bool{}, state: &jiraSeedState{}}
	err := seedJiraTokenFile(context.Background(), h, ui.New(io.Discard))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a gateway placeholder")
	assert.Empty(t, h.state.get())
}

func TestJiraIsBuiltinAndReserved(t *testing.T) {
	assert.True(t, isBuiltinProviderName("jira"))
	assert.True(t, isReservedProfileID(jiraProviderType))
	data, err := scaffold.FullsendRepoFile("providers/jira.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(data), "\nname: jira\n")
	assert.Contains(t, string(data), "\ntype: fullsend-jira\n")
	assert.Contains(t, string(data), `JIRA_TOKEN: "${FULLSEND_JIRA_TOKEN}"`)
	profile, err := scaffold.FullsendRepoFile("profiles/fullsend-jira.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(profile), "id: fullsend-jira\n")
	assert.NotContains(t, string(profile), "read-write", "the sandbox's Jira access is read-only")
}

// writeJiraFullsendDir lays out a fullsend dir whose code harness declares
// providerEntry (a bare builtin name, or a workspace provider file name).
func writeJiraFullsendDir(t *testing.T, providerEntry string, extraFiles map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"harness", "agents", "providers", "profiles"} {
		require.NoError(t, os.MkdirAll(filepath.Join(dir, d), 0o755))
	}
	writePolicyFixture(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agents", "code.md"), []byte("You are a coding agent."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\nrole: test\npolicy: policies/base.yaml\nproviders:\n  - "+providerEntry+"\n"), 0o644))
	for p, c := range extraFiles {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("version: \"1\"\nagents:\n  - harness/code.yaml\n"), 0o644))
	return dir
}

func runJiraAgent(t *testing.T, dir string) (error, []string) {
	t.Helper()
	return runJiraAgentPrinting(t, dir, ui.New(io.Discard))
}

func runJiraAgentPrinting(t *testing.T, dir string, printer *ui.Printer) (error, []string) {
	t.Helper()
	logPath := recordingProvidersStub(t)
	rFlags := resolveFlags{maxDepth: 10, maxResources: 50}
	err := runAgent(context.Background(), "code", dir, "", t.TempDir(), fakeSandboxBinary(t), nil, false, "", "", "", rFlags, statusOpts{}, printer, false, runOverrideFlags{})
	data, readErr := os.ReadFile(logPath)
	require.NoError(t, readErr)
	return err, strings.Split(strings.TrimSpace(string(data)), "\n")
}

func linesWithPrefix(lines []string, prefix string) []string {
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

// TestRunAgent_JiraProviderMintsRunScopedToken: with only client
// credentials the run mints the token, attaches a run-scoped instance
// instead of the bare name, tries to seed the token file once the sandbox
// is up (the stub's sandbox exec fails, which fails the run), and deletes
// the instance at the end. The client secret is on no command line.
func TestRunAgent_JiraProviderMintsRunScopedToken(t *testing.T) {
	unsetJiraToken(t)
	t.Setenv(jira.ClientIDEnvVar, testJiraClientID)
	t.Setenv(jira.ClientSecretEnvVar, testJiraClientSecret)
	calls := stubJiraProvider(t, time.Hour, nil)
	dir := writeJiraFullsendDir(t, "jira", nil)

	err, lines := runJiraAgent(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Jira token file", "the run stops at the token file seed")
	assert.Equal(t, int32(1), calls.mints.Load())
	require.Len(t, calls.creates, 1)

	all := strings.Join(lines, "\n")
	assert.NotContains(t, all, testJiraClientSecret)
	assert.NotContains(t, all, "jira-minted-access-token")
	assert.Contains(t, all, "fullsend-jira-", "the embedded fullsend-jira profile is imported")
	assert.Empty(t, linesWithPrefix(lines, "provider create --name jira "), "no shared jira provider in mint mode")

	sandboxLines := linesWithPrefix(lines, "sandbox create ")
	require.Len(t, sandboxLines, 1)
	assert.Regexp(t, `--provider jira-[0-9a-f]{12}( |$)`, sandboxLines[0], "the run-scoped instance is attached")
	assert.NotRegexp(t, `--provider jira( |$)`, sandboxLines[0])
	scoped := regexpFind(t, `jira-[0-9a-f]{12}`, sandboxLines[0])
	assert.Contains(t, lines, "provider delete "+scoped, "and deleted at run end")
}

func regexpFind(t *testing.T, pattern, s string) string {
	t.Helper()
	m := regexp.MustCompile(pattern).FindString(s)
	require.NotEmpty(t, m, "no %s in %q", pattern, s)
	return m
}

// TestRunAgent_JiraProviderStaticTokenUnchanged: with a static
// FULLSEND_JIRA_TOKEN the builtin provider is created like any shared
// provider, by bare key, and nothing is minted.
func TestRunAgent_JiraProviderStaticTokenUnchanged(t *testing.T) {
	t.Setenv(jira.TokenEnvVar, "static-jira-token-for-test")
	calls := stubJiraProvider(t, time.Hour, nil)
	dir := writeJiraFullsendDir(t, "jira", nil)

	err, lines := runJiraAgent(t, dir)
	require.Error(t, err, "the stub cannot bootstrap an agent")
	assert.NotContains(t, err.Error(), "Jira token file", "no token file in static mode")
	assert.Equal(t, int32(0), calls.mints.Load())
	assert.Empty(t, calls.creates)
	assert.Contains(t, lines, "provider create --name jira --type fullsend-jira --credential JIRA_TOKEN", "shared provider, value by bare key")
	sandboxLines := linesWithPrefix(lines, "sandbox create ")
	require.Len(t, sandboxLines, 1)
	assert.Regexp(t, `--provider jira( |$)`, sandboxLines[0])
	assert.NotContains(t, strings.Join(lines, "\n"), "static-jira-token-for-test")
}

// TestRunAgent_JiraRoProviderUnchanged: the agents repo's own jira-ro
// provider (profile fullsend-jira-ro) is not the builtin one; it is
// created exactly as before, with no minting, even on a deployment that
// has only client credentials.
func TestRunAgent_JiraRoProviderUnchanged(t *testing.T) {
	unsetJiraToken(t)
	t.Setenv(jira.ClientIDEnvVar, testJiraClientID)
	t.Setenv(jira.ClientSecretEnvVar, testJiraClientSecret)
	calls := stubJiraProvider(t, time.Hour, nil)
	dir := writeJiraFullsendDir(t, "jira-ro", map[string]string{
		"providers/jira-ro.yaml": "name: jira-ro\ntype: fullsend-jira-ro\ncredentials:\n  JIRA_TOKEN: \"${FULLSEND_JIRA_TOKEN}\"\n",
	})

	_, lines := runJiraAgent(t, dir)
	assert.Equal(t, int32(0), calls.mints.Load())
	assert.Contains(t, lines, "provider create --name jira-ro --type fullsend-jira-ro --credential JIRA_TOKEN=")
}

// TestRunAgent_JiraProviderStaticTokenKeepsRepositoryProfile: with a
// static token nothing is minted, so a harness-listed fullsend-jira copy
// (as earlier vendored installs carry) is warned about and kept, like any
// other reserved profile copy, instead of failing the run.
func TestRunAgent_JiraProviderStaticTokenKeepsRepositoryProfile(t *testing.T) {
	t.Setenv(jira.TokenEnvVar, "static-jira-token-for-test")
	calls := stubJiraProvider(t, time.Hour, nil)
	dir := writeJiraFullsendDir(t, "jira", map[string]string{
		"profiles/fullsend-jira.yaml": "id: fullsend-jira\ndisplay_name: vendored copy\ncategory: data\nendpoints:\n  - host: \"api.atlassian.com\"\n    port: 443\n    protocol: rest\n    access: read-only\n    enforcement: enforce\n",
	})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\nrole: test\npolicy: policies/base.yaml\nopenshell:\n  profiles:\n    - profiles/fullsend-jira.yaml\nproviders:\n  - jira\n"), 0o644))

	var buf syncBuffer
	err, lines := runJiraAgentPrinting(t, dir, ui.New(&buf))
	require.Error(t, err, "the stub cannot bootstrap an agent")
	assert.NotContains(t, err.Error(), "is reserved")
	assert.Contains(t, buf.String(), `provider profile "fullsend-jira" will be rejected in a future release`)
	assert.Equal(t, int32(0), calls.mints.Load())
	assert.Contains(t, lines, "provider create --name jira --type fullsend-jira --credential JIRA_TOKEN", "the shared provider is created")
	imports := linesWithPrefix(lines, "provider profile import ")
	require.Len(t, imports, 1, "the embedded profile is not imported over the copy")
	assert.Contains(t, imports[0], filepath.Join(dir, "profiles", "fullsend-jira.yaml"))
}

// TestRunAgent_JiraProviderRefusesRepositoryProfile: a harness-listed
// profile with the reserved fullsend-jira id is refused before anything
// is minted when the run mints.
func TestRunAgent_JiraProviderRefusesRepositoryProfile(t *testing.T) {
	unsetJiraToken(t)
	t.Setenv(jira.ClientIDEnvVar, testJiraClientID)
	t.Setenv(jira.ClientSecretEnvVar, testJiraClientSecret)
	calls := stubJiraProvider(t, time.Hour, nil)
	dir := writeJiraFullsendDir(t, "jira", map[string]string{
		"profiles/fullsend-jira.yaml": "id: fullsend-jira\ndisplay_name: loose\ncategory: data\nendpoints:\n  - host: \"*\"\n    port: 443\n    access: read-write\n",
	})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\nrole: test\npolicy: policies/base.yaml\nopenshell:\n  profiles:\n    - profiles/fullsend-jira.yaml\nproviders:\n  - jira\n"), 0o644))

	err, _ := runJiraAgent(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"fullsend-jira" is reserved`)
	assert.Equal(t, int32(0), calls.mints.Load())
}

// TestRunAgent_TwoMintedJiraProvidersRejected: two differently named
// fullsend-jira definitions in one run would share the one token file, so
// the run stops before it mints or creates anything.
func TestRunAgent_TwoMintedJiraProvidersRejected(t *testing.T) {
	unsetJiraToken(t)
	t.Setenv(jira.ClientIDEnvVar, testJiraClientID)
	t.Setenv(jira.ClientSecretEnvVar, testJiraClientSecret)
	calls := stubJiraProvider(t, time.Hour, nil)
	dir := writeJiraFullsendDir(t, "jira", map[string]string{
		"providers/jira-second.yaml": "name: jira-second\ntype: fullsend-jira\ncredentials:\n  JIRA_TOKEN: \"${FULLSEND_JIRA_TOKEN}\"\n",
	})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\nrole: test\npolicy: policies/base.yaml\nproviders:\n  - jira\n  - jira-second\n"), 0o644))

	err, lines := runJiraAgent(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "one Jira token file")
	assert.Contains(t, err.Error(), `"jira"`)
	assert.Contains(t, err.Error(), `"jira-second"`)
	assert.Equal(t, int32(0), calls.mints.Load(), "nothing is minted")
	assert.Empty(t, calls.creates)
	assert.Empty(t, linesWithPrefix(lines, "provider create "), "no provider is created")
}

// TestRunAgent_StaticJiraProviderNextToMintedRejected: a static
// fullsend-jira definition next to the minted builtin would share
// JIRA_TOKEN, so the run stops before it mints or creates anything.
func TestRunAgent_StaticJiraProviderNextToMintedRejected(t *testing.T) {
	unsetJiraToken(t)
	t.Setenv("JIRA_STATIC_FOR_TEST", "static-jira-token-value")
	t.Setenv(jira.ClientIDEnvVar, testJiraClientID)
	t.Setenv(jira.ClientSecretEnvVar, testJiraClientSecret)
	calls := stubJiraProvider(t, time.Hour, nil)
	dir := writeJiraFullsendDir(t, "jira", map[string]string{
		"providers/jira-static.yaml": "name: jira-static\ntype: fullsend-jira\ncredentials:\n  JIRA_TOKEN: \"${JIRA_STATIC_FOR_TEST}\"\n",
	})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\nrole: test\npolicy: policies/base.yaml\nproviders:\n  - jira\n  - jira-static\n"), 0o644))

	err, lines := runJiraAgent(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "share JIRA_TOKEN")
	assert.Equal(t, int32(0), calls.mints.Load())
	assert.Empty(t, linesWithPrefix(lines, "provider create "))
}

// TestRunAgent_JiraRoNextToMintedJiraRejected: a harness migrating from
// the agents repo's jira-ro that lists both providers would attach two
// providers exporting JIRA_TOKEN, so the run stops before it mints.
func TestRunAgent_JiraRoNextToMintedJiraRejected(t *testing.T) {
	unsetJiraToken(t)
	t.Setenv(jira.ClientIDEnvVar, testJiraClientID)
	t.Setenv(jira.ClientSecretEnvVar, testJiraClientSecret)
	calls := stubJiraProvider(t, time.Hour, nil)
	dir := writeJiraFullsendDir(t, "jira", map[string]string{
		"providers/jira-ro.yaml": "name: jira-ro\ntype: fullsend-jira-ro\ncredentials:\n  JIRA_TOKEN: \"${FULLSEND_JIRA_TOKEN}\"\n",
	})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "harness", "code.yaml"),
		[]byte("agent: agents/code.md\nrole: test\npolicy: policies/base.yaml\nproviders:\n  - jira\n  - jira-ro\n"), 0o644))

	err, lines := runJiraAgent(t, dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"jira-ro"`)
	assert.Contains(t, err.Error(), "share JIRA_TOKEN")
	assert.Equal(t, int32(0), calls.mints.Load())
	assert.Empty(t, linesWithPrefix(lines, "provider create "))
}

func TestRejectMultipleMintedJiraProviders(t *testing.T) {
	t.Setenv("JIRA_STATIC_FOR_TEST", "static-jira-token-value")
	both := jiraEnv(map[string]string{jira.ClientIDEnvVar: testJiraClientID, jira.ClientSecretEnvVar: testJiraClientSecret})
	minted := func(name string) harness.ProviderDef {
		return harness.ProviderDef{Name: name, Type: jiraProviderType, Credentials: map[string]string{jiraCredentialKey: ""}}
	}
	static := harness.ProviderDef{Name: "jira-static", Type: "Fullsend-Jira", Credentials: map[string]string{jiraCredentialKey: "${JIRA_STATIC_FOR_TEST}"}}
	other := harness.ProviderDef{Name: "github", Type: "fullsend-github", Credentials: map[string]string{"GH_TOKEN": ""}}

	require.NoError(t, rejectMultipleMintedJiraProviders([]harness.ProviderDef{minted("jira"), other}, both), "one minted provider")
	require.NoError(t, rejectMultipleMintedJiraProviders([]harness.ProviderDef{static, static, other}, both), "nothing mints")
	require.NoError(t, rejectMultipleMintedJiraProviders([]harness.ProviderDef{minted("jira"), minted("jira-b")}, jiraEnv(nil)), "nothing mints without client credentials")
	err := rejectMultipleMintedJiraProviders([]harness.ProviderDef{minted("jira"), other, minted("jira-b")}, both)
	require.Error(t, err)
	assert.Contains(t, err.Error(), sandbox.SandboxJiraTokenFile)

	// Any type declaring JIRA_TOKEN counts; one declaring other keys does not.
	jiraRo := harness.ProviderDef{Name: "jira-ro", Type: "fullsend-jira-ro", Credentials: map[string]string{jiraCredentialKey: "${FULLSEND_JIRA_TOKEN}"}}
	err = rejectMultipleMintedJiraProviders([]harness.ProviderDef{minted("jira"), jiraRo}, both)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"jira-ro"`)
	require.NoError(t, rejectMultipleMintedJiraProviders([]harness.ProviderDef{jiraRo, static}, both), "nothing mints")

	// A static fullsend-jira next to a minted one shares JIRA_TOKEN too.
	err = rejectMultipleMintedJiraProviders([]harness.ProviderDef{static, other, minted("jira")}, both)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"jira-static"`)
	assert.Contains(t, err.Error(), `"jira" mints`)
}

func TestEnsureJiraProvider_StoreAndDeleteFailureExpiresCredential(t *testing.T) {
	calls := stubJiraProvider(t, time.Hour, nil)
	updateJiraProviderFn = func(context.Context, string, map[string]string, time.Time) error { return errors.New("update refused") }
	fakeOpenshellRecorder(t, "provider delete")
	var buf syncBuffer
	_, err := ensureJiraProvider(context.Background(), harness.ProviderDef{Name: "jira", Type: jiraProviderType}, "fs-x-0123456789abcdef", testJiraCreds(), ui.New(&buf))
	require.Error(t, err)
	require.Len(t, calls.expiryList(), 1, "the credential is expired when the delete fails")
	assert.WithinDuration(t, time.Now(), calls.expiryList()[0], 5*time.Second)
	assert.Contains(t, buf.String(), "its credential was expired")
}
