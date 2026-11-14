package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/security"
)

const testTirithPolicy = "approval_rules:\n  - rule_ids: [package_lookup_incomplete]\n"

// gitT runs git in dir with a fixed identity and returns trimmed stdout.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir())
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// newPolicyRepo creates a git repo whose first commit holds files.
func newPolicyRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	gitT(t, dir, "init", "-q")
	for name, content := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func TestTrustedTirithRepoPolicy_FilePresent(t *testing.T) {
	dir := newPolicyRepo(t, map[string]string{tirithRepoPolicyPath: testTirithPolicy})
	policy, note, err := trustedTirithRepoPolicy(context.Background(), dir, tirithPolicySource{})
	require.NoError(t, err)
	assert.Equal(t, testTirithPolicy, string(policy))
	assert.Contains(t, note, "Staging repository tirith policy")
}

func TestTrustedTirithRepoPolicy_FileAbsent(t *testing.T) {
	dir := newPolicyRepo(t, map[string]string{"README.md": "hi\n"})
	policy, note, err := trustedTirithRepoPolicy(context.Background(), dir, tirithPolicySource{})
	require.NoError(t, err)
	assert.Nil(t, policy)
	assert.Empty(t, note)
}

func TestTrustedTirithRepoPolicy_EmptyFileStagesNothing(t *testing.T) {
	dir := newPolicyRepo(t, map[string]string{tirithRepoPolicyPath: ""})
	policy, _, err := trustedTirithRepoPolicy(context.Background(), dir, tirithPolicySource{})
	require.NoError(t, err)
	assert.Nil(t, policy)
}

func TestTrustedTirithRepoPolicy_ReadsCommittedCopyNotWorkingTree(t *testing.T) {
	dir := newPolicyRepo(t, map[string]string{tirithRepoPolicyPath: testTirithPolicy})
	require.NoError(t, os.WriteFile(filepath.Join(dir, tirithRepoPolicyPath), []byte("allowlist: [\"*\"]\n"), 0o644))
	policy, _, err := trustedTirithRepoPolicy(context.Background(), dir, tirithPolicySource{})
	require.NoError(t, err)
	assert.Equal(t, testTirithPolicy, string(policy), "an uncommitted edit must not be staged")
}

func TestTrustedTirithRepoPolicy_NotAGitCheckout(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".tirith"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, tirithRepoPolicyPath), []byte(testTirithPolicy), 0o644))
	policy, note, err := trustedTirithRepoPolicy(context.Background(), dir, tirithPolicySource{})
	require.NoError(t, err)
	assert.Nil(t, policy, "without a commit there is no trusted ref")
	assert.Empty(t, note)
}

