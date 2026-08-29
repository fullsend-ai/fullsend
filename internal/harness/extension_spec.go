package harness

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

	"gopkg.in/yaml.v3"
)

// ExtensionSpec is one `extensions:` entry: a pi extension directory that
// lives in the harness repository (ADR 0094). It supports two YAML forms:
//
//	# String form — just the directory
//	- extensions/go-diagnostics
//
//	# Object form — when the extension needs CLI flags or environment
//	- path: extensions/pi-fff
//	  args: ["--fff-mode", "override"]
//	  env:
//	    FFF_MULTIGREP: "1"
//
// Path is resolved like plugins: relative to the harness directory, or
// fetched from a URL-sourced base. URL, npm:/git:/ssh: and traversing
// forms are rejected at Validate — pi would install npm:/git: sources from
// the network at startup, which the sandbox cannot do. Args are appended
// to pi's command line right after the extension's `-e <path>`; they are
// the flags the extension registers with pi.registerFlag; pi's own options
// are rejected. Env is exported right before pi starts and is inherited by
// pi and by every hook script it spawns, so a broad deny-list — not the
// export order — is what keeps the runtime's own names out of an
// extension's reach (see reservedExtensionEnvKey).
type ExtensionSpec struct {
	Path string
	Args []string
	Env  map[string]string
}

// Name is the extension's sandbox name: the directory basename, which is
// also what the runtime uploads it as.
func (e ExtensionSpec) Name() string {
	return filepath.Base(e.Path)
}

// UnmarshalYAML implements yaml.Unmarshaler for the string and object forms.
func (e *ExtensionSpec) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		e.Path = value.Value
		return nil
	}
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("extension entry must be a path string or a {path, args, env} map")
	}
	var pathNode, argsNode, envNode *yaml.Node
	for i := 0; i+1 < len(value.Content); i += 2 {
		keyNode, valNode := value.Content[i], value.Content[i+1]
		switch keyNode.Value {
		case "path":
			pathNode = valNode
		case "args":
			argsNode = valNode
		case "env":
			envNode = valNode
		default:
			// A typo'd key (arg:, environment:) must not be silently ignored.
			return fmt.Errorf("extension entry has unknown key %q (allowed: path, args, env)", keyNode.Value)
		}
	}
	if pathNode == nil || pathNode.Kind != yaml.ScalarNode || pathNode.Value == "" {
		return fmt.Errorf("extension entry: path is required and must be a string")
	}
	e.Path = pathNode.Value
	if argsNode != nil {
		if argsNode.Kind != yaml.SequenceNode {
			return fmt.Errorf("extension entry %q: args must be a list of strings", e.Path)
		}
		if err := argsNode.Decode(&e.Args); err != nil {
			return fmt.Errorf("extension entry %q: args must be a list of strings: %w", e.Path, err)
		}
	}
	if envNode != nil {
		if envNode.Kind != yaml.MappingNode {
			return fmt.Errorf("extension entry %q: env must be a map of strings", e.Path)
		}
		if err := envNode.Decode(&e.Env); err != nil {
			return fmt.Errorf("extension entry %q: env must be a map of strings: %w", e.Path, err)
		}
	}
	return nil
}

// MarshalYAML round-trips: the string form when there are no args or env,
// the object form otherwise.
func (e ExtensionSpec) MarshalYAML() (interface{}, error) {
	if len(e.Args) == 0 && len(e.Env) == 0 {
		return e.Path, nil
	}
	out := map[string]interface{}{"path": e.Path}
	if len(e.Args) > 0 {
		out["args"] = e.Args
	}
	if len(e.Env) > 0 {
		out["env"] = e.Env
	}
	return out, nil
}

// ExtensionPaths extracts the directory paths from a slice of ExtensionSpec
// values, for call sites that only need the directories.
func ExtensionPaths(entries []ExtensionSpec) []string {
	if entries == nil {
		return nil
	}
	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.Path
	}
	return paths
}

// PiReservedExtensionNames are the sandbox names the pi runtime owns: the
// hook adapter's file basename and the vendored provider extensions Run
// loads by path. A declared extension uploads under its directory
// basename, so one of these names would shadow — or be mistaken for —
// runner-owned code. runtime.piResolveRunExtensions refuses them again at
// bootstrap; the check here is so a harness author learns at load which
// entry is the problem. The list lives in this package because
// internal/runtime imports it and not the other way round.
var PiReservedExtensionNames = []string{"fullsend-hooks", "anthropic-vertex", "xai-vertex"}

