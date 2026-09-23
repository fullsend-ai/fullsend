package e2etest

import "fmt"

// commitSHALdflags returns the -ldflags argument that stamps
// internal/cli.commitSHA with sha, or "" when sha is empty. Extracted into
// an untagged file so this logic (which decides whether e2e/behaviour CLI
// builds pin scaffold refs to the commit under test, see buildCLIBinary in
// build.go) is exercised by `make go-test` even though buildCLIBinary
// itself only compiles under the e2e/behaviour build tags.
func commitSHALdflags(sha string) string {
	if sha == "" {
		return ""
	}
	return fmt.Sprintf("-X github.com/fullsend-ai/fullsend/internal/cli.commitSHA=%s", sha)
}

// cliBuildArgs returns the `go build` arguments buildCLIBinary passes to
// `go` to compile the fullsend CLI into binary. When sha is non-empty, the
// build stamps commitSHA via -ldflags so scaffold refs pin to the commit
// under test instead of falling back to main (see resolveUpstreamRef).
// Extracted into this untagged file so the exact wiring buildCLIBinary
// relies on is exercised by `make go-test`: buildCLIBinary itself only
// compiles under the e2e/behaviour build tags, which no `make` target runs
// for package e2etest's own tests (see TestBuildCLI in cli_test.go).
func cliBuildArgs(binary, sha string) []string {
	if sha == "" {
		return []string{"build", "-o", binary, "./cmd/fullsend/"}
	}
	return []string{
		"build",
		"-ldflags", commitSHALdflags(sha),
		"-o", binary,
		"./cmd/fullsend/",
	}
}
