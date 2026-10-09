package resolve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/fetch"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/gitfetch"
	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/lock"
	"github.com/fullsend-ai/fullsend/internal/pluginformat"
	"github.com/fullsend-ai/fullsend/internal/security"
)

// WorkflowDependencyField is the Dependency.Field (and lock.yaml field)
// of a remote workflow definition. A lock file that records it is written
// as version 2 (see lock.Save).
const WorkflowDependencyField = lock.WorkflowField

// ResolvedWorkflow is the workflow-definition repository a harness
// workflow: field pins (ADR 0130), fetched or read, verified, cached and
// classified as a Claude plugin or a pi extension.
type ResolvedWorkflow struct {
	// Source is workflow.source without its #sha256= fragment: a tree URL
	// (for a source pinned by a URL base, the rewritten one) or a path as
	// the harness wrote it.
	Source string
	// Local is true for a path source.
	Local bool
	// Owner, Repo, Commit and Path describe a remote source's tree URL.
	Owner, Repo, Commit, Path string
	// TreeHash is the tree hash of the cached (materialized) tree.
	TreeHash string
	// LocalPath is the cache directory, named harness.WorkflowSandboxDir,
	// so the sandbox directory the definition is uploaded under.
	LocalPath string
	// Kind is what the tree is: pluginformat.KindClaude or KindPi.
	Kind pluginformat.Kind
	// Name and Args are the harness workflow.name and workflow.args (a
	// Claude plugin only).
	Name string
	Args string
	// PluginName is the namespace Claude Code gives a Claude plugin
	// definition's workflows: the "name" in .claude-plugin/plugin.json,
	// or, without that file, the sandbox directory name.
	PluginName string
}

// Command returns the slash command that starts a Claude workflow:
// /<plugin>:<name>, followed by the literal args when set.
func (r *ResolvedWorkflow) Command() string {
	cmd := "/" + r.PluginName + ":" + r.Name
	if r.Args != "" {
		cmd += " " + r.Args
	}
	return cmd
}

// DisplaySource is the source for the run plan: a remote source shortened
// to <owner>/<repo>@<commit12>[/<path>], a path source as written.
func (r *ResolvedWorkflow) DisplaySource() string {
	if r.Local {
		return r.Source
	}
	commit := r.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	out := r.Owner + "/" + r.Repo + "@" + commit
	if r.Path != "" {
		out += "/" + r.Path
	}
	return out
}

// WorkflowLocation says where a harness came from, which decides how a
// relative workflow.source resolves.
type WorkflowLocation struct {
	// HarnessDir is the directory of the harness file. A relative source
	// resolves in the git checkout that holds it, unless a local base
	// harness declared the source (WorkflowSpec.DeclaredIn): then it
	// resolves in the checkout that holds the base file.
	HarnessDir string
	// FullsendDir is fullsend's configuration directory, which is never
	// part of a local definition.
	FullsendDir string
	// AddedByURL is true when the harness was registered by URL (fullsend
	// agent add <url>, or a config.yaml agents: entry with a URL source):
	// its relative paths would resolve in the consumer's repository, not
	// the harness's, so a relative source is refused.
	AddedByURL bool
}