var validExtensionEnvKey = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// The environment names an extension's env: may not set. The runtime
// exports extension env last, right before pi starts — after its own
// PI_*/FULLSEND_* pins (PiRuntime.EnvExports) and after the per-provider
// credential hygiene (the ANTHROPIC_*, XAI_*, OPENAI_* unsets and the
// GOOGLE_* project pins) — and pi hands its whole environment on to every
// hook script it spawns. Export order therefore protects nothing: this
// deny-list is what stops an extension from re-introducing the variables
// those steps remove, from redirecting the interpreter that runs pi or the
// hook scripts, or from planting a credential the sandbox would then use.
//
// It is deliberately broad. An extension reads its own settings from names
// outside these families (FFF_MULTIGREP, GO_DIAG_LEVEL); nothing legitimate
// needs to set PATH or a *_TOKEN.
var (
	// Exact names: the shell/interpreter environment, the trust stores the
	// hook scripts' own tooling reads, and the region pin. IFS changes how
	// every sh the hook scripts spawn splits words; CDPATH changes what
	// `cd dir` resolves to and PROMPT_COMMAND runs a command per prompt;
	// HOSTALIASES redirects name resolution; the CA-bundle and OPENSSL_CONF
	// names move the trust anchor curl/python/openssl validate the egress
	// proxy against, and SSLKEYLOGFILE (no underscore, so the SSL_ prefix
	// misses it) writes every TLS session key to a file the agent names;
	// JAVA_TOOL_OPTIONS/RUBYOPT/PERL5OPT inject code at interpreter start
	// the way NODE_OPTIONS does; GOPROXY/GOFLAGS steer a Go toolchain the
	// agent may invoke.
	//
	// This list and the prefixes below are the extension-env twin of
	// reservedCredentialKeys in internal/sandbox/sandbox.go, which refuses
	// the same names as provider *credential* keys. The two cannot share
	// one variable — internal/sandbox imports internal/harness, so the
	// dependency only runs one way — so they are kept in sync by hand and
	// by TestReservedCredentialKeys_ReservedForExtensionEnv in
	// internal/sandbox. Add a name to one, add it to the other.
	reservedExtensionEnvNames = map[string]bool{
		"PATH": true, "HOME": true, "TMPDIR": true, "ENV": true,
		"BASH_ENV": true, "SHELL": true, "CLOUD_ML_REGION": true,
		"IFS": true, "CDPATH": true, "PROMPT_COMMAND": true,
		"HOSTALIASES": true, "OPENSSL_CONF": true, "SSLKEYLOGFILE": true,
		"REQUESTS_CA_BUNDLE": true, "CURL_CA_BUNDLE": true,
		"JAVA_TOOL_OPTIONS": true, "RUBYOPT": true, "PERL5OPT": true,
		"GOPROXY": true, "GOFLAGS": true,
	}
	// Families that steer a loader (LD_*, DYLD_*, PYTHON*, NODE_*, SSL_*,
	// JITI_*) or belong to the runner, its providers and the tools the hook
	// scripts shell out to. JITI_* is pi's own module loader: JITI_FS_CACHE
	// re-enables the transpile cache the runtime disables and JITI_ALIAS
	// swaps the file behind a loaded module path, both of them code paths
	// around the extension tree hash (see PiRuntime.EnvExports and
	// runtime.piLoaderEnvNames). GIT_ is reserved whole rather than by its
	// half-dozen dangerous members (GIT_SSH_COMMAND, GIT_PROXY_COMMAND,
	// GIT_ASKPASS, GIT_EXEC_PATH, GIT_TEMPLATE_DIR, GIT_CONFIG*,
	// GIT_SSL_*): git runs the first three as commands, and the family
	// grows with every git release.
	reservedExtensionEnvPrefixes = []string{
		"LD_", "DYLD_", "PYTHON", "NODE_", "SSL_", "JITI_",
		"PI_", "FULLSEND_", "TIRITH_", "GOOGLE_", "GCLOUD_", "CLOUDSDK_",
		"GIT_",
		"ANTHROPIC_", "XAI_", "OPENAI_", "AZURE_", "AWS_",
	}
	// Credential- and proxy-shaped names, whatever the vendor prefix.
	reservedExtensionEnvSuffixes = []string{"_PROXY", "_API_KEY", "_TOKEN"}
)