func TestTrustedTirithRepoPolicy_SymlinkIsRefused(t *testing.T) {
	dir := newPolicyRepo(t, map[string]string{"real.yaml": testTirithPolicy})
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".tirith"), 0o755))
	require.NoError(t, os.Symlink("../real.yaml", filepath.Join(dir, tirithRepoPolicyPath)))
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-q", "-m", "link")
	_, _, err := trustedTirithRepoPolicy(context.Background(), dir, tirithPolicySource{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a regular file")
}

func TestTrustedTirithRepoPolicy_OversizedIsRefused(t *testing.T) {
	big := strings.Repeat("#", tirithRepoPolicyMaxBytes+1)
	dir := newPolicyRepo(t, map[string]string{tirithRepoPolicyPath: big})
	_, _, err := trustedTirithRepoPolicy(context.Background(), dir, tirithPolicySource{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "limit")
}

func TestTrustedTirithRepoPolicy_RequestRunUsesBaseCommit(t *testing.T) {
	dir := newPolicyRepo(t, map[string]string{tirithRepoPolicyPath: testTirithPolicy})
	base := gitT(t, dir, "rev-parse", "HEAD")
	// The request's head changes the policy; only the base copy may be used.
	require.NoError(t, os.WriteFile(filepath.Join(dir, tirithRepoPolicyPath), []byte("# head copy\n"), 0o644))
	gitT(t, dir, "commit", "-q", "-am", "head")

	policy, note, err := trustedTirithRepoPolicy(context.Background(), dir, tirithPolicySource{requestRun: true, baseSHA: strings.ToUpper(base)})
	require.NoError(t, err)
	assert.Equal(t, testTirithPolicy, string(policy))
	assert.Contains(t, note, base[:12])
}

func TestTrustedTirithRepoPolicy_RequestRunWithoutBaseStagesNothing(t *testing.T) {
	dir := newPolicyRepo(t, map[string]string{tirithRepoPolicyPath: testTirithPolicy})
	for name, sha := range map[string]string{
		"no base":        "",
		"not in repo":    strings.Repeat("a", 40),
		"not an oid":     "HEAD",
		"option-shaped":  "--output=/tmp/x",
		"abbreviated":    gitT(t, dir, "rev-parse", "--short", "HEAD"),
		"sha256 missing": strings.Repeat("b", 64),
	} {
		t.Run(name, func(t *testing.T) {
			policy, note, err := trustedTirithRepoPolicy(context.Background(), dir, tirithPolicySource{requestRun: true, baseSHA: sha})
			require.NoError(t, err)
			assert.Nil(t, policy, "the head copy must never be staged")
			assert.Contains(t, note, "not staged")
		})
	}
}

func TestTirithPolicySourceFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	assert.Equal(t, tirithPolicySource{}, tirithPolicySourceFromEnv(env(nil)))
	for _, name := range []string{"PR_NUMBER", "GITHUB_PR_URL", "MR_IID", "CI_MERGE_REQUEST_IID"} {
		assert.True(t, tirithPolicySourceFromEnv(env(map[string]string{name: "7"})).requestRun, name)
	}
	assert.True(t, tirithPolicySourceFromEnv(env(map[string]string{"FULLSEND_NOTE_TARGET": "merge_requests"})).requestRun)
	assert.False(t, tirithPolicySourceFromEnv(env(map[string]string{"PR_NUMBER": "  "})).requestRun)

	base := strings.Repeat("c", 40)
	dir := t.TempDir()
	direct := filepath.Join(dir, "direct.json")
	require.NoError(t, os.WriteFile(direct, []byte(`{"pull_request":{"base":{"sha":"`+base+`"}}}`), 0o644))
	src := tirithPolicySourceFromEnv(env(map[string]string{"GITHUB_EVENT_PATH": direct}))
	assert.Equal(t, tirithPolicySource{requestRun: true, baseSHA: base}, src)

	dispatch := filepath.Join(dir, "dispatch.json")
	require.NoError(t, os.WriteFile(dispatch, []byte(`{"inputs":{"event_payload":"{\"pull_request\":{\"base\":{\"sha\":\"`+base+`\"}}}"}}`), 0o644))
	src = tirithPolicySourceFromEnv(env(map[string]string{"GITHUB_EVENT_PATH": dispatch}))
	assert.Equal(t, tirithPolicySource{requestRun: true, baseSHA: base}, src)

	issue := filepath.Join(dir, "issue.json")
	require.NoError(t, os.WriteFile(issue, []byte(`{"issue":{"number":1}}`), 0o644))
	assert.Equal(t, tirithPolicySource{}, tirithPolicySourceFromEnv(env(map[string]string{"GITHUB_EVENT_PATH": issue})))
}

func TestPRBaseSHAFromEventPath_Unreadable(t *testing.T) {
	assert.Empty(t, prBaseSHAFromEventPath(""))
	assert.Empty(t, prBaseSHAFromEventPath(filepath.Join(t.TempDir(), "missing.json")))
	bad := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte("{"), 0o644))
	assert.Empty(t, prBaseSHAFromEventPath(bad))
	badPayload := filepath.Join(t.TempDir(), "payload.json")
	require.NoError(t, os.WriteFile(badPayload, []byte(`{"inputs":{"event_payload":"{"}}`), 0o644))
	assert.Empty(t, prBaseSHAFromEventPath(badPayload))
}

func TestAttachTirithRepoPolicy(t *testing.T) {
	dir := newPolicyRepo(t, map[string]string{tirithRepoPolicyPath: testTirithPolicy})
	noEnv := func(string) string { return "" }
	h := &harness.Harness{Agent: "agent.md"}

	t.Run("stages onto the hooks bootstrap", func(t *testing.T) {
		boot, err := newHarnessBootstrap(h, nil, "sb", "agent", "", nil, nil, "", "/sandbox/workspace/repo", false)
		require.NoError(t, err)
		note, err := attachTirithRepoPolicy(context.Background(), boot, dir, noEnv)
		require.NoError(t, err)
		assert.NotEmpty(t, note)
		hb, ok := boot.(*harnessBootstrapWithHooks)
		require.True(t, ok)
		files := security.HookFiles(hb.SandboxHookConfig())
		assert.Equal(t, testTirithPolicy, string(files[security.TirithRepoPolicyFile]))
	})

	t.Run("no target repo stages nothing", func(t *testing.T) {
		boot, err := newHarnessBootstrap(h, nil, "sb", "agent", "", nil, nil, "", "/sandbox/workspace/repo", false)
		require.NoError(t, err)
		note, err := attachTirithRepoPolicy(context.Background(), boot, "", noEnv)
		require.NoError(t, err)
		assert.Empty(t, note)
		assert.NotContains(t, security.HookFiles(boot.(*harnessBootstrapWithHooks).SandboxHookConfig()), security.TirithRepoPolicyFile)
	})

	t.Run("security disabled is left alone", func(t *testing.T) {
		off := false
		disabled := &harness.Harness{Agent: "agent.md", Security: &harness.SecurityConfig{Enabled: &off}}
		require.False(t, disabled.SecurityEnabled())
		boot, err := newHarnessBootstrap(disabled, nil, "sb", "agent", "", nil, nil, "", "/sandbox/workspace/repo", false)
		require.NoError(t, err)
		note, err := attachTirithRepoPolicy(context.Background(), boot, dir, noEnv)
		require.NoError(t, err)
		assert.Empty(t, note)
	})

	t.Run("staging error is returned", func(t *testing.T) {
		bad := newPolicyRepo(t, map[string]string{tirithRepoPolicyPath: strings.Repeat("#", tirithRepoPolicyMaxBytes+1)})
		boot, err := newHarnessBootstrap(h, nil, "sb", "agent", "", nil, nil, "", "/sandbox/workspace/repo", false)
		require.NoError(t, err)
		_, err = attachTirithRepoPolicy(context.Background(), boot, bad, noEnv)
		require.Error(t, err)
	})
}
