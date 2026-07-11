//go:build behaviour

package emulate_test

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/e2e/behaviour/drivers/scm"
	"github.com/fullsend-ai/fullsend/e2e/behaviour/drivers/scm/emulate"
)

// Compile-time check: Instance satisfies scm.Driver.
var _ scm.Driver = (*emulate.Instance)(nil)

const (
	testOrg  = "emu-org"
	testRepo = "emu-repo"
)

var inst *emulate.Instance

// TestMain starts one shared Instance for the whole package — spawning
// `npx emulate` per test function would pay Node startup cost for no
// isolation benefit, since CreateIssue returns a fresh, unique number on
// every call and tests only ever touch the issue they created.
func TestMain(m *testing.M) {
	if _, err := exec.LookPath("npx"); err != nil {
		if inCI() {
			// scm-emulate.yml runs actions/setup-node before this suite, so
			// a missing npx in CI means Node setup broke, not that Node is
			// unavailable by design. Failing loud here prevents a silently
			// green job that ran zero tests.
			println("emulate: npx not found on PATH in CI (broken Node setup?):", err.Error())
			os.Exit(1)
		}
		// No Node/npx on PATH locally — skip the whole binary rather than
		// fail, since Node isn't a universal prerequisite for this repo.
		os.Exit(0)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	started, err := emulate.Start(ctx, emulate.SeedOptions{Org: testOrg, Repo: testRepo}, func(format string, args ...any) {
		// Discard subprocess log lines in the test run; keep them for
		// manual debugging by swapping this for t.Logf-style output.
	})
	if err != nil {
		println("emulate: failed to start:", err.Error())
		os.Exit(1)
	}
	inst = started

	code := m.Run()
	_ = inst.Close()
	os.Exit(code)
}

// inCI reports whether the test binary is running in GitHub Actions (or
// any CI system that sets the conventional CI env var).
func inCI() bool {
	return os.Getenv("CI") == "true" || os.Getenv("GITHUB_ACTIONS") == "true"
}

func TestCreateAndLabelIssue(t *testing.T) {
	ctx := context.Background()

	issue, err := inst.CreateIssue(ctx, testOrg, testRepo, "Login fails", "steps to reproduce")
	require.NoError(t, err)
	require.NotZero(t, issue.Number)
	assert.Equal(t, "Login fails", issue.Title)
	assert.Equal(t, "steps to reproduce", issue.Body)

	require.NoError(t, inst.AddIssueLabels(ctx, testOrg, testRepo, issue.Number, "ready-for-triage"))

	got, err := inst.GetIssue(ctx, testOrg, testRepo, issue.Number)
	require.NoError(t, err)
	assert.Contains(t, got.Labels, "ready-for-triage")
}

func TestCommentAndClose(t *testing.T) {
	ctx := context.Background()

	issue, err := inst.CreateIssue(ctx, testOrg, testRepo, "Flaky test", "intermittent failure")
	require.NoError(t, err)

	comment, err := inst.AddComment(ctx, testOrg, testRepo, issue.Number, "investigating")
	require.NoError(t, err)
	assert.Equal(t, "investigating", comment.Body)
	assert.NotZero(t, comment.ID)

	// forge.Issue has no State field, so CloseIssue's effect isn't
	// re-verifiable through GetIssue with the current driver — this only
	// asserts the state-transition PATCH itself succeeds against the
	// emulator.
	require.NoError(t, inst.CloseIssue(ctx, testOrg, testRepo, issue.Number))
}

func TestCommitFile(t *testing.T) {
	// Skipped: emulate@0.8.0's GET /repos/:owner/:repo/git/commits/:sha
	// route reuses the REST commits-API formatter (nested commit.tree.sha,
	// GitHub-user author/committer) instead of the git-data API's
	// documented shape (top-level tree.sha, git-author name/email/date) —
	// confirmed by reading packages/@emulators/github/src/routes/branches.ts
	// (formatCommitJson, used by both routes). forge/github.LiveClient's
	// CommitFiles correctly expects the real, documented git-data shape,
	// so it fails to find commitObj.Tree.SHA against this emulator. This is
	// an emulate bug, not a fullsend one — CommitFile is already covered
	// against live GitHub via steps/dummy_agent.go and steps/cleanup.go in
	// the existing behaviour suite. Reported upstream:
	// https://github.com/vercel-labs/emulate/issues/190 — un-skip once
	// fixed and the pinned version is bumped past it.
	t.Skip("emulate git/commits response shape bug — see comment, vercel-labs/emulate#190")

	ctx := context.Background()

	path := "committed/hello.txt"
	content := []byte("hello from emulate driver test\n")

	require.NoError(t, inst.CommitFile(ctx, testOrg, testRepo, path, "test: add hello.txt", content))

	got, err := inst.Client().GetFileContent(ctx, testOrg, testRepo, path)
	require.NoError(t, err)
	assert.Equal(t, content, got)
}
