package harness

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// WorkflowSandboxDir is the sandbox directory name the workflow:
// definition is uploaded under (ADR 0130). A harness has one workflow:, so
// the name is fixed; plugins: entries may not use it, and a Claude plugin
// definition without .claude-plugin/plugin.json takes it as its namespace.
const WorkflowSandboxDir = "workflow-definition"

// WorkflowSpec is the harness workflow: field (ADR 0130). Source pins a
// workflow-definition repository: a GitHub tree URL at a full commit sha
// with a #sha256= tree hash, or a path in the repository that holds the
// harness ("." is its root). For a Claude plugin definition, Name is the
// workflow to start (workflows/<name>.js) and Args its optional
// arguments, whose ${VAR} references the runner expands from its
// environment; a pi extension definition takes neither.
type WorkflowSpec struct {
	Source string `yaml:"source"`
	Name   string `yaml:"name,omitempty"`
	Args   string `yaml:"args,omitempty"`

	// baseDirURL is set by ResolveBaseWorkflowSource when Source was a
	// relative path in a harness composed through a URL base: Source is
	// then a tree URL at the base's commit without a #sha256= fragment,
	// and baseDirURL is the raw-content directory URL the allowlist is
	// checked against, as for a base plugin. It cannot be set from YAML.
	baseDirURL string
	// declaringDir is set by base composition when Source is a relative
	// path in a local base harness: the directory of that harness file,
	// whose git checkout the path resolves in (ADR 0130 rule 1), not the
	// child's. It cannot be set from YAML.
	declaringDir string
}

// InheritedFromBase returns the raw-content directory URL of a source
// that ResolveBaseWorkflowSource rewrote from a relative path in a URL
// base, or "" when Source is as the harness wrote it.
func (w *WorkflowSpec) InheritedFromBase() string { return w.baseDirURL }

// DeclaredIn returns the directory of the local base harness that
// declared a relative Source, or "" when the harness being run declared
// it (or Source is a URL).
func (w *WorkflowSpec) DeclaredIn() string { return w.declaringDir }

// markLocalBaseWorkflow records dir, the directory of the local base
// harness file that holds w, as where a relative w.Source resolves. A
// mark already set by a deeper base is kept: the harness that wrote the
// path is the one whose checkout it names.
func markLocalBaseWorkflow(w *WorkflowSpec, dir string) {
	if w == nil || w.declaringDir != "" || w.Source == "" || IsURL(w.Source) {
		return
	}
	w.declaringDir = dir
}

// IsRemote reports whether Source is a URL rather than a path in the
// repository that holds the harness.
func (w *WorkflowSpec) IsRemote() bool { return IsURL(w.Source) }

// validWorkflowName matches workflow.name, the <name> of
// workflows/<name>.js.
var validWorkflowName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// validCommitSHA matches a full 40-hex-character git commit sha.
var validCommitSHA = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// ValidWorkflowName reports whether name is a valid workflow.name, and
// also whether it is a usable Claude Code plugin namespace for one.
func ValidWorkflowName(name string) bool { return validWorkflowName.MatchString(name) }

// validateWorkflow is the Validate() check for the workflow: field.
func (h *Harness) validateWorkflow() error {
	return ValidateWorkflowSpec(h.Workflow)
}

// ValidateWorkflowSpec checks the shape of a workflow: field; nil passes.
// Which kind of definition the source holds, and so whether name is
// required or refused, is known once it is fetched.
func ValidateWorkflowSpec(w *WorkflowSpec) error {
	if w == nil {
		return nil
	}
	if err := validateWorkflowSource(w); err != nil {
		return err
	}
	if w.Name != "" && !validWorkflowName.MatchString(w.Name) {
		return fmt.Errorf("workflow.name %q may contain only letters, digits, _ and -, the <name> of workflows/<name>.js", w.Name)
	}
	if strings.ContainsAny(w.Args, "\x00\r\n") {
		return fmt.Errorf("workflow.args must be a single line: remove NUL, carriage return and newline characters")
	}
	if w.Args != "" && w.Name == "" {
		return fmt.Errorf("workflow.args is set without workflow.name: args go to the Claude workflow that name starts; set name, or remove args")
	}
	return CheckWorkflowArgsVariables(w.Args, nil)
}

