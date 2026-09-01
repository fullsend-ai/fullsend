package harness

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/pluginformat"
)

// PluginSpec is one `plugins:` entry: a directory a runtime loads (ADR
// 0094). Which runtime loads it follows from the directory's format, not
// from the key: a plugin.json bundle is Claude Code's, a directory pi's
// `-e <dir>` loader resolves an entry point in is pi's, and each runtime
// names and skips the entries of the other format. Two YAML forms:
//
//	# String form — just the directory
//	- plugins/gopls-lsp
//
//	# Object form — when the entry needs environment or runtime options
//	- path: extensions/pi-fff
//	  env:
//	    FFF_MULTIGREP: "1"
//	  pi:
//	    args: ["--fff-mode", "override"]
//
// Path is a path inside the harness repository or a pinned forge tree URL,
// the same sourcing rule as skills:. npm:/git:/ssh: forms are rejected —
// pi would install such a source from the network at startup, which the
// sandbox cannot do.
//
// Env and the pi: block only apply to an entry a runtime loads as code
// (today: pi), and validation refuses them on a Claude plugin rather than
// dropping them silently. Env is exported right before pi starts and is
// inherited by pi and by every hook script it spawns, so a broad deny-list
// — not the export order — is what keeps the runtime's own names out of a
// plugin's reach (see reservedPluginEnvKey).
type PluginSpec struct {
	Path string
	Env  map[string]string
	Pi   *PiPluginOptions
}

// PiPluginOptions are the knobs that apply when pi loads the entry. Args
// are appended to pi's command line right after the entry's `-e <path>`;
// they are the flags the extension registered with pi.registerFlag, and
// pi's own options are rejected (pluginformat.PiArgsProblem).
type PiPluginOptions struct {
	Args []string
}

// Name is the plugin's sandbox name: the directory basename, which is also
// what the runtime uploads it as.
func (p PluginSpec) Name() string {
	return filepath.Base(p.Path)
}

// SameOptions reports whether two entries carry the same env and pi
// options, treating an absent map, block or args list as equal to an empty
// one — `env: {}` and no `env:` mean the same thing to every runtime.
func (p PluginSpec) SameOptions(o PluginSpec) bool {
	if len(p.Env) != len(o.Env) {
		return false
	}
	for k, v := range p.Env {
		if ov, ok := o.Env[k]; !ok || ov != v {
			return false
		}
	}
	a, b := p.PiArgs(), o.PiArgs()
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// PiArgs is the entry's pi args, or nil when it carries no pi: block.
func (p PluginSpec) PiArgs() []string {
	if p.Pi == nil {
		return nil
	}
	return p.Pi.Args
}

// UnmarshalYAML implements yaml.Unmarshaler for the string and object forms.
func (p *PluginSpec) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		p.Path = value.Value
		return nil
	}
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("plugin entry must be a path string or a {path, env, pi} map")
	}
	var pathNode, envNode, piNode *yaml.Node
	for i := 0; i+1 < len(value.Content); i += 2 {
		keyNode, valNode := value.Content[i], value.Content[i+1]
		switch keyNode.Value {
		case "path":
			pathNode = valNode
		case "env":
			envNode = valNode
		case "pi":
			piNode = valNode
		default:
			// A typo'd key (environment:, or a bare args: from the pi-only
			// spelling this key replaced) must not be silently ignored.
			return fmt.Errorf("plugin entry has unknown key %q (allowed: path, env, pi)", keyNode.Value)
		}
	}
	if pathNode == nil || pathNode.Kind != yaml.ScalarNode || pathNode.Value == "" {
		return fmt.Errorf("plugin entry: path is required and must be a string")
	}
	p.Path = pathNode.Value
	if envNode != nil {
		if envNode.Kind != yaml.MappingNode {
			return fmt.Errorf("plugin entry %q: env must be a map of strings", p.Path)
		}
		if err := envNode.Decode(&p.Env); err != nil {
			return fmt.Errorf("plugin entry %q: env must be a map of strings: %w", p.Path, err)
		}
	}
	if piNode != nil {
		if piNode.Kind != yaml.MappingNode {
			return fmt.Errorf("plugin entry %q: pi must be a map of pi options (args)", p.Path)
		}
		opts := &PiPluginOptions{}
		for i := 0; i+1 < len(piNode.Content); i += 2 {
			keyNode, valNode := piNode.Content[i], piNode.Content[i+1]
			if keyNode.Value != "args" {
				return fmt.Errorf("plugin entry %q: pi has unknown key %q (allowed: args)", p.Path, keyNode.Value)
			}
			if valNode.Kind != yaml.SequenceNode {
				return fmt.Errorf("plugin entry %q: pi.args must be a list of strings", p.Path)
			}
			if err := valNode.Decode(&opts.Args); err != nil {
				return fmt.Errorf("plugin entry %q: pi.args must be a list of strings: %w", p.Path, err)
			}
		}
		p.Pi = opts
	}
	return nil
}

