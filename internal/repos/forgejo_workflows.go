package repos

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

const (
	// forgejoWorkflowIndexPath lists the workflow files Fullsend manages in
	// a Forgejo repository. Only files it lists are updated or removed.
	forgejoWorkflowIndexPath = ".fullsend/forgejo-workflows.yaml"
	// forgejoWorkflowIndexMarker is the first line of a Fullsend-written
	// index; an index without it is not Fullsend's and is never changed.
	forgejoWorkflowIndexMarker = "# fullsend-managed: forgejo-workflows\n"
	// forgejoShimPath is the consumer shim workflow.
	forgejoShimPath = ".forgejo/workflows/fullsend.yml"
	// forgejoDispatchPath is the bundle's reusable dispatch workflow the
	// shim calls.
	forgejoDispatchPath = ".forgejo/workflows/fullsend-dispatch.yml"
	// forgejoIndexVersion is the schema version of the workflow index.
	forgejoIndexVersion = 1
	// forgejoBundleTagPrefix starts the tag that names a published bundle
	// version on the workflow host.
	forgejoBundleTagPrefix = "fullsend-bundle-"
	// forgejoPendingPinPrefix starts the placeholder pin a dry run shows for
	// a bundle that would be published.
	forgejoPendingPinPrefix = "<pending "
)

// forgejoManagedPrefixes bound the paths an index may claim: the reserved
// workflow files and the index and helpers under .fullsend/forgejo. A listed
// path outside them is ignored, so an edited index cannot make repos
// install or uninstall rewrite or delete unrelated repository files, such as
// .fullsend/config.yaml, which has its own ownership marker.
var forgejoManagedPrefixes = []string{".forgejo/workflows/fullsend", ".fullsend/forgejo"}