func validateWorkflowSource(w *WorkflowSpec) error {
	src := w.Source
	if src == "" {
		return fmt.Errorf("workflow.source is required: give a GitHub tree URL pinned to a commit sha with #sha256=<tree hash>, or a path in the repository that holds this harness (\".\" is its root)")
	}
	if strings.ContainsAny(src, "\x00\r\n") {
		return fmt.Errorf("workflow.source must not contain NUL, carriage return or newline characters")
	}
	if IsURL(src) {
		return validateRemoteWorkflowSource(src, w.baseDirURL != "")
	}
	lower := strings.ToLower(src)
	if strings.HasPrefix(lower, "http://") {
		return fmt.Errorf("workflow.source URL must use https, got http")
	}
	if strings.Contains(src, "://") {
		return fmt.Errorf("workflow.source URL scheme is not supported; use an https GitHub tree URL or a path in the repository that holds this harness")
	}
	return validateLocalWorkflowSource(src)
}

// validateLocalWorkflowSource checks a relative workflow.source: a path
// in the repository that holds the harness, "." being its root.
func validateLocalWorkflowSource(src string) error {
	if strings.HasPrefix(src, "/") {
		return fmt.Errorf("workflow.source %q is an absolute path; give a path relative to the root of the repository that holds this harness (\".\" is its root)", src)
	}
	if strings.ContainsRune(src, '\\') {
		return fmt.Errorf("workflow.source %q contains a backslash; separate path segments with /", src)
	}
	if strings.ContainsAny(src, "?#") {
		return fmt.Errorf("workflow.source %q contains ? or #; a path source takes no query or pin, and a remote source must be a tree URL", src)
	}
	for _, seg := range strings.Split(src, "/") {
		switch seg {
		case "..":
			return fmt.Errorf("workflow.source %q contains \"..\"; a path source must stay inside the repository that holds this harness", src)
		case ".git", ".fullsend-cache":
			return fmt.Errorf("workflow.source %q names %s/, which is never part of a definition; point source at the definition's directory in the working tree", src, seg)
		}
	}
	return nil
}

// validateRemoteWorkflowSource checks a URL workflow.source: a GitHub
// tree URL at a full commit sha, at the repository root or a
// sub-directory, with a #sha256= tree hash. A source that base
// composition rewrote (inherited) carries no fragment: like a base
// plugin, it is pinned by the base's commit and its tree hash is recorded
// in the lock file.
func validateRemoteWorkflowSource(src string, inherited bool) error {
	cleanURL, _, hasHash := ParseIntegrityHash(src)
	if !hasHash && !inherited {
		return fmt.Errorf("workflow.source URL must end in #sha256=<64 hex characters>, the tree hash of the fetched definition, so the runner can verify what it uploads")
	}
	info, err := forge.ParseForgeURL(cleanURL)
	if err != nil {
		return fmt.Errorf("workflow.source must be a GitHub tree URL of the form https://github.com/<owner>/<repo>/tree/<commit sha>[/<path>]: %w", err)
	}
	if info.Forge != "github" {
		return fmt.Errorf("workflow.source is on %q, but a remote workflow definition must be a github.com tree URL today, because tree fetching (shared with plugins: and skills:) supports only github.com; use a path source, which works on any forge", info.Forge)
	}
	if info.PathType != "tree" {
		return fmt.Errorf("workflow.source URL must use /tree/ (a directory), not /%s/", info.PathType)
	}
	if !validCommitSHA.MatchString(info.Ref) {
		return fmt.Errorf("workflow.source ref %q is not a commit sha; pin the commit sha (40 hex characters) so the definition cannot move under the pin", info.Ref)
	}
	if info.Path != "" {
		for _, seg := range strings.Split(info.Path, "/") {
			if seg == "." || seg == ".." {
				return fmt.Errorf("workflow.source path %q must not contain \".\" or \"..\" segments", info.Path)
			}
		}
	}
	return nil
}