// MarshalYAML round-trips: the string form when the entry is only a path,
// the object form otherwise.
func (p PluginSpec) MarshalYAML() (interface{}, error) {
	if len(p.Env) == 0 && p.Pi == nil {
		return p.Path, nil
	}
	out := map[string]interface{}{"path": p.Path}
	if len(p.Env) > 0 {
		out["env"] = p.Env
	}
	if p.Pi != nil {
		pi := map[string]interface{}{}
		if len(p.Pi.Args) > 0 {
			pi["args"] = p.Pi.Args
		}
		out["pi"] = pi
	}
	return out, nil
}

var validPluginEnvKey = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// The environment names a plugin's env: may not set. The runtime
// exports extension env last, right before pi starts — after its own
// PI_*/FULLSEND_* pins (PiRuntime.EnvExports) and after the per-provider
// credential hygiene (the ANTHROPIC_*, XAI_*, OPENAI_* unsets and the
// GOOGLE_* project pins) — and pi hands its whole environment on to every
// hook script it spawns. Export order therefore protects nothing: this
// deny-list is what stops an extension from re-introducing the variables
// those steps remove, from redirecting the interpreter that runs pi or the
// hook scripts, or from planting a credential the sandbox would then use.
//
// It is deliberately broad. A plugin reads its own settings from names
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
	// This list and the prefixes below are the plugin-env twin of
	// reservedCredentialKeys in internal/sandbox/sandbox.go, which refuses
	// the same names as provider *credential* keys. The two cannot share
	// one variable — internal/sandbox imports internal/harness, so the
	// dependency only runs one way — so they are kept in sync by hand and
	// by TestReservedCredentialKeys_ReservedForPluginEnv in
	// internal/sandbox. Add a name to one, add it to the other.
	reservedPluginEnvNames = map[string]bool{
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
	reservedPluginEnvPrefixes = []string{
		"LD_", "DYLD_", "PYTHON", "NODE_", "SSL_", "JITI_",
		"PI_", "FULLSEND_", "TIRITH_", "GOOGLE_", "GCLOUD_", "CLOUDSDK_",
		"GIT_",
		"ANTHROPIC_", "XAI_", "OPENAI_", "AZURE_", "AWS_",
	}
	// Credential- and proxy-shaped names, whatever the vendor prefix.
	reservedPluginEnvSuffixes = []string{"_PROXY", "_API_KEY", "_TOKEN"}
)

// reservedPluginEnvKey returns the rule a reserved key matched, for the
// validation message, and whether it matched at all. Names are compared
// case-insensitively so the lowercase proxy spellings (http_proxy) are
// covered even though validPluginEnvKey only admits uppercase today.
func reservedPluginEnvKey(key string) (string, bool) {
	upper := strings.ToUpper(key)
	if reservedPluginEnvNames[upper] {
		return "the shell, interpreter and trust-store environment (PATH, HOME, TMPDIR, ENV, BASH_ENV, SHELL, IFS, CDPATH, PROMPT_COMMAND, HOSTALIASES, OPENSSL_CONF, SSLKEYLOGFILE, REQUESTS_CA_BUNDLE, CURL_CA_BUNDLE, JAVA_TOOL_OPTIONS, RUBYOPT, PERL5OPT, GOPROXY, GOFLAGS, CLOUD_ML_REGION)", true
	}
	for _, prefix := range reservedPluginEnvPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return "the " + prefix + "* family, which belongs to the runtime, a provider or a language loader", true
		}
	}
	for _, suffix := range reservedPluginEnvSuffixes {
		if strings.HasSuffix(upper, suffix) {
			return "the *" + suffix + " family (credential- and proxy-shaped names)", true
		}
	}
	if strings.Contains(upper, "_SECRET") {
		return "the *_SECRET* family (credential-shaped names)", true
	}
	return "", false
}

