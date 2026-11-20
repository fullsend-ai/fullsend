package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/security"
)

const testTirithPolicy = "approval_rules:\n  - rule_ids: [package_lookup_incomplete]\n"

// testTirithHeadPolicy is what a request's head commits in place of the base
// policy. It must never be staged.
const testTirithHeadPolicy = "# head copy\n"

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

// newRequestUpstream creates an upstream repository whose "main" branch tip
// (the request's base) holds testTirithPolicy and whose "head" branch, one
// commit on top, replaces it with testTirithHeadPolicy. It returns the
// upstream directory and the base commit.
func newRequestUpstream(t *testing.T) (string, string) {
	t.Helper()
	upstream := newPolicyRepo(t, map[string]string{tirithRepoPolicyPath: testTirithPolicy})
	gitT(t, upstream, "branch", "-M", "main")
	base := gitT(t, upstream, "rev-parse", "HEAD")
	gitT(t, upstream, "checkout", "-q", "-b", "head")
	require.NoError(t, os.WriteFile(filepath.Join(upstream, tirithRepoPolicyPath), []byte(testTirithHeadPolicy), 0o644))
	gitT(t, upstream, "commit", "-q", "-am", "head")
	gitT(t, upstream, "checkout", "-q", "main")
	return upstream, base
}

// shallowHeadCheckout clones only the request's head commit, as a shallow
// review checkout does: the base commit is not in it.
func shallowHeadCheckout(t *testing.T, upstream, base string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "checkout")
	gitT(t, t.TempDir(), "clone", "-q", "--depth=1", "--branch", "head", "file://"+upstream, dir)
	require.False(t, gitCommitExists(context.Background(), dir, base), "the shallow checkout must not start with the base commit")
	return dir
}

func TestTrustedTirithRepoPolicy_FilePresent(t *testing.T) {
	dir := newPolicyRepo(t, map[string]string{tirithRepoPolicyPath: testTirithPolicy})
	policy, note, err := trustedTirithRepoPolicy(context.Background(), dir, tirithPolicySource{})
	require.NoError(t, err)
	assert.Equal(t, testTirithPolicy, string(policy))
	assert.Contains(t, note.text, "Staging repository tirith policy")
	assert.False(t, note.omitted)
}

func TestTrustedTirithRepoPolicy_FileAbsent(t *testing.T) {
	dir := newPolicyRepo(t, map[string]string{"README.md": "hi\n"})
	policy, note, err := trustedTirithRepoPolicy(context.Background(), dir, tirithPolicySource{})
	require.NoError(t, err)
	assert.Nil(t, policy)
	assert.Equal(t, tirithPolicyNote{}, note)
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
	assert.Equal(t, tirithPolicyNote{}, note)
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
	require.NoError(t, os.WriteFile(filepath.Join(dir, tirithRepoPolicyPath), []byte(testTirithHeadPolicy), 0o644))
	gitT(t, dir, "commit", "-q", "-am", "head")

	policy, note, err := trustedTirithRepoPolicy(context.Background(), dir, tirithPolicySource{requestRun: true, baseSHA: strings.ToUpper(base)})
	require.NoError(t, err)
	assert.Equal(t, testTirithPolicy, string(policy))
	assert.Contains(t, note.text, base[:12])
	assert.False(t, note.omitted)
}

func TestTrustedTirithRepoPolicy_RequestRunWithoutBaseStagesNothing(t *testing.T) {
	// No origin remote, so a base commit that is not in the checkout cannot
	// be fetched either.
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
			assert.Contains(t, note.text, "not staged")
			assert.True(t, note.omitted)
		})
	}
}

