package e2etest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIBuildArgs_StampsFullSHAOnly(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	assert.Equal(t,
		[]string{"build", "-ldflags", "-X github.com/fullsend-ai/fullsend/internal/cli.upstreamRefOverride=" + sha, "-o", "/tmp/fullsend", "./cmd/fullsend/"},
		cliBuildArgs("/tmp/fullsend", sha))

	for _, notSHA := range []string{"", "0123456", "main", sha + "\n", "0123456789ABCDEF0123456789ABCDEF01234567"} {
		assert.Equal(t, []string{"build", "-o", "/tmp/fullsend", "./cmd/fullsend/"},
			cliBuildArgs("/tmp/fullsend", notSHA), "input %q", notSHA)
	}
}

func TestE2EUpstreamRef_StampsOnlyPublishedHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	head := headCommit(repo)
	require.Len(t, head, 40)

	var asked []string
	published := func(sha string) bool { asked = append(asked, sha); return true }
	assert.Equal(t, head, e2eUpstreamRef(repo, published))
	assert.Equal(t, []string{head}, asked, "published must be asked about HEAD")

	unpublished := func(string) bool { return false }
	assert.Empty(t, e2eUpstreamRef(repo, unpublished), "an unpublished HEAD must not be stamped")

	never := func(string) bool { t.Fatal("published called without a checkout HEAD"); return false }
	assert.Empty(t, e2eUpstreamRef(t.TempDir(), never), "not a checkout")
}

func TestHeadCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	git := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return string(out)
	}
	git("init", "-q")
	git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	want := git("rev-parse", "HEAD")

	assert.Equal(t, want[:40], headCommit(repo), "top of a checkout")

	sub := filepath.Join(repo, "sub")
	require.NoError(t, os.Mkdir(sub, 0o755))
	assert.Empty(t, headCommit(sub), "inside a checkout but not its top")

	assert.Empty(t, headCommit(t.TempDir()), "not a checkout")
}

func TestFetchable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	upstream := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		out, err := exec.Command("git", append([]string{"-C", upstream}, args...)...).CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
	}
	url := "file://" + upstream

	assert.True(t, fetchable(url, headCommit(upstream)), "a commit the repository has")
	assert.False(t, fetchable(url, "0123456789abcdef0123456789abcdef01234567"), "a commit it does not have")
	assert.False(t, fetchable("file://"+filepath.Join(upstream, "missing"), headCommit(upstream)), "no repository at url")
}