// validatePlugins is the Validate() check for plugins: entries. It holds
// the checks that need no disk access — path shape, duplicates, and the
// syntax of env and pi.args. The checks that depend on which format the
// directory is in (env and pi: are only meaningful for a runtime that
// loads the entry as code, and pi owns a few sandbox names) live in
// ValidateFilesExist, which runs after URL entries have been fetched to
// local paths, so a URL entry is checked exactly like a local one.
//
// An absolute path is treated as already resolved (by base composition or
// ResolveRelativeTo, the same convention as skill overrides and providers)
// and only basename-checked.
//
// Duplicates are rejected here rather than only in the runtime, so a base
// harness and its child that name the same plugin fail at load with the
// offending index, not at bootstrap: the sandbox upload replaces its
// destination wholesale, so two entries sharing a basename would silently
// drop one.
func (h *Harness) validatePlugins() error {
	seenPaths := make(map[string]int, len(h.Plugins))
	seenNames := make(map[string]int, len(h.Plugins))
	for i, e := range h.Plugins {
		field := fmt.Sprintf("plugins[%d]", i)
		p := e.Path
		if p == "" {
			return fmt.Errorf("%s: path is required", field)
		}
		if strings.ContainsRune(p, 0) {
			return fmt.Errorf("%s: path %q must not contain null bytes", field, p)
		}
		lower := strings.ToLower(p)
		if strings.HasPrefix(lower, "npm:") || strings.HasPrefix(lower, "git:") || strings.HasPrefix(lower, "ssh:") {
			return fmt.Errorf("%s: %q must be a path inside the harness repository or a pinned forge URL, not an npm:/git:/ssh: source (pi would fetch it from the network at startup)", field, p)
		}
		if prev, ok := seenPaths[p]; ok {
			return fmt.Errorf("%s: %q is already listed as plugins[%d]", field, p, prev)
		}
		seenPaths[p] = i
		if !IsURL(p) {
			// URL entries are shape-checked by ValidateResourceTypes, which
			// reads the basename out of the forge path rather than the URL
			// string.
			for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
				if seg == ".." {
					return fmt.Errorf("%s: path %q must not contain path traversal segments", field, p)
				}
			}
			if !ValidPluginBasename(e.Name()) {
				return fmt.Errorf("%s name %q contains invalid characters (allowed: a-z, A-Z, 0-9, _, -)", field, e.Name())
			}
			if prev, ok := seenNames[e.Name()]; ok {
				return fmt.Errorf("%s: %q and plugins[%d] %q both load as plugin %q; the second would replace the first in the sandbox", field, p, prev, h.Plugins[prev].Path, e.Name())
			}
			seenNames[e.Name()] = i
		}
		if problem := pluginformat.PiArgsProblem(e.PiArgs()); problem != "" {
			return fmt.Errorf("%s: pi.%s", field, problem)
		}
		for k, v := range e.Env {
			if !validPluginEnvKey.MatchString(k) {
				return fmt.Errorf("%s: env key %q must match ^[A-Z_][A-Z0-9_]*$", field, k)
			}
			if rule, reserved := reservedPluginEnvKey(k); reserved {
				return fmt.Errorf("%s: env key %q is reserved: it matches %s. Plugin env is exported last and is inherited by the agent runtime and by every hook script it spawns, so these names are the runner's to set", field, k, rule)
			}
			if strings.ContainsAny(v, "\n\r\x00") {
				return fmt.Errorf("%s: env[%q] must not contain newlines", field, k)
			}
		}
	}
	return nil
}

// validatePluginDir is the ValidateFilesExist check for one resolved
// plugin directory: it exists, it is a directory, exactly one runtime
// format claims it, and the options the entry carries apply to that
// format.
func (h *Harness) validatePluginDir(field string, e PluginSpec) error {
	info, err := os.Stat(e.Path)
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: %q must be a directory (a Claude plugin is a plugin.json bundle; pi loads index.js/index.ts/index.mjs/index.cjs, or the package.json \"pi.extensions\"/\"main\" entries, from it)", field, e.Path)
	}
	// A directory neither runtime would load is a silent no-op at run time:
	// Claude Code ignores a bundle without plugin.json, and pi exits 1 with
	// `Failed to load extension "<path>"` or loads nothing at all from a
	// directory that turned into package layout. The harness author learns
	// here instead.
	kind, problem, err := pluginformat.Detect(e.Path)
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	if kind == "" {
		return pluginNotLoadableError(field, e.Path, problem)
	}
	if kind != pluginformat.KindPi {
		if len(e.Env) > 0 || e.Pi != nil {
			return fmt.Errorf("%s: env/pi options apply to plugins the runtime loads as code; %q is a Claude plugin", field, e.Path)
		}
		return nil
	}
	for _, reserved := range pluginformat.PiReservedExtensionNames {
		if e.Name() == reserved {
			return fmt.Errorf("%s: %q is a name the runner owns (the pi hook adapter and the vendored provider extensions); rename the directory", field, reserved)
		}
	}
	return nil
}

// pluginNotLoadableError is the ValidateFilesExist / fetch error for a
// directory no runtime would load.
func pluginNotLoadableError(field, path, problem string) error {
	return fmt.Errorf("%s %q: %s", field, path, problem)
}
