package harness

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/pluginformat"
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
		for _, reserved := range pluginformat.PiReservedExtensionNames {
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
		if problem := pluginformat.PiArgsProblem(e.Args); problem != "" {
			return fmt.Errorf("%s: %s", field, problem)
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

// extensionNotLoadableError is the ValidateFilesExist / fetch error for a
// directory pi would load nothing from.
func extensionNotLoadableError(field, path, problem string) error {
	return fmt.Errorf("%s %q: %s", field, path, problem)
}