// reservedExtensionEnvKey returns the rule a reserved key matched, for the
// validation message, and whether it matched at all. Names are compared
// case-insensitively so the lowercase proxy spellings (http_proxy) are
// covered even though validExtensionEnvKey only admits uppercase today.
func reservedExtensionEnvKey(key string) (string, bool) {
	upper := strings.ToUpper(key)
	if reservedExtensionEnvNames[upper] {
		return "the shell, interpreter and trust-store environment (PATH, HOME, TMPDIR, ENV, BASH_ENV, SHELL, IFS, CDPATH, PROMPT_COMMAND, HOSTALIASES, OPENSSL_CONF, SSLKEYLOGFILE, REQUESTS_CA_BUNDLE, CURL_CA_BUNDLE, JAVA_TOOL_OPTIONS, RUBYOPT, PERL5OPT, GOPROXY, GOFLAGS, CLOUD_ML_REGION)", true
	}
	for _, prefix := range reservedExtensionEnvPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return "the " + prefix + "* family, which belongs to the runtime, a provider or a language loader", true
		}
	}
	for _, suffix := range reservedExtensionEnvSuffixes {
		if strings.HasSuffix(upper, suffix) {
			return "the *" + suffix + " family (credential- and proxy-shaped names)", true
		}
	}
	if strings.Contains(upper, "_SECRET") {
		return "the *_SECRET* family (credential-shaped names)", true
	}
	return "", false
}

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

// validExtensionFlag is the shape of an option element in args: --name or
// --name=value. Single-dash forms and the bare "-"/"--" are refused.
var validExtensionFlag = regexp.MustCompile(`^--[A-Za-z0-9][A-Za-z0-9._-]*(=.*)?$`)

// validateExtensionArgs checks one entry's args against the shape pi's own
// parser gives them (cli/args.ts parseArgs at 0.84.4):
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
func validateExtensionArgs(field string, args []string) error {
	expectValue := false
	for j, a := range args {
		if a == "" {
			return fmt.Errorf("%s: args[%d] must be non-empty", field, j)
		}
		if strings.ContainsAny(a, "\n\r\x00") {
			return fmt.Errorf("%s: args[%d] must not contain newlines", field, j)
		}
		if !strings.HasPrefix(a, "-") {
			if strings.HasPrefix(a, "@") {
				return fmt.Errorf("%s: args[%d] %q must not start with '@' (pi reads @path as a file to attach to the prompt)", field, j, a)
			}
			if j == 0 {
				return fmt.Errorf("%s: args[0] %q must be a --flag (pi treats bare words as prompt text)", field, a)
			}
			if !expectValue {
				return fmt.Errorf("%s: args[%d] %q is a bare word pi would read as prompt text and prepend to the agent's prompt: at most one value may follow a --flag, and none may follow --flag=value", field, j, a)
			}
			expectValue = false
			continue
		}
		if !validExtensionFlag.MatchString(a) {
			return fmt.Errorf("%s: args[%d] %q must be --flag or --flag=value (pi has no single-dash options, and every element is parsed positionally)", field, j, a)
		}
		name, value, hasEq := strings.Cut(a, "=")
		if piReservedOptions[name] {
			return fmt.Errorf("%s: args[%d] %q is one of pi's own options, which the runner owns (an extension may only pass flags it registered itself)", field, j, name)
		}
		if hasEq {
			// Same rule as the separate-token form, so the two spellings
			// cannot be told apart by what they smuggle.
			if strings.HasPrefix(value, "-") || strings.HasPrefix(value, "@") {
				return fmt.Errorf("%s: args[%d] %q: the value after \"=\" must not start with '-' or '@'", field, j, a)
			}
			expectValue = false
			continue
		}
		expectValue = true
	}
	return nil
}