// ResolveWorkflowDefinition resolves h.Workflow and delivers the
// definition: it appends the cached directory to h.Plugins, so bootstrap
// uploads it, the injection scan reads it and the runtime whose kind it
// is loads it (Claude Code with --plugin-dir, pi with -e). It returns nil
// results when the harness has no workflow: field.
//
// A remote source is checked against opts.OrgAllowlist, looked up in the
// content-addressed cache and otherwise fetched with symlinks materialized
// (opts.TreeFetcher replaces the fetcher in tests) and cached. A source
// with a #sha256= pin is found by that hash and the fetched tree must
// match it; a source that base composition pinned to the base's commit
// (no fragment) is found through the URL index, as a base plugin is, and
// its tree hash is recorded. When fullsend run has a current lock entry,
// opts.LockedWorkflowSHA256 is the hash instead, whether or not the rest
// of the entry replays: the lock wins over the URL index and over a fetch. The returned Dependency (Field "workflow")
// is what `fullsend lock` records. A path source is read from the git
// index of the checkout that holds the harness file that declared it
// (loc.HarnessDir, or a local base's directory; tracked files only, see
// gitfetch.ReadLocalTree) with the same link rules and cached by its
// computed tree hash; it has no Dependency. Both are cached under
// fetch.MaterializedCachePath, a namespace of their own that skills and
// plugins never read or write.
//
// It runs as its own step after ResolveHarness so the runtime gate can
// refuse a runtime that runs no definition before anything is fetched;
// a lock entry reaches it through opts.LockedWorkflowSHA256.
func ResolveWorkflowDefinition(ctx context.Context, h *harness.Harness, loc WorkflowLocation, opts ResolveOpts) (*ResolvedWorkflow, *Dependency, error) {
	spec := h.Workflow
	if spec == nil {
		return nil, nil, nil
	}
	if !spec.IsRemote() && loc.AddedByURL {
		return nil, nil, fmt.Errorf("workflow.source %q is relative, but this harness was added by URL, so relative paths resolve in your repository, not the harness's; pin source as a tree URL with #sha256=, or install the harness through a one-line base: harness", spec.Source)
	}

	// A relative source inherited from a local base harness resolves in
	// the checkout that holds the base file.
	if dir := spec.DeclaredIn(); dir != "" && !spec.IsRemote() {
		loc.HarnessDir = dir
	}

	rw := &ResolvedWorkflow{Name: spec.Name, Args: spec.Args}
	var dep *Dependency
	var treePath string
	var err error
	if spec.IsRemote() {
		dep, treePath, err = fetchRemoteWorkflow(ctx, spec, opts)
		if err != nil {
			return nil, nil, err
		}
		cleanURL, _, _ := harness.ParseIntegrityHash(spec.Source)
		rw.Source = cleanURL
		rw.TreeHash = dep.SHA256
		if info, parseErr := forge.ParseForgeURL(cleanURL); parseErr == nil {
			rw.Owner, rw.Repo, rw.Commit, rw.Path = info.Owner, info.Repo, info.Ref, info.Path
		}
	} else {
		rw.Local = true
		rw.Source = spec.Source
		rw.TreeHash, treePath, err = readLocalWorkflow(ctx, spec.Source, loc, opts.WorkspaceRoot)
		if err != nil {
			return nil, nil, err
		}
	}

	// Name the cached tree after the fixed sandbox directory: a harness
	// has one workflow:, and the uploaded directory takes its name from
	// the path's basename.
	namedPath, err := fetch.CacheNamedSymlink(treePath, harness.WorkflowSandboxDir)
	if err != nil {
		return nil, nil, fmt.Errorf("workflow.source: naming cached directory: %w", err)
	}
	rw.LocalPath = namedPath
	if dep != nil {
		dep.LocalPath = namedPath
	}

	if err := checkWorkflowDefinition(rw); err != nil {
		return nil, nil, err
	}
	if err := CheckWorkflowNamespace(h, rw); err != nil {
		return nil, nil, err
	}
	// The cache writes files 0600; a definition's scripts need the
	// executable bit, as fetched plugins do.
	if err := harness.ChmodPluginDir(namedPath); err != nil {
		return nil, nil, fmt.Errorf("workflow.source: setting permissions: %w", err)
	}
	h.Plugins = append(h.Plugins, harness.PluginSpec{Path: namedPath})
	return rw, dep, nil
}

// CheckWorkflowNamespace refuses a harness plugins: entry that would load
// as the definition's sandbox directory or, for a Claude plugin
// definition, under its Claude Code plugin name. The definition's own
// entry (rw.LocalPath) is skipped. A URL plugin is checked only once it is
// resolved, so a caller that resolves plugins after
// ResolveWorkflowDefinition (fullsend lock) calls it again then, and
// refuses what fullsend run refuses.
func CheckWorkflowNamespace(h *harness.Harness, rw *ResolvedWorkflow) error {
	for i, p := range h.Plugins {
		if p.Path == rw.LocalPath {
			continue
		}
		if strings.EqualFold(harness.PluginEntryDirName(p), harness.WorkflowSandboxDir) {
			return fmt.Errorf("plugins[%d] %q loads as the sandbox directory %q, which is reserved for the workflow: definition; rename that plugin directory", i, p.Path, harness.WorkflowSandboxDir)
		}
		if rw.Kind != pluginformat.KindClaude {
			continue
		}
		// Claude Code namespaces commands by plugin name, so two plugins
		// with the same name would clash on /<name>:.
		if ns, ok := pluginClaudeNamespace(p); ok && strings.EqualFold(ns, rw.PluginName) {
			return fmt.Errorf("plugins[%d] %q has the Claude Code plugin name %q, the same as the workflow: definition's plugin name %q; rename one of them (the \"name\" in %s, or the directory when there is no manifest)", i, p.Path, ns, rw.PluginName, claudeManifest)
		}
	}
	return nil
}

