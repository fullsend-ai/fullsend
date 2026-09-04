package pluginformat

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// The pi half of the format rule: what pi's `-e <dir>` loader accepts, the
// options an entry's `pi.args` may carry, and the names the runner owns.
// Everything here mirrors pi's own source (read at 0.84.4) rather than
// fullsend policy, so a harness never ships a directory pi would refuse or
// silently load nothing from.

// PiReservedExtensionNames are the sandbox names the pi runtime owns: the
// hook adapter's file basename and the vendored provider extensions Run
// loads by path. A declared extension uploads under its directory
// basename, so one of these names would shadow — or be mistaken for —
// runner-owned code. runtime.piResolveRunPlugins refuses them again at
// bootstrap; the check here is so a harness author learns at load which
// entry is the problem. The list lives in this package because both
// internal/harness and internal/runtime read it.
var PiReservedExtensionNames = []string{"fullsend-hooks", "fullsend-agent", "anthropic-vertex", "xai-vertex"}

// piReservedOptions are pi's own command-line options (cli/args.ts, read
// at 0.84.4). An extension's args are appended verbatim after its
// `-e <path>` and pi matches its own options first, so an unfiltered list
// could re-open approvals, load a second extension from the agent-writable
// workspace, or swap the model. `--debug` is deliberately absent: pi has no
// such option (fullsend's own CLI does), so an extension may register it.
var piReservedOptions = map[string]bool{
	"--extension": true, "--no-extensions": true, "--approve": true, "--no-approve": true,
	"--tools": true, "--no-tools": true, "--no-builtin-tools": true, "--exclude-tools": true,
	"--model": true, "--models": true, "--provider": true, "--thinking": true, "--api-key": true,
	"--system-prompt": true, "--append-system-prompt": true,
	"--session": true, "--session-dir": true, "--session-id": true, "--no-session": true,
	"--continue": true, "--resume": true, "--fork": true, "--name": true,
	"--skill": true, "--no-skills": true, "--prompt-template": true, "--no-prompt-templates": true,
	"--theme": true, "--use-theme": true, "--no-themes": true, "--tui-mode": true,
	"--no-context-files": true, "--mode": true,
	"--print": true, "--offline": true, "--verbose": true, "--export": true,
	"--list-models": true, "--help": true, "--version": true,
}

// validPiFlag is the shape of an option element in args: --name or
// --name=value. Single-dash forms and the bare "-"/"--" are refused.
var validPiFlag = regexp.MustCompile(`^--[A-Za-z0-9][A-Za-z0-9._-]*(=.*)?$`)

// PiArgsProblem reports why an entry's `pi: {args}` list is not
// admissible, or "" when it is. It checks the args against the shape pi's
// own parser gives them (cli/args.ts parseArgs at 0.84.4; 0.85.0 changes
// only the help text, adding PI_SERVER_DIR/PI_SERVER_ID):
//
//   - `--flag=value` sets the flag and consumes nothing after it;
//   - a bare `--flag` consumes the next element as its value, but only when
//     that element starts with neither "-" nor "@";
//   - every other element that is not dash-prefixed is pushed onto
//     `messages` — pi *prompt text*, prepended to the runner's own prompt.
//     `@word` is read as a file to attach.
//
// So a bare word is legal exactly once, directly after a `--flag` written
// without "=". Two in a row, or one after `--flag=value`, is prompt
// injection through the harness rather than a flag value.
func PiArgsProblem(args []string) string {
	expectValue := false
	for j, a := range args {
		if a == "" {
			return fmt.Sprintf("args[%d] must be non-empty", j)
		}
		if strings.ContainsAny(a, "\n\r\x00") {
			return fmt.Sprintf("args[%d] must not contain newlines", j)
		}
		if !strings.HasPrefix(a, "-") {
			if strings.HasPrefix(a, "@") {
				return fmt.Sprintf("args[%d] %q must not start with '@' (pi reads @path as a file to attach to the prompt)", j, a)
			}
			if j == 0 {
				return fmt.Sprintf("args[0] %q must be a --flag (pi treats bare words as prompt text)", a)
			}
			if !expectValue {
				return fmt.Sprintf("args[%d] %q is a bare word pi would read as prompt text and prepend to the agent's prompt: at most one value may follow a --flag, and none may follow --flag=value", j, a)
			}
			expectValue = false
			continue
		}
		if !validPiFlag.MatchString(a) {
			return fmt.Sprintf("args[%d] %q must be --flag or --flag=value (pi has no single-dash options, and every element is parsed positionally)", j, a)
		}
		name, value, hasEq := strings.Cut(a, "=")
		if piReservedOptions[name] {
			return fmt.Sprintf("args[%d] %q is one of pi's own options, which the runner owns (an extension may only pass flags it registered itself)", j, name)
		}
		if hasEq {
			// Same rule as the separate-token form, so the two spellings
			// cannot be told apart by what they smuggle.
			if strings.HasPrefix(value, "-") || strings.HasPrefix(value, "@") {
				return fmt.Sprintf("args[%d] %q: the value after \"=\" must not start with '-' or '@'", j, a)
			}
			expectValue = false
			continue
		}
		expectValue = true
	}
	return ""
}

