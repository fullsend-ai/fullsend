// Package buildinfo holds build metadata stamped into the fullsend CLI at
// link time via -ldflags -X. It is a dependency-free leaf package so any
// internal package can read the version without importing internal/cli
// (which would pull in cobra and every subcommand, or create an import
// cycle).
//
// Build sites stamp it with:
//
//	-X github.com/fullsend-ai/fullsend/internal/buildinfo.version=<version>
//	-X github.com/fullsend-ai/fullsend/internal/buildinfo.commitSHA=<sha>
package buildinfo

// version and commitSHA must stay uninitialized-or-constant string vars:
// the linker's -X flag only overwrites vars of that shape.
var (
	version   = "dev"
	commitSHA = "dev"
)

// Version returns the CLI version string set at build time, or "dev".
func Version() string {
	return version
}

// CommitSHA returns the git commit SHA set at build time, or "dev".
func CommitSHA() string {
	return commitSHA
}
