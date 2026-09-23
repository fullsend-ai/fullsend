package e2etest

import (
	"reflect"
	"testing"
)

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

// TestCLIBuildArgs exercises the exact `go build` argument wiring
// buildCLIBinary (build.go, e2e/behaviour only) relies on to stamp
// commitSHA. Without this untagged test, deleting the -ldflags branch from
// cliBuildArgs would still leave `make go-test` green, since no `make`
// target runs package e2etest's own e2e/behaviour-tagged tests (see
// TestBuildCLI in cli_test.go).
func TestCLIBuildArgs(t *testing.T) {
	const binary = "/tmp/fullsend"

	got := cliBuildArgs(binary, "")
	want := []string{"build", "-o", binary, "./cmd/fullsend/"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cliBuildArgs(%q, \"\") = %#v, want %#v", binary, got, want)
	}

	const sha = "abcdef0123456789abcdef0123456789abcdef01"
	got = cliBuildArgs(binary, sha)
	want = []string{
		"build",
		"-ldflags", commitSHALdflags(sha),
		"-o", binary,
		"./cmd/fullsend/",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cliBuildArgs(%q, %q) = %#v, want %#v", binary, sha, got, want)
	}
}