// piPackageResourceDirs are the subdirectory names that make pi treat a
// `-e <dir>` target as a *package* rather than a single extension
// (core/package-manager.ts collectPackageResources, 0.84.4; the compiled
// module is byte-identical at 0.85.0): the loader
// collects extensions, skills, prompts and themes from them and never
// looks for an index entry point. One of these directories — even an empty
// one — therefore silently disables an index.js-based extension, which is
// why they are a rejection and not a warning.
var piPackageResourceDirs = []string{"extensions", "prompts", "skills", "themes"}

// piIndexEntryFiles are the entry-point basenames pi's local extension
// source resolver accepts, in jiti's preference order (index.js wins over
// index.ts when both exist).
var piIndexEntryFiles = []string{"index.js", "index.ts", "index.mjs", "index.cjs"}

// PiLoadProblem reports why pi would load nothing from an
// extension directory given with `-e <dir>`, or "" when pi would load it.
// It mirrors pi's own rule for a local directory source
// (core/package-manager.ts resolveLocalExtensionSource ->
// collectPackageResources, core/pi-manifest.ts readPiManifest, verified at
// 0.84.4 by reading the source and by running each shape below):
//
//  1. If package.json parses and carries a "pi" *object*, readPiManifest
//     returns non-null, collectPackageResources adds the manifest entries
//     and returns true — so the directory itself is never loaded and
//     index.* and "main" are never consulted. The verdict then rests
//     entirely on "pi.extensions": `{"pi":{}}`, `{"pi":{"skills":[...]}}`
//     and a "pi.extensions" whose entries do not resolve all load
//     *nothing*, silently, with pi exiting 0.
//  2. Otherwise, if any of extensions/, prompts/, skills/ or themes/
//     exists, the directory is a package: index.* is ignored and nothing is
//     loaded from a `-e` that named it.
//  3. Otherwise a package.json "main" pointing at an existing file, or one
//     of index.js/index.ts/index.mjs/index.cjs.
//
// Outside the "pi" manifest there is deliberately no discovery branch: a
// bare top-level tools.js or a subdirectory with its own index.js is *not*
// loaded (pi exits 1 with `Failed to load extension ... Cannot find
// module`), so accepting either here would let a harness ship an extension
// that cannot start.
//
// files and dirs are the listings of regular files and of directories, as
// slash-separated paths relative to the directory; read returns a file's
// bytes (only package.json files are read). Used on local directories and
// on fetched trees alike so a harness never ships an extension pi refuses.
func PiLoadProblem(files, dirs map[string]bool, read func(rel string) ([]byte, error)) string {
	manifest, problem := extensionManifest("", files, read)
	if problem != "" {
		return problem
	}
	if manifest.hasPi {
		for _, entry := range manifest.entries {
			loads, problem := extensionManifestEntryLoads(entry, files, dirs, read)
			if problem != "" {
				return problem
			}
			if loads {
				return ""
			}
		}
		if len(manifest.entries) == 0 && manifest.excludes > 0 {
			return `package.json "pi.extensions" holds only "!" exclusion patterns, which remove entries rather than name any, so pi loads nothing — add at least one entry to load`
		}
		return `package.json has a "pi" object, so pi loads only what "pi.extensions" names (index.js and "main" are ignored) and none of its entries resolves to a file or to a directory pi would find an entry point in — name the entry points in "pi.extensions", or remove the "pi" object`
	}
	for _, d := range piPackageResourceDirs {
		// existsSync, not a directory probe: a regular *file* named
		// `skills` switches pi to package layout just the same (verified on
		// 0.84.4 — index.js stopped loading).
		if dirs[d] || files[d] {
			return fmt.Sprintf(`a %q entry makes pi read it as a package (it collects extensions/, prompts/, skills/ and themes/ and ignores index.js) — either remove it or name the entry points in package.json "pi.extensions"`, d)
		}
	}
	if manifest.main != "" && files[manifest.main] {
		return ""
	}
	for _, name := range piIndexEntryFiles {
		if files[name] {
			return ""
		}
	}
	return `no index.js/index.ts/index.mjs/index.cjs, package.json "pi.extensions" entry or "main" file — pi would fail to load it`
}