func forgejoManagedPath(p string) bool {
	if strings.Contains(p, "..") || strings.HasPrefix(p, "/") {
		return false
	}
	for _, prefix := range forgejoManagedPrefixes {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// ForgejoBundle is a version of the Fullsend workflow bundle: the reusable
// workflows and helpers published to a workflow host or vendored into a
// repository.
type ForgejoBundle struct {
	// Version identifies the content: the first 12 hex digits of a SHA-256
	// over every path and file. Any content change yields a new version.
	Version string
	// Files maps repository paths to content.
	Files map[string][]byte
}

// NewForgejoBundle returns the bundle holding files, with its version
// computed from their content.
func NewForgejoBundle(files map[string][]byte) ForgejoBundle {
	h := sha256.New()
	for _, p := range sortedKeys(files) {
		fmt.Fprintf(h, "%s\x00%d\x00", p, len(files[p]))
		h.Write(files[p])
	}
	return ForgejoBundle{Version: hex.EncodeToString(h.Sum(nil))[:12], Files: files}
}

// LoadForgejoBundle returns the workflow bundle embedded in this build.
func LoadForgejoBundle() (ForgejoBundle, error) {
	files := make(map[string][]byte)
	if err := scaffold.WalkForgejoBundle(func(p string, content []byte) error {
		if !forgejoManagedPath(p) {
			return fmt.Errorf("bundle file %q is outside the managed workflow paths", p)
		}
		files[p] = content
		return nil
	}); err != nil {
		return ForgejoBundle{}, fmt.Errorf("loading Forgejo workflow bundle: %w", err)
	}
	if _, ok := files[forgejoDispatchPath]; !ok {
		return ForgejoBundle{}, fmt.Errorf("loading Forgejo workflow bundle: %s is missing", forgejoDispatchPath)
	}
	return NewForgejoBundle(files), nil
}

// Tag returns the tag that marks this version on a workflow host.
func (b ForgejoBundle) Tag() string { return forgejoBundleTagPrefix + b.Version }

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// forgejoRole is how a declared Forgejo repository uses the workflow bundle.
type forgejoRole string

const (
	// forgejoRoleConsumer pins its shim to the instance's workflow host.
	forgejoRoleConsumer forgejoRole = "consumer"
	// forgejoRoleVendored carries its own copy of the bundle.
	forgejoRoleVendored forgejoRole = "vendored"
	// forgejoRoleHost publishes the bundle for the instance.
	forgejoRoleHost forgejoRole = "workflow-host"
)

// forgejoRoleOf resolves t's role from its manifest entry. An undeclared
// target is a consumer.
func (m *Manifest) forgejoRoleOf(t ForgejoTarget) forgejoRole {
	if m.Forgejo == nil {
		return forgejoRoleConsumer
	}
	for _, inst := range m.Forgejo.Instances {
		if !strings.EqualFold(inst.Alias, t.Alias) {
			continue
		}
		for _, r := range inst.Repos {
			if !strings.EqualFold(r.Name, t.Path()) {
				continue
			}
			switch {
			case r.WorkflowHost:
				return forgejoRoleHost
			case resolveBoolField(r.Vendor, m.Defaults.Vendor, false):
				return forgejoRoleVendored
			}
			return forgejoRoleConsumer
		}
	}
	return forgejoRoleConsumer
}

// forgejoHost returns the workflow host declared on the alias's instance.
func (m *Manifest) forgejoHost(alias string) (ForgejoTarget, bool) {
	for _, t := range m.forgejoTargets() {
		if strings.EqualFold(t.Alias, alias) && m.forgejoRoleOf(t) == forgejoRoleHost {
			return t, true
		}
	}
	return ForgejoTarget{}, false
}

// forgejoDependents returns the declared repositories that call host's
// bundle: every non-vendored repository on the same instance. Consumers
// that repos.yaml does not declare cannot be enumerated.
func (m *Manifest) forgejoDependents(host ForgejoTarget) []ForgejoTarget {
	var out []ForgejoTarget
	for _, t := range m.forgejoTargets() {
		if strings.EqualFold(t.Alias, host.Alias) && m.forgejoRoleOf(t) == forgejoRoleConsumer {
			out = append(out, t)
		}
	}
	return out
}

// forgejoIndex is the parsed workflow index.
type forgejoIndex struct {
	Version int      `yaml:"version"`
	Role    string   `yaml:"role"`
	Bundle  string   `yaml:"bundle"`
	Files   []string `yaml:"files"`
}

// renderForgejoIndex returns the index listing files (and itself).
func renderForgejoIndex(role forgejoRole, bundle ForgejoBundle, files []string) []byte {
	var b strings.Builder
	b.WriteString(forgejoWorkflowIndexMarker)
	b.WriteString("# Lists the files 'fullsend repos' manages in this repository. repos install\n")
	b.WriteString("# rewrites them and repos uninstall removes them with this file.\n")
	fmt.Fprintf(&b, "version: %d\nrole: %s\nbundle: %s\nfiles:\n", forgejoIndexVersion, role, bundle.Tag())
	for _, f := range files {
		fmt.Fprintf(&b, "  - %s\n", f)
	}
	return []byte(b.String())
}

// renderForgejoShim returns the consumer shim calling dispatch.
func renderForgejoShim(dispatch, source string) ([]byte, error) {
	tmpl, err := scaffold.ForgejoShimTemplate()
	if err != nil {
		return nil, fmt.Errorf("reading Forgejo shim template: %w", err)
	}
	if err := validateForgejoShimValue("workflow reference", dispatch, false); err != nil {
		return nil, err
	}
	if err := validateForgejoShimValue("workflow source", source, false); err != nil {
		return nil, err
	}
	s := strings.ReplaceAll(string(tmpl), "__REUSABLE_DISPATCH__", dispatch)
	return []byte(strings.ReplaceAll(s, "__DISPATCH_SOURCE__", source)), nil
}

// validateForgejoShimValue rejects a value that could break out of the YAML
// scalar or comment it is substituted into: any control character or Unicode
// line break and, when strict (a plain scalar), any whitespace.
func validateForgejoShimValue(what, v string, strict bool) error {
	for _, r := range v {
		bad := unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
		if strict {
			bad = bad || unicode.IsSpace(r)
		}
		if bad {
			return fmt.Errorf("the %s %q contains a character that is not allowed in a generated shim", what, v)
		}
	}
	return nil
}

// forgejoCommitPin matches a full hex commit SHA.
var forgejoCommitPin = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// forgejoRemoteDispatch returns the reference to host's dispatch workflow
// at the commit pin. It names the host by its full instance URL, so the
// reference always resolves on the same instance, path prefix included.
func forgejoRemoteDispatch(host ForgejoTarget, pin string) string {
	return strings.TrimRight(host.BaseURL, "/") + "/" + host.Path() + "/" + forgejoDispatchPath + "@" + pin
}

// desiredForgejoWorkflows returns the workflow files t's role needs, with
// the index. pin is the host bundle commit a consumer's shim references.
func desiredForgejoWorkflows(role forgejoRole, bundle ForgejoBundle, host ForgejoTarget, pin string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	if role != forgejoRoleConsumer {
		for p, c := range bundle.Files {
			files[p] = c
		}
	}
	switch role {
	case forgejoRoleVendored:
		shim, err := renderForgejoShim("./"+forgejoDispatchPath, "vendored copy of "+bundle.Tag()+" in this repository")
		if err != nil {
			return nil, err
		}
		files[forgejoShimPath] = shim
	case forgejoRoleConsumer:
		if !forgejoCommitPin.MatchString(pin) && !strings.HasPrefix(pin, forgejoPendingPinPrefix) {
			return nil, fmt.Errorf("the workflow host pin %q is not a commit SHA", pin)
		}
		if err := validateForgejoShimValue("workflow host URL", forgejoRemoteDispatch(host, ""), true); err != nil {
			return nil, err
		}
		shim, err := renderForgejoShim(forgejoRemoteDispatch(host, pin), host.Selector()+" at "+bundle.Tag())
		if err != nil {
			return nil, err
		}
		files[forgejoShimPath] = shim
	}
	listed := append(sortedKeys(files), forgejoWorkflowIndexPath)
	sort.Strings(listed)
	files[forgejoWorkflowIndexPath] = renderForgejoIndex(role, bundle, listed)
	return files, nil
}

// readForgejoIndex reads the workflow index. It returns the index file
// (nil when absent), whether Fullsend wrote it, and the managed paths it
// lists.
func readForgejoIndex(ctx context.Context, c ForgejoFileClient, t ForgejoTarget) (*ForgejoFile, bool, map[string]bool, error) {
	f, err := c.GetFile(ctx, t.Owner, t.Repo, forgejoWorkflowIndexPath)
	if forge.IsNotFound(err) {
		return nil, false, map[string]bool{}, nil
	}
	if err != nil {
		return nil, false, nil, err
	}
	if !bytes.HasPrefix(f.Content, []byte(forgejoWorkflowIndexMarker)) {
		return f, false, nil, nil
	}
	var idx forgejoIndex
	if err := yaml.Unmarshal(f.Content, &idx); err != nil {
		return nil, false, nil, fmt.Errorf("parsing %s: %w", forgejoWorkflowIndexPath, err)
	}
	if idx.Version > forgejoIndexVersion {
		return nil, false, nil, fmt.Errorf("%s has version %d; this fullsend understands up to version %d, so upgrade fullsend", forgejoWorkflowIndexPath, idx.Version, forgejoIndexVersion)
	}
	owned := map[string]bool{forgejoWorkflowIndexPath: true}
	for _, p := range idx.Files {
		if forgejoManagedPath(p) {
			owned[p] = true
		}
	}
	return f, true, owned, nil
}

// forgejoWorkflowPlan is the change that converges a repository's
// workflow files.
type forgejoWorkflowPlan struct {
	// State is not-installed, up-to-date, drift, or unmanaged.
	State   ForgejoState
	Changes []ForgejoFileChange
	// Conflict names the file that makes the state unmanaged.
	Conflict string
}

// planForgejoWorkflows compares t's workflow files with desired. A desired
// path that exists but is not listed in a Fullsend index is a conflict: it
// is never overwritten.
func planForgejoWorkflows(ctx context.Context, c ForgejoFileClient, t ForgejoTarget, desired map[string][]byte) (forgejoWorkflowPlan, error) {
	index, ours, owned, err := readForgejoIndex(ctx, c, t)
	if err != nil {
		return forgejoWorkflowPlan{}, err
	}
	if index != nil && !ours {
		return forgejoWorkflowPlan{State: ForgejoStateUnmanaged, Conflict: forgejoWorkflowIndexPath}, nil
	}
	var plan forgejoWorkflowPlan
	present := false
	for _, p := range sortedKeys(desired) {
		existing := index
		if p != forgejoWorkflowIndexPath {
			existing, err = c.GetFile(ctx, t.Owner, t.Repo, p)
			if forge.IsNotFound(err) {
				existing, err = nil, nil
			}
			if err != nil {
				return forgejoWorkflowPlan{}, err
			}
		}
		if existing == nil {
			plan.Changes = append(plan.Changes, ForgejoFileChange{Operation: ForgejoFileCreate, Path: p, Content: desired[p]})
			continue
		}
		present = true
		if !owned[p] {
			return forgejoWorkflowPlan{State: ForgejoStateUnmanaged, Conflict: p}, nil
		}
		if !bytes.Equal(existing.Content, desired[p]) {
			plan.Changes = append(plan.Changes, ForgejoFileChange{Operation: ForgejoFileUpdate, Path: p, Content: desired[p], SHA: existing.SHA})
		}
	}
	stale := make([]string, 0, len(owned))
	for p := range owned {
		if _, ok := desired[p]; !ok {
			stale = append(stale, p)
		}
	}
	sort.Strings(stale)
	for _, p := range stale {
		f, err := c.GetFile(ctx, t.Owner, t.Repo, p)
		if forge.IsNotFound(err) {
			continue
		}
		if err != nil {
			return forgejoWorkflowPlan{}, err
		}
		present = true
		plan.Changes = append(plan.Changes, ForgejoFileChange{Operation: ForgejoFileDelete, Path: p, SHA: f.SHA})
	}
	if want, ok := desired[forgejoWorkflowIndexPath]; ok && index != nil && len(plan.Changes) > 0 && bytes.Equal(index.Content, want) {
		// Re-write the unchanged index so the commit is conditional on the
		// index revision that authorized it: a concurrent ownership
		// withdrawal rejects the whole commit.
		plan.Changes = append(plan.Changes, ForgejoFileChange{Operation: ForgejoFileUpdate, Path: forgejoWorkflowIndexPath, Content: want, SHA: index.SHA})
	}
	switch {
	case len(plan.Changes) == 0:
		plan.State = ForgejoStateUpToDate
	case !present:
		plan.State = ForgejoStateNotInstalled
	default:
		plan.State = ForgejoStateDrift
	}
	return plan, nil
}

// planForgejoWorkflowRemoval returns the deletes that remove every file a
// Fullsend index lists, and the index. A repository without an index has
// no ownership record: it is not installed unless a Fullsend workflow file
// remains, which is reported as unmanaged and left in place.
func planForgejoWorkflowRemoval(ctx context.Context, c ForgejoFileClient, t ForgejoTarget) (forgejoWorkflowPlan, error) {
	index, ours, owned, err := readForgejoIndex(ctx, c, t)
	switch {
	case err != nil:
		return forgejoWorkflowPlan{}, err
	case index == nil:
		for _, p := range []string{forgejoShimPath, forgejoDispatchPath} {
			if _, err := c.GetFile(ctx, t.Owner, t.Repo, p); err == nil {
				return forgejoWorkflowPlan{State: ForgejoStateUnmanaged, Conflict: p}, nil
			} else if !forge.IsNotFound(err) {
				return forgejoWorkflowPlan{}, err
			}
		}
		return forgejoWorkflowPlan{State: ForgejoStateNotInstalled}, nil
	case !ours:
		return forgejoWorkflowPlan{State: ForgejoStateUnmanaged, Conflict: forgejoWorkflowIndexPath}, nil
	}
	paths := make([]string, 0, len(owned))
	for p := range owned {
		if p != forgejoWorkflowIndexPath {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	plan := forgejoWorkflowPlan{State: ForgejoStateRemoved}
	for _, p := range paths {
		f, err := c.GetFile(ctx, t.Owner, t.Repo, p)
		if forge.IsNotFound(err) {
			continue
		}
		if err != nil {
			return forgejoWorkflowPlan{}, err
		}
		plan.Changes = append(plan.Changes, ForgejoFileChange{Operation: ForgejoFileDelete, Path: p, SHA: f.SHA})
	}
	plan.Changes = append(plan.Changes, ForgejoFileChange{Operation: ForgejoFileDelete, Path: forgejoWorkflowIndexPath, SHA: index.SHA})
	return plan, nil
}

// verifyForgejoBundleAt checks that every bundle file at ref matches the
// bundle, so a consumer is never pinned to a tag that was moved or created
// over other content.
func verifyForgejoBundleAt(ctx context.Context, c ForgejoFileClient, host ForgejoTarget, ref string, bundle ForgejoBundle) error {
	// The ref comes from the server; reject anything that is not a full
	// commit SHA before it is used in a request or an error message.
	if !forgejoCommitPin.MatchString(ref) {
		return fmt.Errorf("tag %s on %s resolved to %q, which is not a full commit SHA", bundle.Tag(), host.Selector(), ref)
	}
	for _, p := range sortedKeys(bundle.Files) {
		f, err := c.GetFileAt(ctx, host.Owner, host.Repo, p, ref)
		if forge.IsNotFound(err) {
			return fmt.Errorf("tag %s on %s points at commit %q, which lacks %s; delete the tag, then rerun repos install for the workflow host",
				bundle.Tag(), host.Selector(), ref, p)
		}
		if err != nil {
			return fmt.Errorf("verifying %s at %s: %w", p, bundle.Tag(), err)
		}
		if !bytes.Equal(f.Content, bundle.Files[p]) {
			return fmt.Errorf("tag %s on %s points at commit %q, whose %s differs from the bundle; delete the tag, then rerun repos install for the workflow host",
				bundle.Tag(), host.Selector(), ref, p)
		}
	}
	return nil
}

// forgejoPin is the published commit of a host's bundle.
type forgejoPin struct {
	SHA       string
	Published bool
	// Pending is set in a dry run whose host would publish the bundle.
	Pending bool
	Err     error
}

// lookupForgejoPin reads, without writing, the commit the bundle's tag
// points at on host and verifies its content.
func lookupForgejoPin(ctx context.Context, c ForgejoFileClient, host ForgejoTarget, bundle ForgejoBundle) forgejoPin {
	sha, err := c.GetTag(ctx, host.Owner, host.Repo, bundle.Tag())
	if forge.IsNotFound(err) {
		return forgejoPin{}
	}
	if err != nil {
		return forgejoPin{Err: fmt.Errorf("reading tag %s on %s: %w", bundle.Tag(), host.Selector(), err)}
	}
	if err := verifyForgejoBundleAt(ctx, c, host, sha, bundle); err != nil {
		return forgejoPin{Err: err}
	}
	return forgejoPin{SHA: sha, Published: true}
}

// unmanagedDetail explains why an unmanaged plan leaves workflow files in
// place.
func (p forgejoWorkflowPlan) unmanagedDetail() string {
	if p.Conflict == forgejoWorkflowIndexPath {
		return forgejoWorkflowIndexPath + " exists without the Fullsend ownership marker; workflow files are left unchanged"
	}
	return p.Conflict + " exists but no Fullsend ownership index lists it; it is left unchanged"
}

// forgejoConflictError explains a workflow file Fullsend does not own.
func forgejoConflictError(path string) error {
	if path == forgejoWorkflowIndexPath {
		return fmt.Errorf("%s exists without the Fullsend ownership marker; move or delete it, then rerun repos install", path)
	}
	return fmt.Errorf("%s exists but is not listed in %s, so Fullsend does not own it; move or delete it, then rerun repos install", path, forgejoWorkflowIndexPath)
}

// combineForgejoStatus merges the status of a repository's components.
func combineForgejoStatus(states ...ForgejoState) ForgejoState {
	count := make(map[ForgejoState]int)
	for _, s := range states {
		count[s]++
	}
	switch {
	case count[ForgejoStateError] > 0:
		return ForgejoStateError
	case count[ForgejoStateUnmanaged] > 0:
		return ForgejoStateUnmanaged
	case count[ForgejoStateUpToDate] == len(states):
		return ForgejoStateUpToDate
	case count[ForgejoStateNotInstalled] == len(states):
		return ForgejoStateNotInstalled
	default:
		return ForgejoStateDrift
	}
}

// combineForgejoInstall merges per-component install outcomes.
func combineForgejoInstall(states ...ForgejoState) ForgejoState {
	count := make(map[ForgejoState]int)
	for _, s := range states {
		count[s]++
	}
	switch {
	case count[ForgejoStateUpToDate] == len(states):
		return ForgejoStateUpToDate
	case count[ForgejoStateInstalled] == len(states):
		return ForgejoStateInstalled
	default:
		return ForgejoStateUpgraded
	}
}

// installOutcome maps a planned state to the state a write produces.
func installOutcome(planned ForgejoState) ForgejoState {
	switch planned {
	case ForgejoStateUpToDate:
		return ForgejoStateUpToDate
	case ForgejoStateNotInstalled:
		return ForgejoStateInstalled
	default:
		return ForgejoStateUpgraded
	}
}

func joinDetails(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "; ")
}