func TestTrustedTirithRepoPolicy_ShallowCheckoutFetchesBase(t *testing.T) {
	upstream, base := newRequestUpstream(t)
	dir := shallowHeadCheckout(t, upstream, base)

	policy, note, err := trustedTirithRepoPolicy(context.Background(), dir, tirithPolicySource{requestRun: true, baseSHA: base})
	require.NoError(t, err)
	assert.Equal(t, testTirithPolicy, string(policy), "the base copy is staged, never the head copy")
	assert.Contains(t, note.text, base[:12])
	assert.False(t, note.omitted)
	assert.Equal(t, "true", gitT(t, dir, "rev-parse", "--is-shallow-repository"), "the checkout stays shallow")
	assert.Equal(t, testTirithHeadPolicy, gitT(t, dir, "show", "HEAD:"+tirithRepoPolicyPath)+"\n", "the checkout's HEAD is unchanged")
}

func TestTrustedTirithRepoPolicy_FullCheckoutFetchesMissingBase(t *testing.T) {
	upstream, _ := newRequestUpstream(t)
	// The base branch moves on after the request branched: the new base is
	// not in a full checkout of the request's head branch.
	const newBasePolicy = "approval_rules:\n  - rule_ids: [curl_pipe_shell]\n"
	require.NoError(t, os.WriteFile(filepath.Join(upstream, tirithRepoPolicyPath), []byte(newBasePolicy), 0o644))
	gitT(t, upstream, "commit", "-q", "-am", "base moves on")
	base := gitT(t, upstream, "rev-parse", "HEAD")
	dir := filepath.Join(t.TempDir(), "checkout")
	gitT(t, t.TempDir(), "clone", "-q", "--single-branch", "--branch", "head", "file://"+upstream, dir)
	require.False(t, gitCommitExists(context.Background(), dir, base))

	policy, note, err := trustedTirithRepoPolicy(context.Background(), dir, tirithPolicySource{requestRun: true, baseSHA: base})
	require.NoError(t, err)
	assert.Equal(t, newBasePolicy, string(policy))
	assert.False(t, note.omitted)
	assert.Equal(t, "false", gitT(t, dir, "rev-parse", "--is-shallow-repository"), "a full checkout is not made shallow")
}

func TestTrustedTirithRepoPolicy_UnfetchableBaseStagesNothing(t *testing.T) {
	upstream, base := newRequestUpstream(t)
	dir := shallowHeadCheckout(t, upstream, base)
	gitT(t, dir, "remote", "set-url", "origin", "file://"+filepath.Join(t.TempDir(), "gone"))

	policy, note, err := trustedTirithRepoPolicy(context.Background(), dir, tirithPolicySource{requestRun: true, baseSHA: base})
	require.NoError(t, err)
	assert.Nil(t, policy, "the head copy must never be staged")
	assert.True(t, note.omitted)
	assert.Contains(t, note.text, base[:12])
	assert.Contains(t, note.text, "could not be fetched")
	assert.NotContains(t, note.text, "gone", "the remote URL is not reported")
	assert.LessOrEqual(t, len([]rune(note.text)), 200, "the note fits the status comment's detail limit")
}