// piPackageManifest is the part of package.json pi's local source resolver
// reads. hasPi records whether package.json carried a "pi" object at all,
// which is the flag readPiManifest keys on and therefore what decides
// whether the entries or the index/main rules apply.
type piPackageManifest struct {
	hasPi bool
	// entries are the include patterns, joined onto dir. A leading "!" is
	// pi's disable form, which removes an entry rather than naming one, so
	// those are counted in excludes instead.
	entries  []string
	excludes int
	main     string
}

// extensionManifest parses the package.json under dir ("" for the extension
// root) into "pi.extensions" entries and "main", as slash paths relative to
// the extension root. It returns a problem string when an entry escapes the
// extension directory: pi resolves "pi.extensions" and "main" against the
// package root with no containment check and loads `../evil.js` from
// outside the tree the preflight hashes (verified on 0.84.4), so every
// listed entry is checked, not just the first one that exists.
//
// A missing or unparsable package.json, or one whose "pi" is not an object,
// yields hasPi false — the package-layout and index rules then decide,
// which is what readPiManifest's null return makes pi do.
func extensionManifest(dir string, files map[string]bool, read func(rel string) ([]byte, error)) (piPackageManifest, string) {
	rel := extensionJoin(dir, "package.json")
	if !files[rel] || read == nil {
		return piPackageManifest{}, ""
	}
	pkg, err := read(rel)
	if err != nil {
		return piPackageManifest{}, ""
	}
	// readPiManifest strips a UTF-8 byte-order mark before parsing;
	// encoding/json does not, and an editor that wrote one would otherwise
	// hide the "pi" object here and send the verdict down the index.js
	// branch pi never takes.
	pkg = bytes.TrimPrefix(pkg, []byte("\xef\xbb\xbf"))
	var manifest struct {
		Main string          `json:"main"`
		Pi   json.RawMessage `json:"pi"`
	}
	if err := json.Unmarshal(pkg, &manifest); err != nil {
		return piPackageManifest{}, ""
	}
	var out piPackageManifest
	if manifest.Main != "" {
		main, ok := relSlashPath(manifest.Main)
		if !ok {
			return out, extensionEntryEscapesProblem("main", manifest.Main)
		}
		out.main = extensionJoin(dir, main)
	}
	// A "pi" value that is not an object leaves readPiManifest at null. An
	// "extensions" that is not an array of strings is dropped from the
	// manifest but still leaves it non-null — so the directory is a package
	// with no entries, and pi loads nothing.
	pi, isObject := jsonObject(manifest.Pi)
	if !isObject {
		return out, ""
	}
	out.hasPi = true
	var entries []string
	if raw, ok := pi["extensions"]; ok && json.Unmarshal(raw, &entries) == nil {
		out.entries = make([]string, 0, len(entries))
		for _, entry := range entries {
			// "!name" disables an entry other patterns brought in; it can
			// never contribute one, and it is not resolved as a path.
			if strings.HasPrefix(entry, "!") {
				out.excludes++
				continue
			}
			clean, ok := relSlashPath(entry)
			if !ok {
				return out, extensionEntryEscapesProblem("pi.extensions", entry)
			}
			out.entries = append(out.entries, extensionJoin(dir, clean))
		}
	}
	return out, ""
}

func extensionEntryEscapesProblem(field, entry string) string {
	return fmt.Sprintf("package.json %s entry %q escapes the extension directory — pi resolves it against the package root without a containment check, so it would load code the sandbox preflight never hashes", field, entry)
}

// jsonObject decodes raw as a JSON object, the shape readPiManifest
// requires of "pi" before it returns a manifest at all.
func jsonObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, false
	}
	return obj, true
}

// relSlashPath cleans p into a slash path relative to the extension root,
// reporting false when it is absolute or climbs out of the directory.
func relSlashPath(p string) (string, bool) {
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") {
		return "", false
	}
	clean := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(p)), "./")
	if clean == "" || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

func extensionJoin(dir, rel string) string {
	if dir == "" {
		return rel
	}
	return dir + "/" + rel
}

