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
