package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/pluginformat"
)

// Declared pi extensions (harness `extensions:`, ADR 0094). Bootstrap
// uploads each directory to ConfigDir/extensions/<name>/ and records it in
// the manifest; Run re-hashes the host directory, preflights the sandbox
// copy against that hash (piExtensionsGuard) and loads it with `-e`.
//
// The expected hash the guard embeds comes from the host directory at Run
// time, never from the manifest: the manifest lives in the agent-writable
// config dir, so a value read back from it could be rewritten together with
// the extension between iterations. The manifest copy is informational —
// the hook adapter reads the names for its roster and extension-tool
// handling.

// piExtensionTamperedExit is the exit code of the extension preflight when
// a declared extension directory is missing from the sandbox or its tree
// hash no longer matches the host copy. Distinct from piHooksMissingExit
// and piConfigTamperedExit so Run can name the cause.
const piExtensionTamperedExit = 96

// piReservedExtensionNames are sandbox names an extension may not take:
// the hook adapter's file basename and the vendored provider extensions
// Run loads by path. A declared extension with one of these names would
// shadow (or be mistaken for) runner-owned code. The list is defined in
// internal/pluginformat so harness validation can refuse such an entry at
// harness load, with the offending index named, instead of only here.
var piReservedExtensionNames = pluginformat.PiReservedExtensionNames

// piManifestExtension is one `extensions` entry in fullsend-manifest.json
// and the resolved form Run renders the command line from.
type piManifestExtension struct {
	Name string `json:"name"`
	// Path is the extension directory inside the sandbox.
	Path string `json:"path"`
	// SHA256 is the tree hash (piExtensionTreeHash) of the host directory.
	SHA256 string            `json:"sha256"`
	Args   []string          `json:"args,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
}

func (r PiRuntime) piExtensionsDir() string { return r.ConfigDir() + "/extensions" }

// piResolveRunExtensions turns the runner's ExtensionInputs into manifest
// entries: sandbox path, host tree hash, args and env. Both Bootstrap and
// Run call it so the two agree on the hash by construction. Name
// collisions between entries and with piReservedExtensionNames are errors
// (sandbox.UploadDir replaces its destination wholesale, so a collision
// would silently drop one extension).
func piResolveRunExtensions(inputs []ExtensionInput) ([]piManifestExtension, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	paths := make([]string, 0, len(inputs))
	for _, in := range inputs {
		if in.Path == "" {
			continue
		}
		// duplicateDestinationNameError keys on the path basename; an
		// explicit Name that differs from it is checked through a
		// synthetic path so both collide the same way.
		paths = append(paths, filepath.Join(filepath.Dir(in.Path), in.SandboxName()))
	}
	if err := duplicateDestinationNameError("extension", paths, piReservedExtensionNames...); err != nil {
		return nil, err
	}
	r := PiRuntime{}
	exts := make([]piManifestExtension, 0, len(inputs))
	for _, in := range inputs {
		if in.Path == "" {
			continue
		}
		sum, err := piExtensionTreeHash(in.Path)
		if err != nil {
			return nil, fmt.Errorf("hashing pi extension %q (%s): %w", in.SandboxName(), in.Path, err)
		}
		exts = append(exts, piManifestExtension{
			Name:   in.SandboxName(),
			Path:   r.piExtensionsDir() + "/" + in.SandboxName(),
			SHA256: sum,
			Args:   append([]string(nil), in.Args...),
			Env:    cloneStringMap(in.Env),
		})
	}
	return exts, nil
}

func cloneStringMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Tree hash. One definition, implemented twice — piExtensionTreeHash in Go
// for the host copy, piTreeHashCommand as POSIX sh for the sandbox copy —
// and the two must agree byte for byte (TestPiExtensionTreeHash_MatchesShell):
//
//   - Regular files and directories only. A symlink, socket, fifo or device
//     node anywhere in the tree is refused: on the host piExtensionTreeHash
//     returns an error naming the entry, in the sandbox the pipeline prints
//     nothing so the guard's comparison fails closed. pi's `-e <dir>` loader
//     follows symlinks when it resolves an entry point, so a symlink left
//     out of the verdict is a way to swap an extension's code without
//     moving its hash. Trees fetched from a forge cannot carry symlinks
//     anyway, so nothing legitimate is lost.
//   - One line per regular file in GNU sha256sum's output form:
//     "<sha256 hex>  ./<relative path>" (two spaces; slash-separated path
//     prefixed with "./" as `find .` prints it), sorted bytewise
//     (LC_ALL=C sort), newline-terminated.
//   - Then one trailing line for the directory set: the SHA-256 of the
//     sorted `find . -type d` listing ("." for the root, "./<relative path>"
//     below it), rendered in sha256sum's read-from-stdin form "<hex>  -".
//     Directories are hashed because pi reacts to directory *names*: an
//     `extensions/`, `skills/`, `prompts/` or `themes/` directory turns the
//     extension into package layout and index.js stops being an entry
//     point, so a bare `mkdir skills` disables an extension. The digest is
//     appended after the sorted file lines rather than sorted together with
//     them so the shell side stays a plain pipeline.
//   - The hash is the SHA-256 of those lines concatenated.
//   - Path names containing a newline, a carriage return or a backslash are
//     refused on the host: GNU sha256sum escapes all three and prefixes the
//     line with "\", which the Go side does not mirror, and a newline would
//     break the directory listing too.
//
// Both refusals are pluginformat.ExtensionEntryProblem, shared with harness
// validation and the bootstrap injection scan.

// piExtensionTreeHash computes the tree hash of dir on the host. The root
// itself may be a symlink (cache paths are named symlinks into the
// content-addressed store); nothing below it may be.
func piExtensionTreeHash(dir string) (string, error) {
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	var fileLines, dirNames []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		// One rule, three call sites: harness validation
		// (pluginformat.PiLoadProblem) and the bootstrap injection scan
		// apply the same predicate, so an author learns about a symlink or an
		// unreproducible name at validation instead of here.
		if problem := pluginformat.ExtensionEntryProblem(rel, d.Type()); problem != "" {
			return errors.New(problem)
		}
		if d.IsDir() {
			if rel == "." {
				dirNames = append(dirNames, ".")
			} else {
				dirNames = append(dirNames, "./"+rel)
			}
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		fileLines = append(fileLines, hex.EncodeToString(h.Sum(nil))+"  ./"+rel)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(fileLines)
	sort.Strings(dirNames)
	dirSum := sha256.Sum256([]byte(piHashLines(dirNames)))
	h := sha256.New()
	h.Write([]byte(piHashLines(fileLines)))
	h.Write([]byte(hex.EncodeToString(dirSum[:]) + "  -\n"))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// piHashLines renders sorted lines the way `sort` writes them: every line
// newline-terminated, nothing at all when there are none.
func piHashLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// piSha256Tool is how the sandbox-side pipeline names sha256sum: resolved
// through the default PATH once (`command -pv`), so neither a PATH entry
// nor a shell function the agent left behind can stand in, and usable as
// find's -exec program, which a builtin-prefixed `command -p sha256sum`
// could not be. Tests substitute a shim on hosts without GNU sha256sum.
const piSha256Tool = `"$(command -pv sha256sum)"`

// piTreeHashCommand renders the POSIX sh pipeline that prints the tree
// hash of dir (see the definition above), and prints nothing at all when
// the tree holds an entry that is neither a regular file nor a directory,
// so the guard comparing its output fails closed on a planted symlink.
// find's output order is unspecified, hence the sorts; `command -p` keeps
// find, sort, head and cut on the default PATH.
func piTreeHashCommand(dir, shaTool string) string {
	return "cd " + shellQuote(dir) +
		` && [ -z "$(command -p find . ! -type f ! -type d | command -p head -c1)" ]` +
		" && { command -p find . -type f -exec " + shaTool + " {} +" +
		" | LC_ALL=C command -p sort;" +
		" command -p find . -type d | LC_ALL=C command -p sort | " + shaTool + "; }" +
		" | " + shaTool +
		" | command -p cut -d' ' -f1"
}

// piExtensionsGuard is the POSIX sh fragment run before pi, and before the
// agent-writable .env is sourced, when the harness declares extensions:
// every extension directory must exist in the sandbox and hash to the
// value computed from the host copy, else the iteration stops with
// piExtensionTamperedExit before any extension code can run. Empty when
// there are no extensions.
func piExtensionsGuard(exts []piManifestExtension) string {
	return piExtensionsGuardWith(exts, piSha256Tool)
}

func piExtensionsGuardWith(exts []piManifestExtension, shaTool string) string {
	if len(exts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(exts))
	for _, e := range exts {
		msg := fmt.Sprintf(`fullsend: pi extension "%s" is missing or was modified`, sanitizeOutput(e.Name))
		parts = append(parts, fmt.Sprintf(`{ test -d %s && [ "$(%s)" = %s ] || { echo %s >&2; exit %d; }; }`,
			shellQuote(e.Path), piTreeHashCommand(e.Path, shaTool), shellQuote(e.SHA256), shellQuote(msg), piExtensionTamperedExit))
	}
	return strings.Join(parts, " && ")
}

// piExtensionArgs renders the `-e <path> <args...>` fragment for the
// declared extensions, in harness order. Provider extensions and the hook
// adapter are loaded before these: pi runs tool_call handlers in -e order
// and the first `block` wins, so the adapter's PreToolUse hooks see every
// call before any declared extension's handler does.
func piExtensionArgs(exts []piManifestExtension) []string {
	var parts []string
	for _, e := range exts {
		parts = append(parts, "-e "+shellQuote(e.Path))
		for _, a := range e.Args {
			parts = append(parts, shellQuote(a))
		}
	}
	return parts
}

// piExtensionEnvExports renders `export K='v'` for every declared
// extension's env, in harness order with keys sorted within an extension.
// They go right before pi, after the runtime's own exports and provider
// hygiene; harness validation refuses the reserved names those steps set.
func piExtensionEnvExports(exts []piManifestExtension) []string {
	var parts []string
	for _, e := range exts {
		keys := make([]string, 0, len(e.Env))
		for k := range e.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			parts = append(parts, "export "+k+"="+shellQuote(e.Env[k]))
		}
	}
	return parts
}