func fetchRemoteWorkflow(ctx context.Context, spec *harness.WorkflowSpec, opts ResolveOpts) (*Dependency, string, error) {
	cleanURL, expectedHash, hasHash := harness.ParseIntegrityHash(spec.Source)
	info, err := forge.ParseForgeURL(cleanURL)
	if err != nil {
		return nil, "", fmt.Errorf("workflow.source: %w", err)
	}
	// A source pinned by a URL base is checked against the raw-content
	// directory URL, the key a base plugin is checked against.
	allowKey := cleanURL
	if inherited := spec.InheritedFromBase(); inherited != "" {
		allowKey = inherited
	}
	allowedBy := harness.MatchingAllowedPrefixInList(allowKey, opts.OrgAllowlist)
	if allowedBy == "" {
		return nil, "", fmt.Errorf("workflow.source URL %q is not covered by allowed_remote_resources; add a prefix that covers it, such as %s, to allowed_remote_resources in config.yaml", allowKey, allowlistHint(allowKey, info))
	}
	// A current lock entry is authoritative: its hash was verified when
	// it was locked, and the definition must be that tree. Without one,
	// the URL index serves a base-pinned source: its commit pins it, and
	// the index records the tree hash it was fetched with. The index is a
	// convenience cache that anything writing the workspace can change,
	// so it never overrides the lock.
	indexKey := WorkflowDependencyField + ":" + cleanURL
	locked := opts.LockedWorkflowSHA256
	switch {
	case locked != "" && hasHash && locked != expectedHash:
		return nil, "", fmt.Errorf("workflow.source: the harness pins sha256=%s for %s, but .fullsend/lock.yaml records sha256=%s; run `fullsend lock` to update the lock file", expectedHash, cleanURL, locked)
	case locked != "":
		expectedHash = locked
	case !hasHash:
		expectedHash, _ = harness.LookupURLIndex(opts.WorkspaceRoot, indexKey)
	}

	var treePath string
	var dirEntry *fetch.DirCacheEntry
	if expectedHash != "" {
		treePath, dirEntry, err = fetch.CacheGetMaterializedDir(opts.WorkspaceRoot, expectedHash)
		if err != nil {
			return nil, "", fmt.Errorf("workflow.source: cache lookup: %w", err)
		}
	}
	cacheHit := treePath != ""
	fetchedAt := time.Now().UTC()
	if cacheHit {
		fetchedAt = dirEntry.FetchTime
	} else {
		if opts.FetchPolicy.Offline {
			return nil, "", fmt.Errorf("workflow.source %s is not in the cache and offline mode forbids fetching it; run without --offline or `fullsend lock` first", cleanURL)
		}
		fetcher := opts.TreeFetcher
		if fetcher == nil {
			fetcher = materializingFetcher
		}
		files, fetchErr := fetcher(ctx, info.CloneURL(), info.Path, info.Ref, opts.GitToken)
		if fetchErr != nil {
			var linkErr *gitfetch.LinkError
			if errors.As(fetchErr, &linkErr) {
				return nil, "", fmt.Errorf("workflow.source: %w", linkErr)
			}
			// git's stderr is external text: mask credentials in it, as
			// the runner does for other external output.
			if res := security.NewSecretRedactor().Scan(fetchErr.Error()); !res.Safe {
				fetchErr = &redactedError{msg: res.Sanitized, err: fetchErr}
			}
			if opts.GitToken == "" {
				return nil, "", fmt.Errorf("workflow.source: fetching %s: %w (hint: set GH_TOKEN or GITHUB_TOKEN for private repos)", cleanURL, fetchErr)
			}
			return nil, "", fmt.Errorf("workflow.source: fetching %s: %w", cleanURL, fetchErr)
		}
		actual := fetch.ComputeTreeHash(files)
		if hasHash && actual != expectedHash {
			return nil, "", fmt.Errorf("workflow.source: tree hash mismatch for %s: the harness pins sha256=%s but the fetched tree hashes to sha256=%s; if the commit is the one you meant, update the #sha256= fragment", cleanURL, expectedHash, actual)
		}
		if locked != "" && actual != locked {
			return nil, "", fmt.Errorf("workflow.source: tree hash mismatch for %s: .fullsend/lock.yaml records sha256=%s but the fetched tree hashes to sha256=%s; run `fullsend lock` to re-lock the definition", cleanURL, locked, actual)
		}
		expectedHash = actual
		if _, err := fetch.CachePutMaterializedDir(ctx, opts.WorkspaceRoot, cleanURL, files); err != nil {
			return nil, "", fmt.Errorf("workflow.source: caching: %w", err)
		}
		if !hasHash {
			if err := harness.RecordURLIndex(opts.WorkspaceRoot, indexKey, expectedHash); err != nil {
				return nil, "", fmt.Errorf("workflow.source: updating URL index: %w", err)
			}
		}
		cachePath, err := fetch.MaterializedCachePath(opts.WorkspaceRoot, expectedHash)
		if err != nil {
			return nil, "", fmt.Errorf("workflow.source: computing cache path: %w", err)
		}
		treePath = filepath.Join(cachePath, "tree")
	}

	if opts.AuditLogPath != "" {
		if err := fetch.AppendFetchAudit(opts.AuditLogPath, fetch.FetchAuditEntry{
			TraceID:   opts.TraceID,
			FetchTime: fetchedAt,
			URL:       cleanURL,
			SHA256:    expectedHash,
			FetchType: "static",
			AllowedBy: allowedBy,
			CacheHit:  cacheHit,
		}); err != nil {
			return nil, "", fmt.Errorf("writing fetch audit log: %w", err)
		}
	}
	// A base-pinned source is recorded under its allowlist key, as a base
	// plugin is, so a lock replay checks it against the same prefix.
	return &Dependency{
		Field:     WorkflowDependencyField,
		URL:       allowKey,
		LocalPath: treePath,
		SHA256:    expectedHash,
		FetchedAt: fetchedAt,
		CacheHit:  cacheHit,
		Type:      "directory",
	}, treePath, nil
}