// validateExtensions is the Validate() check for extensions: entries. An
// absolute path is treated as already resolved (by base composition or
// ResolveRelativeTo, the same convention as skill overrides and providers)
// and only basename-checked; URL-sourced bases reject absolute entries in
// resolveBaseExtensions before they get here.
//
// Duplicates are rejected here rather than only in the runtime, so a base
// harness and its child that name the same extension fail at load with the
// offending index, not at bootstrap: the sandbox upload replaces its
// destination wholesale, so two entries sharing a basename would silently
// drop one.
func (h *Harness) validateExtensions() error {
	seenPaths := make(map[string]int, len(h.Extensions))
	seenNames := make(map[string]int, len(h.Extensions))
	for i, e := range h.Extensions {
		field := fmt.Sprintf("extensions[%d]", i)
		p := e.Path
		if p == "" {
			return fmt.Errorf("%s: path is required", field)
		}
		if strings.ContainsRune(p, 0) {
			return fmt.Errorf("%s: path %q must not contain null bytes", field, p)
		}
		if IsURL(p) {
			return fmt.Errorf("%s: %q must be a path inside the harness repository, not a URL", field, p)
		}
		lower := strings.ToLower(p)
		if strings.HasPrefix(lower, "npm:") || strings.HasPrefix(lower, "git:") || strings.HasPrefix(lower, "ssh:") {
			return fmt.Errorf("%s: %q must be a path inside the harness repository, not an npm:/git:/ssh: source (pi would fetch it from the network at startup)", field, p)
		}
		for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
			if seg == ".." {
				return fmt.Errorf("%s: path %q must not contain path traversal segments", field, p)
			}
		}
		if !ValidPluginBasename(e.Name()) {
			return fmt.Errorf("%s: name %q contains invalid characters (allowed: a-z, A-Z, 0-9, _, -)", field, e.Name())
		}
		for _, reserved := range PiReservedExtensionNames {
			if e.Name() == reserved {
				return fmt.Errorf("%s: %q is a name the runner owns (the pi hook adapter and the vendored provider extensions); rename the directory", field, reserved)
			}
		}
		if prev, ok := seenPaths[p]; ok {
			return fmt.Errorf("%s: %q is already listed as extensions[%d]", field, p, prev)
		}
		if prev, ok := seenNames[e.Name()]; ok {
			return fmt.Errorf("%s: %q and extensions[%d] %q both load as extension %q; the second would replace the first in the sandbox", field, p, prev, h.Extensions[prev].Path, e.Name())
		}
		seenPaths[p] = i
		seenNames[e.Name()] = i
		if err := validateExtensionArgs(field, e.Args); err != nil {
			return err
		}
		for k, v := range e.Env {
			if !validExtensionEnvKey.MatchString(k) {
				return fmt.Errorf("%s: env key %q must match ^[A-Z_][A-Z0-9_]*$", field, k)
			}
			if rule, reserved := reservedExtensionEnvKey(k); reserved {
				return fmt.Errorf("%s: env key %q is reserved: it matches %s. Extension env is exported last and is inherited by pi and by every hook script pi spawns, so these names are the runner's to set", field, k, rule)
			}
			if strings.ContainsAny(v, "\n\r\x00") {
				return fmt.Errorf("%s: env[%q] must not contain newlines", field, k)
			}
		}
	}
	return nil
}

// piPackageResourceDirs are the subdirectory names that make pi treat a
// `-e <dir>` target as a *package* rather than a single extension
// (core/package-manager.ts collectPackageResources, 0.84.4): the loader
// collects extensions, skills, prompts and themes from them and never
// looks for an index entry point. One of these directories — even an empty
// one — therefore silently disables an index.js-based extension, which is
// why they are a rejection and not a warning.
var piPackageResourceDirs = []string{"extensions", "prompts", "skills", "themes"}

// piIndexEntryFiles are the entry-point basenames pi's local extension
// source resolver accepts, in jiti's preference order (index.js wins over
// index.ts when both exist).
var piIndexEntryFiles = []string{"index.js", "index.ts", "index.mjs", "index.cjs"}

// ExtensionDirLoadProblem reports why pi would load nothing from an
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
func ExtensionDirLoadProblem(files, dirs map[string]bool, read func(rel string) ([]byte, error)) string {
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

// TreeLoadProblem applies ExtensionDirLoadProblem to a fetched tree map
// (relative path → content). Directories are derived from the file paths:
// a forge tree carries no empty directories (and no symlinks), so the
// parents of the fetched files are the whole directory set.
func TreeLoadProblem(tree map[string][]byte) string {
	// Both sides are keyed on slash paths: ExtensionDirLoadProblem looks
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
	return ExtensionDirLoadProblem(files, dirs, func(rel string) ([]byte, error) {
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

// extensionDirLoadProblem applies ExtensionDirLoadProblem to a local
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
func extensionDirLoadProblem(dir string) (string, error) {
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
	return ExtensionDirLoadProblem(files, dirs, func(rel string) ([]byte, error) {
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

// extensionNotLoadableError is the ValidateFilesExist / fetch error for a
// directory pi would load nothing from.
func extensionNotLoadableError(field, path, problem string) error {
	return fmt.Errorf("%s %q: %s", field, path, problem)
}