func TestFetchGitCommit_RejectsNonObjectName(t *testing.T) {
	err := fetchGitCommit(context.Background(), t.TempDir(), "--upload-pack=touch /tmp/x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a full object name")
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

// stagedTirithPolicy returns the policy staged on boot, or nil.
func stagedTirithPolicy(t *testing.T, boot any) []byte {
	t.Helper()
	hb, ok := boot.(*harnessBootstrapWithHooks)
	require.True(t, ok)
	return security.HookFiles(hb.SandboxHookConfig())[security.TirithRepoPolicyFile]
}

func TestAttachTirithRepoPolicy(t *testing.T) {
	dir := newPolicyRepo(t, map[string]string{tirithRepoPolicyPath: testTirithPolicy})
	noEnv := func(string) string { return "" }
	h := &harness.Harness{Agent: "agent.md"}

	t.Run("stages onto the hooks bootstrap", func(t *testing.T) {
		boot, err := newHarnessBootstrap(h, nil, "sb", "agent", "", nil, nil, "", "/sandbox/workspace/repo", false)
		require.NoError(t, err)
		note, err := attachTirithRepoPolicy(context.Background(), boot, dir, noEnv, nil)
		require.NoError(t, err)
		assert.NotEmpty(t, note.text)
		assert.Equal(t, testTirithPolicy, string(stagedTirithPolicy(t, boot)))
	})

	t.Run("ordinary run never asks for a merge request base", func(t *testing.T) {
		boot, err := newHarnessBootstrap(h, nil, "sb", "agent", "", nil, nil, "", "/sandbox/workspace/repo", false)
		require.NoError(t, err)
		resolver := func(context.Context) (string, error) {
			t.Fatal("the resolver must not be called for an ordinary run")
			return "", nil
		}
		note, err := attachTirithRepoPolicy(context.Background(), boot, dir, noEnv, resolver)
		require.NoError(t, err)
		assert.False(t, note.omitted)
		assert.Equal(t, testTirithPolicy, string(stagedTirithPolicy(t, boot)))
	})

	t.Run("no target repo stages nothing", func(t *testing.T) {
		boot, err := newHarnessBootstrap(h, nil, "sb", "agent", "", nil, nil, "", "/sandbox/workspace/repo", false)
		require.NoError(t, err)
		note, err := attachTirithRepoPolicy(context.Background(), boot, "", noEnv, nil)
		require.NoError(t, err)
		assert.Equal(t, tirithPolicyNote{}, note)
		assert.Nil(t, stagedTirithPolicy(t, boot))
	})

	t.Run("security disabled is left alone", func(t *testing.T) {
		off := false
		disabled := &harness.Harness{Agent: "agent.md", Security: &harness.SecurityConfig{Enabled: &off}}
		require.False(t, disabled.SecurityEnabled())
		boot, err := newHarnessBootstrap(disabled, nil, "sb", "agent", "", nil, nil, "", "/sandbox/workspace/repo", false)
		require.NoError(t, err)
		note, err := attachTirithRepoPolicy(context.Background(), boot, dir, noEnv, nil)
		require.NoError(t, err)
		assert.Equal(t, tirithPolicyNote{}, note)
	})

	t.Run("staging error is returned", func(t *testing.T) {
		bad := newPolicyRepo(t, map[string]string{tirithRepoPolicyPath: strings.Repeat("#", tirithRepoPolicyMaxBytes+1)})
		boot, err := newHarnessBootstrap(h, nil, "sb", "agent", "", nil, nil, "", "/sandbox/workspace/repo", false)
		require.NoError(t, err)
		_, err = attachTirithRepoPolicy(context.Background(), boot, bad, noEnv, nil)
		require.Error(t, err)
	})
}

// TestAttachTirithRepoPolicy_GitLabMergeRequest covers a GitLab merge request
// run: the event carries no base commit, so it comes from the forge API.
func TestAttachTirithRepoPolicy_GitLabMergeRequest(t *testing.T) {
	h := &harness.Harness{Agent: "agent.md"}
	mrEnv := func(k string) string {
		if k == "FULLSEND_NOTE_TARGET" {
			return "merge_requests"
		}
		return ""
	}
	newBoot := func(t *testing.T) any {
		boot, err := newHarnessBootstrap(h, nil, "sb", "agent", "", nil, nil, "", "/sandbox/workspace/repo", false)
		require.NoError(t, err)
		return boot
	}

	t.Run("stages the base copy from a shallow head checkout", func(t *testing.T) {
		upstream, base := newRequestUpstream(t)
		dir := shallowHeadCheckout(t, upstream, base)
		boot := newBoot(t)
		calls := 0
		resolver := func(context.Context) (string, error) { calls++; return base, nil }

		note, err := attachTirithRepoPolicy(context.Background(), boot.(*harnessBootstrapWithHooks), dir, mrEnv, resolver)
		require.NoError(t, err)
		assert.Equal(t, 1, calls)
		assert.False(t, note.omitted)
		assert.Contains(t, note.text, base[:12])
		assert.Equal(t, testTirithPolicy, string(stagedTirithPolicy(t, boot)), "the head copy must never be staged")
	})

	t.Run("unresolvable base stages nothing and says why", func(t *testing.T) {
		upstream, base := newRequestUpstream(t)
		dir := shallowHeadCheckout(t, upstream, base)
		boot := newBoot(t)
		resolver := func(context.Context) (string, error) {
			return "", errors.New("GET https://gitlab.example/api/v4/...: 403")
		}

		note, err := attachTirithRepoPolicy(context.Background(), boot.(*harnessBootstrapWithHooks), dir, mrEnv, resolver)
		require.NoError(t, err)
		assert.True(t, note.omitted)
		assert.Contains(t, note.text, "could not be read from the forge API")
		assert.NotContains(t, note.text, "gitlab.example", "the API error is not reported")
		assert.Nil(t, stagedTirithPolicy(t, boot), "the head copy must never be staged")
	})

	t.Run("without a resolver nothing is staged", func(t *testing.T) {
		upstream, base := newRequestUpstream(t)
		dir := shallowHeadCheckout(t, upstream, base)
		boot := newBoot(t)

		note, err := attachTirithRepoPolicy(context.Background(), boot.(*harnessBootstrapWithHooks), dir, mrEnv, nil)
		require.NoError(t, err)
		assert.True(t, note.omitted)
		assert.Contains(t, note.text, "not known")
		assert.Nil(t, stagedTirithPolicy(t, boot))
	})
}

func TestMergeRequestBaseSHA(t *testing.T) {
	base := strings.Repeat("d", 40)
	fc := forge.NewFakeClient()
	fc.PullRequestInfos = map[string]forge.PullRequestInfo{
		"group/sub/project/7": {Number: 7, BaseSHA: base},
		"group/sub/project/8": {Number: 8},
	}

	got, err := mergeRequestBaseSHA(context.Background(), fc, "group/sub/project", 7)
	require.NoError(t, err)
	assert.Equal(t, base, got)

	_, err = mergeRequestBaseSHA(context.Background(), fc, "group/sub/project", 8)
	require.ErrorContains(t, err, "no base commit")

	_, err = mergeRequestBaseSHA(context.Background(), fc, "group/sub/project", 9)
	require.Error(t, err)

	_, err = mergeRequestBaseSHA(context.Background(), fc, "project", 7)
	require.ErrorContains(t, err, "namespace/project")
}

func TestGitLabMergeRequestBaseResolver(t *testing.T) {
	mr := statusOpts{statusRepo: "group/project", statusNum: 7}
	assert.Nil(t, gitLabMergeRequestBaseResolver("github", mr))
	assert.Nil(t, gitLabMergeRequestBaseResolver("gitlab", statusOpts{statusNum: 7}))
	assert.Nil(t, gitLabMergeRequestBaseResolver("gitlab", statusOpts{statusRepo: "group/project"}))

	resolver := gitLabMergeRequestBaseResolver("gitlab", mr)
	require.NotNil(t, resolver)
	t.Setenv("GITLAB_TOKEN", "")
	_, err := resolver(context.Background())
	require.ErrorContains(t, err, "GITLAB_TOKEN")
}

func TestCIWarningAnnotation(t *testing.T) {
	gha := func(k string) string {
		if k == "GITHUB_ACTIONS" {
			return "true"
		}
		return ""
	}
	assert.Empty(t, ciWarningAnnotation(func(string) string { return "" }, "msg"))
	assert.Equal(t, "::warning::policy not staged", ciWarningAnnotation(gha, "policy not staged"))
	assert.Equal(t, "::warning::50%25 a%0D%0A::error::b", ciWarningAnnotation(gha, "50% a\r\n::error::b"),
		"a newline cannot start a second workflow command")
}