// allowlistHint is an allowed_remote_resources prefix that would cover
// key: the repository on the same host.
func allowlistHint(key string, info *forge.ForgeURLInfo) string {
	if strings.HasPrefix(key, "https://raw.githubusercontent.com/") {
		return "https://raw.githubusercontent.com/" + info.Owner + "/" + info.Repo + "/"
	}
	return "https://github.com/" + info.Owner + "/" + info.Repo + "/"
}

// materializingFetcher is the default remote fetcher for definitions:
// the tree fetcher with symlinks materialized (ADR 0130 rule 2).
func materializingFetcher(ctx context.Context, cloneURL, subpath, ref, token string) (map[string][]byte, error) {
	return gitfetch.FetchTreeWithOptions(ctx, cloneURL, subpath, ref, token, gitfetch.Options{MaterializeSymlinks: true})
}

// pluginClaudeNamespace is the name Claude Code gives a plugins: entry
// that the Claude runtime loads: the exact "name" key in its
// .claude-plugin/plugin.json, or its directory name when the manifest is
// absent, unreadable or names nothing. ok is false when the entry is not a
// Claude plugin on disk yet: a URL entry before resolution (it is checked
// at run time, once resolved) or a directory the Claude runtime skips,
// such as a pi extension.
func pluginClaudeNamespace(p harness.PluginSpec) (ns string, ok bool) {
	if harness.IsURL(p.Path) {
		return "", false
	}
	if kind, _, err := pluginformat.Detect(p.Path); err != nil || kind != pluginformat.KindClaude {
		return "", false
	}
	dirName := harness.PluginEntryDirName(p)
	data, err := os.ReadFile(filepath.Join(p.Path, filepath.FromSlash(claudeManifest)))
	if err != nil {
		return dirName, true
	}
	// Decode into a map: a struct would also take "Name" or "NAME", since
	// encoding/json matches field names case-insensitively, while Claude
	// Code reads only "name".
	var m map[string]json.RawMessage
	if json.Unmarshal(data, &m) != nil {
		return dirName, true
	}
	var name string
	if raw, found := m["name"]; !found || json.Unmarshal(raw, &name) != nil || name == "" {
		return dirName, true
	}
	return name, true
}

