package e2etest

import "testing"

func TestCommitSHALdflags(t *testing.T) {
	if got := commitSHALdflags(""); got != "" {
		t.Errorf("commitSHALdflags(\"\") = %q, want empty string", got)
	}

	const sha = "abcdef0123456789abcdef0123456789abcdef01"
	want := "-X github.com/fullsend-ai/fullsend/internal/cli.commitSHA=" + sha
	if got := commitSHALdflags(sha); got != want {
		t.Errorf("commitSHALdflags(%q) = %q, want %q", sha, got, want)
	}
}
