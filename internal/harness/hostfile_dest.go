package harness

import (
	"fmt"
	"path"
	"strings"
)

// Runner-owned sandbox paths a host_files dest may not name. host_files are
// uploaded after the runner has written these, so a dest here would replace
// what the runner put in place:
//
//   - reservedHostFileExact: the workspace .env, which the runner writes in
//     bootstrapEnv and every runtime sources before launching its agent.
//   - reservedHostFileBinaries: names under /sandbox/workspace/bin, which .env
//     puts on PATH. The runtime CLIs (runtime Name(): claude, codex, pi,
//     opencode — literal here because internal/runtime imports this package)
//     and the binaries bootstrapCommon uploads there (fullsend,
//     fullsend-check-output; they exist only there, so the directory being
//     last on PATH does not protect them).
//   - reservedHostFileDirs: the runtime config homes (hooks, settings, auth
//     helpers), the runner's .fullsend directory (output schema,
//     iteration.env), its .security findings log, and the /sandbox PATH
//     directories that precede the image's system directories. Anything at
//     or below them is reserved.
//
// Other names under /sandbox/workspace/bin (e.g. my-tool.sh) and files under
// /sandbox/workspace/.env.d stay available to harnesses.
//
// The paths are literals because internal/sandbox's tests import this
// package; TestHostFileDestReservesSandboxPaths in internal/sandbox keeps them
// in step with the sandbox.Sandbox* constants.
const hostFileSandboxWorkspace = "/sandbox/workspace"

var (
	reservedHostFileExact = []string{
		hostFileSandboxWorkspace + "/.env",
	}
	reservedHostFileBinDir   = hostFileSandboxWorkspace + "/bin"
	reservedHostFileBinaries = []string{
		"claude",
		"codex",
		"pi",
		"opencode",
		"fullsend",
		"fullsend-check-output",
	}
	reservedHostFileDirs = []string{
		"/sandbox/claude-config",
		"/sandbox/codex-config",
		"/sandbox/pi-config",
		hostFileSandboxWorkspace + "/.fullsend",
		// The security findings log the runner creates in bootstrapCommon
		// and reads back after the run.
		hostFileSandboxWorkspace + "/.security",
		// PATH directories under /sandbox that come before the image's
		// system directories: the image's virtualenv (first on its PATH,
		// where the hooks' python3 lives) and $HOME/go/bin, which .env puts
		// before the image PATH. A file landing there would replace a
		// command rather than add one.
		"/sandbox/.venv",
		"/sandbox/go/bin",
	}
)

// allowedHostFileRoots are the only trees a host_files dest may land in. The
// reserved list above is a string match, so it needs a closed set of roots
// to mean anything: /proc/self/root/..., /dev/fd/... and the like reach the
// same files under another name. /sandbox is the sandbox user's tree and
// /tmp its scratch space; nothing else is writable by that user anyway.
var allowedHostFileRoots = []string{"/sandbox", "/tmp"}

// ValidateHostFileDest reports whether dest is an absolute path under
// /sandbox or /tmp that does not name a runner-owned location. The path is
// cleaned first, so "..", "//" and a trailing "/" cannot reach a reserved
// path by another spelling.
func ValidateHostFileDest(dest string) error {
	if !strings.HasPrefix(dest, "/") {
		return fmt.Errorf("dest %q must be an absolute path", dest)
	}
	// Refused outright rather than cleaned away: in the sandbox ".." is
	// resolved after any symlink before it, which a lexical Clean cannot see.
	for _, part := range strings.Split(dest, "/") {
		if part == ".." {
			return fmt.Errorf("dest %q must not contain a \"..\" path component", dest)
		}
	}
	clean := path.Clean(dest)
	underRoot := false
	for _, root := range allowedHostFileRoots {
		if strings.HasPrefix(clean, root+"/") {
			underRoot = true
			break
		}
	}
	if !underRoot {
		return fmt.Errorf("dest %q must be under /sandbox/ or /tmp/", dest)
	}
	if hostFileDestReserved(clean) {
		return fmt.Errorf("dest %q is reserved for the runner", dest)
	}
	return nil
}

func hostFileDestReserved(clean string) bool {
	for _, p := range reservedHostFileExact {
		if clean == p {
			return true
		}
	}
	if path.Dir(clean) == reservedHostFileBinDir {
		base := path.Base(clean)
		for _, name := range reservedHostFileBinaries {
			if base == name {
				return true
			}
		}
	}
	for _, dir := range reservedHostFileDirs {
		if clean == dir || strings.HasPrefix(clean, dir+"/") {
			return true
		}
	}
	return false
}