// readLocalWorkflow reads a path source from the git index of the
// checkout that holds the harness into the content-addressed cache, so
// the uploaded directory holds only tracked files, no symlinks, and is
// the tree that was hashed. It returns the tree hash and the cached tree
// path.
func readLocalWorkflow(ctx context.Context, source string, loc WorkflowLocation, workspaceRoot string) (string, string, error) {
	const label = "workflow.source"
	if loc.HarnessDir == "" {
		return "", "", fmt.Errorf("%s: path source %q needs the repository that holds the harness, which is not known here", label, source)
	}
	top, err := gitfetch.RepoToplevel(ctx, loc.HarnessDir)
	if err != nil {
		return "", "", fmt.Errorf("%s: a path source needs the harness to be in a git checkout, and %q is not inside one (%w); run from a git checkout or pin a tree URL", label, loc.HarnessDir, err)
	}
	var exclude []string
	if loc.FullsendDir != "" {
		exclude, err = localWorkflowExclude(label, source, top, loc.FullsendDir)
		if err != nil {
			return "", "", err
		}
	}
	files, err := gitfetch.ReadLocalTree(ctx, top, source, exclude)
	if err != nil {
		return "", "", fmt.Errorf("%s %q: %w", label, source, err)
	}
	treeHash := fetch.ComputeTreeHash(files)
	treePath, _, err := fetch.CacheGetMaterializedDir(workspaceRoot, treeHash)
	if err != nil {
		return "", "", fmt.Errorf("%s: cache lookup: %w", label, err)
	}
	if treePath != "" {
		return treeHash, treePath, nil
	}
	if _, err := fetch.CachePutMaterializedDir(ctx, workspaceRoot, source, files); err != nil {
		return "", "", fmt.Errorf("%s: caching: %w", label, err)
	}
	cachePath, err := fetch.MaterializedCachePath(workspaceRoot, treeHash)
	if err != nil {
		return "", "", fmt.Errorf("%s: computing cache path: %w", label, err)
	}
	return treeHash, filepath.Join(cachePath, "tree"), nil
}

// localWorkflowExclude keeps fullsend's own configuration directory out of
// a path definition (ADR 0130 rule 2): it returns the directory's path
// relative to the checkout top, for ReadLocalTree to leave out when it lies
// under the source. A source that is the configuration directory or lies
// inside it is refused. When the configuration directory is the checkout
// top (a configuration repository), only `.` is refused: every
// sub-directory is outside the configuration files at the top, and nothing
// is left out of it.
func localWorkflowExclude(label, source, top, fullsendDir string) ([]string, error) {
	realFullsend, err := filepath.EvalSymlinks(fullsendDir)
	if err != nil {
		return nil, fmt.Errorf("%s: resolving %s: %w", label, fullsendDir, err)
	}
	rel, err := filepath.Rel(top, realFullsend)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		// The configuration directory is outside the checkout, so no
		// source can include it.
		return nil, nil
	}
	rel = filepath.ToSlash(rel)
	clean := path.Clean(source)
	if rel == "." {
		if clean == "." {
			return nil, fmt.Errorf("%s %q is the top of a checkout that is fullsend's configuration directory, which is never part of a definition; put the definition in a sub-directory and set source to its path", label, source)
		}
		return nil, nil
	}
	if clean == rel {
		return nil, fmt.Errorf("%s %q is fullsend's configuration directory, which is never part of a definition; put the definition in another directory and set source to its path", label, source)
	}
	if strings.HasPrefix(clean, rel+"/") {
		return nil, fmt.Errorf("%s %q is inside fullsend's configuration directory %s/, which is never part of a definition; put the definition in another directory and set source to its path", label, source, rel)
	}
	return []string{rel}, nil
}

// claudeManifest is Claude Code's plugin manifest. Claude Code reads the
// plugin name only from this file; fullsend's root plugin.json marks a
// Claude plugin for fullsend but Claude Code does not read it.
const claudeManifest = ".claude-plugin/plugin.json"