// piGlobChars are the characters that make pi expand a "pi.extensions"
// entry as a glob instead of resolving it as a path: hasGlobPattern in the
// 0.84.4 bundle is `s.includes("*") || s.includes("?")`, so a bracket-only
// entry such as `[ab].js` is a literal file name to pi (it loads nothing
// unless that exact file exists) and must be treated the same here. Real
// globs go through Node's globSync, which does expand braces — so a
// pattern with `*`/`?` and `{`/`}` is accepted unevaluated below rather
// than mismatched by path.Match, which reads braces as literals. "!" is
// handled before this, as an exclusion.
const piGlobChars = "*?"

// extensionGlobMatches reports whether pattern selects at least one of
// names. `**` crosses a separator, which path.Match cannot express, braces
// are expanded by pi's globSync but read literally by path.Match, and a
// pattern path.Match rejects outright is one whose syntax is not mirrored
// here — all are accepted rather than guessed at, because a wrong refusal
// blocks a harness pi would have loaded.
func extensionGlobMatches(pattern string, names map[string]bool) bool {
	if strings.Contains(pattern, "**") || strings.ContainsAny(pattern, "{}") {
		return true
	}
	for name := range names {
		ok, err := path.Match(pattern, name)
		if err != nil {
			return true
		}
		if ok {
			return true
		}
	}
	return false
}

// extensionManifestEntryLoads reports whether one "pi.extensions" entry
// would give pi at least one extension: collectFilesFromPaths sends a file
// straight through and hands a directory to collectAutoExtensionEntries.
// The second return is the containment problem of a manifest one level
// down, which must reach the caller rather than be dropped as "does not
// load": pi resolves a nested "pi.extensions" against its own directory
// with no containment check, so `../../outside.js` there loads a file the
// preflight never hashes (verified on 0.84.4).
func extensionManifestEntryLoads(entry string, files, dirs map[string]bool, read func(rel string) ([]byte, error)) (bool, string) {
	if strings.ContainsAny(entry, piGlobChars) {
		if extensionGlobMatches(entry, files) {
			return true, ""
		}
		for d := range dirs {
			// A pattern path.Match cannot parse was already accepted by
			// extensionGlobMatches above, so the error is not reachable
			// here and a non-match is the only reason to skip.
			if ok, _ := path.Match(entry, d); !ok {
				continue
			}
			if loads, problem := extensionAutoEntries(d, files, dirs, read); problem != "" || loads {
				return loads, problem
			}
		}
		return false, ""
	}
	if files[entry] {
		return true, ""
	}
	if dirs[entry] {
		return extensionAutoEntries(entry, files, dirs, read)
	}
	return false, ""
}

// extensionAutoEntries mirrors collectAutoExtensionEntries for a directory
// named in "pi.extensions": the directory's own entry points if it resolves
// (resolveExtensionEntries — where only index.ts and index.js count, not
// .mjs/.cjs), else any top-level .js/.ts file, else an immediate
// subdirectory that itself resolves. pi's .gitignore handling on that path
// is not mirrored; an ignored file makes this accept a directory pi finds
// empty, which is the harmless direction.
func extensionAutoEntries(dir string, files, dirs map[string]bool, read func(rel string) ([]byte, error)) (bool, string) {
	loads, problem := extensionResolvesEntries(dir, files, read)
	if problem != "" || loads {
		return loads, problem
	}
	for f := range files {
		if path.Dir(f) != dir {
			continue
		}
		name := path.Base(f)
		if strings.HasPrefix(name, ".") {
			continue
		}
		if strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".ts") {
			return true, ""
		}
	}
	for d := range dirs {
		if path.Dir(d) != dir {
			continue
		}
		name := path.Base(d)
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}
		if loads, problem := extensionResolvesEntries(d, files, read); problem != "" || loads {
			return loads, problem
		}
	}
	return false, ""
}

// extensionResolvesEntries mirrors resolveExtensionEntries: a package.json
// "pi.extensions" naming at least one existing entry, else index.ts, else
// index.js. A containment problem in that nested package.json is returned
// rather than swallowed — see extensionManifestEntryLoads.
func extensionResolvesEntries(dir string, files map[string]bool, read func(rel string) ([]byte, error)) (bool, string) {
	manifest, problem := extensionManifest(dir, files, read)
	if problem != "" {
		return false, problem
	}
	if manifest.hasPi {
		for _, entry := range manifest.entries {
			if strings.ContainsAny(entry, piGlobChars) {
				if extensionGlobMatches(entry, files) {
					return true, ""
				}
				continue
			}
			if files[entry] {
				return true, ""
			}
		}
	}
	return files[extensionJoin(dir, "index.ts")] || files[extensionJoin(dir, "index.js")], ""
}

// PiTreeLoadProblem applies PiLoadProblem to a fetched tree map
// (relative path → content). Directories are derived from the file paths:
// a forge tree carries no empty directories (and no symlinks), so the
// parents of the fetched files are the whole directory set.
func PiTreeLoadProblem(tree map[string][]byte) string {
	// Both sides are keyed on slash paths: PiLoadProblem looks
	// entries up as "src/main.js", so a lookup through filepath.FromSlash
	// would miss on a platform whose separator is not "/".
	byslash := make(map[string][]byte, len(tree))
	files := make(map[string]bool, len(tree))
	dirs := map[string]bool{}
	for rel, content := range tree {
		slash := filepath.ToSlash(rel)
		byslash[slash] = content
		files[slash] = true
		for dir := path.Dir(slash); dir != "." && dir != "/"; dir = path.Dir(dir) {
			dirs[dir] = true
		}
	}
	return PiLoadProblem(files, dirs, func(rel string) ([]byte, error) {
		if b, ok := byslash[rel]; ok {
			return b, nil
		}
		return nil, os.ErrNotExist
	})
}

// ExtensionUnsafeNameChars are the characters a file or directory name in
// an extension tree may not contain. GNU sha256sum escapes all three and
// prefixes the line with "\", which the Go side of the tree hash does not
// mirror, and a newline would break the directory listing too — so the
// host and sandbox implementations could not agree on such a name.
const ExtensionUnsafeNameChars = "\n\r\\"

// ExtensionEntryProblem reports why one entry of an extension tree is not
// admissible, or "" when it is. It is the single definition of the rule the
// tree hash (runtime.piExtensionTreeHash and its POSIX-sh twin), the
// injection scan and harness validation all apply: regular files and
// directories only, with reproducible names.
//
// Refusing symlinks is not tidiness. pi follows a symlink when it resolves
// an entry point, and the sandbox-side `find . ! -type f ! -type d` probe
// prints nothing for such a tree, so a symlink left in the verdict would be
// a way to swap an extension's code without moving its hash. Trees fetched
// from a forge cannot carry symlinks anyway, so nothing legitimate is lost.
// The extension root itself may still be a symlink — cache paths are named
// symlinks into the content-addressed store — because callers resolve it
// with filepath.EvalSymlinks before walking.
func ExtensionEntryProblem(rel string, mode fs.FileMode) string {
	if strings.ContainsAny(rel, ExtensionUnsafeNameChars) {
		return fmt.Sprintf("name %q contains a newline, carriage return or backslash, which the sandbox-side find/sha256sum pipeline could not reproduce", rel)
	}
	if mode.IsDir() || mode.IsRegular() {
		return ""
	}
	return fmt.Sprintf("%q is neither a regular file nor a directory (%s): symlinks and special files are refused because the sandbox preflight cannot hash them, and pi would follow a symlink to code outside the extension", rel, mode.Type().String())
}

// piDirLoadProblem applies PiLoadProblem to a local
// directory. Symlinks are resolved first (cache paths are named symlinks
// into the content-addressed store) because WalkDir does not follow a
// symlinked root.
//
// The whole tree is walked, node_modules and dotted directories included,
// so that ExtensionEntryProblem rejects a planted symlink here — at harness
// validation, with the offending path named — rather than at Bootstrap,
// where the same tree fails the hash with nothing to point at. Only the
// listing skips those directories: they cannot hold an entry point pi would
// resolve from `-e <dir>`.
func piDirLoadProblem(dir string) (string, error) {
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	files := map[string]bool{}
	dirs := map[string]bool{}
	skipped := map[string]bool{}
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if problem := ExtensionEntryProblem(rel, d.Type()); problem != "" {
			return errors.New(problem)
		}
		// Inside a skipped directory nothing is listed, but every entry is
		// still checked above.
		listed := !extensionUnderSkipped(rel, skipped)
		if d.IsDir() {
			if d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".") {
				skipped[rel] = true
				return nil
			}
			if listed {
				dirs[rel] = true
			}
			return nil
		}
		if listed {
			files[rel] = true
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return PiLoadProblem(files, dirs, func(rel string) ([]byte, error) {
		return os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	}), nil
}

// extensionUnderSkipped reports whether rel lies inside one of the
// directories the listing ignores.
func extensionUnderSkipped(rel string, skipped map[string]bool) bool {
	for parent := path.Dir(rel); parent != "." && parent != "/"; parent = path.Dir(parent) {
		if skipped[parent] {
			return true
		}
	}
	return false
}