// ResolveBaseWorkflowSource rewrites a relative workflow.source of a
// harness fetched from baseURL (a raw.githubusercontent.com URL) into a
// tree URL at the base's repository and commit, the path taken from the
// repository root ("." is the root), as ADR 0130 rule 1 resolves a
// relative source in a base harness. The URL has no #sha256= fragment: as
// for a base plugin, the base's commit pins it and the tree hash is
// recorded when it is fetched. The raw-content directory URL is checked
// against allowlist, the check a base plugin gets. A nil workflow, or a
// source that is already a URL, is left alone.
func ResolveBaseWorkflowSource(w *WorkflowSpec, baseURL string, allowlist []string) error {
	if w == nil || w.Source == "" || IsURL(w.Source) {
		return nil
	}
	if err := validateLocalWorkflowSource(w.Source); err != nil {
		return fmt.Errorf("base %w", err)
	}
	cleanBase, _, _ := ParseIntegrityHash(baseURL)
	info, err := forge.ParseRawContentURL(cleanBase)
	if err != nil {
		return fmt.Errorf("base workflow.source %q is a relative path, so it resolves in the base harness's repository, but the base URL %s does not name one (%w); set workflow.source in the base to a tree URL with #sha256=", w.Source, cleanBase, err)
	}
	if !validCommitSHA.MatchString(info.Ref) {
		return fmt.Errorf("base workflow.source %q is a relative path, so it resolves in the base harness's repository at the base's commit, but the base URL %s pins ref %q, not a commit sha; pin base: at the full 40-character commit sha, or set workflow.source in the base to a tree URL with #sha256=", w.Source, cleanBase, info.Ref)
	}
	rel := path.Clean(w.Source)
	rawDir := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/", info.Owner, info.Repo, info.Ref)
	treeURL := fmt.Sprintf("https://github.com/%s/%s/tree/%s", info.Owner, info.Repo, info.Ref)
	if rel != "." {
		rawDir += rel + "/"
		treeURL += "/" + rel
	}
	if matchingAllowedPrefix(rawDir, allowlist) == "" {
		return fmt.Errorf("base workflow.source %q resolves to %s, which is not in allowed_remote_resources; add a prefix that covers it to allowed_remote_resources in config.yaml", w.Source, rawDir)
	}
	w.Source = treeURL
	w.baseDirURL = rawDir
	return nil
}

// credentialEnvSuffixes extend reservedPluginEnvSuffixes (_PROXY,
// _API_KEY, _TOKEN) with the other credential-shaped endings. A bare
// _KEY is not one: ISSUE_KEY names a Jira work item, which args exist to
// carry.
var credentialEnvSuffixes = []string{"_PASSWORD", "_CREDENTIALS", "_PRIVATE_KEY", "_ACCESS_KEY", "_SECRET_KEY"}

// CredentialShapedEnvName reports whether an environment variable name
// looks like it holds a credential, and the rule it matched. It matches
// the suffixes *_PROXY, *_API_KEY, *_TOKEN (reservedPluginEnvSuffixes),
// *_PASSWORD, *_CREDENTIALS, *_PRIVATE_KEY, *_ACCESS_KEY and *_SECRET_KEY,
// any name containing _SECRET, and the OTEL_* prefix, whose exporter
// headers carry collector tokens. A name equal to a suffix without its
// underscore (TOKEN, PASSWORD) matches too. A bare *_KEY such as ISSUE_KEY
// is allowed on purpose: it names a Jira work item, which args exist to
// carry. Names are compared case-insensitively.
func CredentialShapedEnvName(name string) (string, bool) {
	upper := strings.ToUpper(name)
	if strings.HasPrefix(upper, "OTEL_") {
		return "the OTEL_* family, whose exporter headers carry collector tokens", true
	}
	for _, suffix := range append(append([]string(nil), reservedPluginEnvSuffixes...), credentialEnvSuffixes...) {
		if strings.HasSuffix(upper, suffix) || upper == suffix[1:] {
			return "the *" + suffix + " family (credential-shaped names)", true
		}
	}
	if strings.Contains(upper, "_SECRET") || upper == "SECRET" {
		return "the *_SECRET* family (credential-shaped names)", true
	}
	return "", false
}

// CheckWorkflowArgsVariables refuses workflow.args when a variable the
// runner would expand in it names a credential: args reach the model
// prompt, the run plan, metrics.json and traces, so no credential may get
// in. The runner expands args only when they contain "${", so args
// without it name no variable. extra, when set, adds the runner's own
// denied names (runner-only credentials) with the rule to report.
func CheckWorkflowArgsVariables(args string, extra func(name string) (string, bool)) error {
	if !strings.Contains(args, "${") {
		return nil
	}
	var refused error
	os.Expand(args, func(name string) string {
		if refused != nil {
			return ""
		}
		rule, denied := CredentialShapedEnvName(name)
		if !denied && extra != nil {
			rule, denied = extra(name)
		}
		if denied {
			refused = fmt.Errorf("workflow.args references ${%s}, which names a credential (%s); pass work-item identifiers such as ${ISSUE_NUMBER} instead", name, rule)
		}
		return ""
	})
	return refused
}