// checkWorkflowDefinition classifies the cached definition (recorded on
// rw.Kind) and checks what its kind needs: a Claude plugin has a usable
// namespace (recorded on rw) and ships workflows/<rw.Name>.js; a pi
// extension takes no name and no args.
func checkWorkflowDefinition(rw *ResolvedWorkflow) error {
	kind, problem, err := pluginformat.Detect(rw.LocalPath)
	if err != nil {
		return fmt.Errorf("workflow.source %s: %w", rw.Source, err)
	}
	rw.Kind = kind
	switch kind {
	case pluginformat.KindPi:
		if rw.Name != "" || rw.Args != "" {
			return fmt.Errorf("workflow.source %s is a pi extension, which starts its own sequence from its session hook, so workflow.name and workflow.args do not apply; remove them", rw.Source)
		}
		return nil
	case pluginformat.KindClaude:
	default:
		return fmt.Errorf("workflow.source %s is neither a Claude Code plugin nor a pi extension (%s): add .claude-plugin/plugin.json at the definition root for a Claude workflow, or a pi extension entry point", rw.Source, problem)
	}

	if rw.Name == "" {
		return fmt.Errorf("workflow.source %s is a Claude Code plugin, so workflow.name is required: set it to the <name> of the workflows/<name>.js script to start", rw.Source)
	}
	name, err := pluginNamespace(rw.LocalPath, harness.WorkflowSandboxDir)
	if err != nil {
		return fmt.Errorf("workflow.source %s: %w", rw.Source, err)
	}
	rw.PluginName = name

	available, err := listWorkflowScripts(filepath.Join(rw.LocalPath, "workflows"))
	if err != nil {
		return fmt.Errorf("workflow.source %s: listing workflows/: %w", rw.Source, err)
	}
	for _, a := range available {
		if a == rw.Name {
			return checkWorkflowMeta(rw)
		}
	}
	if len(available) == 0 {
		return fmt.Errorf("workflow.source %s has no workflows/%s.js, and no workflows/*.js scripts at all; workflow.name must name one", rw.Source, rw.Name)
	}
	return fmt.Errorf("workflow.source %s has no workflows/%s.js; the definition ships: %s", rw.Source, rw.Name, strings.Join(available, ", "))
}

// pluginNamespace returns the namespace Claude Code gives the plugin at
// dir when it is loaded with --plugin-dir: the "name" in
// .claude-plugin/plugin.json, or, without that file, the directory name,
// which is the fixed sandbox directory name. It refuses a manifest that sets the
// "workflows" key, which moves the scripts away from workflows/.
func pluginNamespace(dir, dirName string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(claudeManifest)))
	if errors.Is(err, os.ErrNotExist) {
		return dirName, nil
	}
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", claudeManifest, err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return "", fmt.Errorf("%s is not valid JSON: %w; fix the plugin manifest", claudeManifest, err)
	}
	if _, ok := m["workflows"]; ok {
		return "", fmt.Errorf("%s sets \"workflows\", a custom workflow path, which fullsend does not support yet; keep the scripts in workflows/ at the definition root and remove the \"workflows\" key", claudeManifest)
	}
	var name string
	if raw, ok := m["name"]; ok {
		if err := json.Unmarshal(raw, &name); err != nil {
			return "", fmt.Errorf("%s \"name\" is not a string; set it to letters, digits, _ and -", claudeManifest)
		}
	}
	if !harness.ValidWorkflowName(name) {
		return "", fmt.Errorf("%s has no usable \"name\" (got %q); set \"name\" to letters, digits, _ and -, since it is the /<name>: prefix of the workflow command", claudeManifest, name)
	}
	return name, nil
}

// listWorkflowScripts returns the sorted workflow names in dir: regular
// files named <name>.js with a valid name. The directory is read as a
// literal path, so glob characters in the workspace path do not matter.
func listWorkflowScripts(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		base, ok := strings.CutSuffix(e.Name(), ".js")
		if !ok || !e.Type().IsRegular() || !harness.ValidWorkflowName(base) {
			continue
		}
		names = append(names, base)
	}
	sort.Strings(names)
	return names, nil
}

// redactedError shows a redacted message but keeps the original error in
// its chain, so errors.Is and errors.As (a transient fetch failure, a
// cancelled context) still classify it.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }
