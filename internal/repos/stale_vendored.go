package repos

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// staleVendoredComponent is the ComponentAction component and Drift field
// reported for vendored assets an earlier vendored install left behind.
const staleVendoredComponent = "vendored-assets"

// ActionPending marks a ComponentAction for outstanding work convergence
// could not do safely, such as stale vendored assets kept because a workflow
// still calls the local reusable workflows. It is not "none", so the repo is
// not reported AlreadyCurrent.
const ActionPending = "pending"

// findStaleVendored reads the Fullsend-owned vendored assets of a GitHub
// repository whose resolved vendor setting is false. It only reads from the
// forge. It returns nil for GitLab (vendor mode is unsupported there) and for
// vendored repositories.
func findStaleVendored(ctx context.Context, resolved ResolvedConfig) (*scaffold.StaleVendoredAssets, error) {
	if resolved.Forge != ForgeGitHub || resolved.Vendor {
		return nil, nil
	}
	assets, err := scaffold.FindStaleVendoredAssets(ctx, resolved.ForgeConfig.Client, resolved.Owner, resolved.Repo)
	if err != nil {
		return nil, err
	}
	return assets, nil
}

// staleVendoredPlan is the shared answer install, dry run and status give
// about stale vendored assets.
type staleVendoredPlan struct {
	// stale lists every stale Fullsend-owned vendored file.
	stale []string
	// ignored counts vendor-manifest entries kept as not Fullsend-owned.
	ignored int
	// rewrites replace Fullsend-owned callers that would otherwise still
	// call a local reusable workflow being deleted.
	rewrites []forge.TreeFile
	// blockedBy lists workflows (sanitized for display) that would still
	// call a local reusable workflow. When non-empty nothing is deleted or
	// rewritten.
	blockedBy []string
}

// deletable reports whether the stale files can be removed now.
func (p *staleVendoredPlan) deletable() bool {
	return p != nil && len(p.stale) > 0 && len(p.blockedBy) == 0
}

// callerContentFunc returns the vendor-free content Fullsend would render
// for its own caller workflows, keyed by path, or nil when no content can
// be rendered.
type callerContentFunc func() (map[string][]byte, error)

// planStaleVendoredCleanup decides which stale vendored assets can be
// removed. planned holds the files the scaffold commit already delivers;
// their content takes precedence over the default branch. Every workflow
// that would still call a local reusable workflow being deleted is
// rewritten when it is a Fullsend-owned caller and expected supplies a
// vendor-free version; any other such workflow blocks the whole cleanup so
// the callers keep working.
func planStaleVendoredCleanup(ctx context.Context, resolved ResolvedConfig, assets *scaffold.StaleVendoredAssets, planned []forge.TreeFile, expected callerContentFunc) (*staleVendoredPlan, error) {
	if assets == nil || len(assets.Paths) == 0 {
		return nil, nil
	}
	plan := &staleVendoredPlan{stale: assets.Paths, ignored: assets.Ignored}
	targets := scaffold.LocalReusableWorkflowTargets(assets.Paths)
	if len(targets) == 0 {
		return plan, nil
	}

	deleting := make(map[string]bool, len(assets.Paths))
	for _, p := range assets.Paths {
		deleting[p] = true
	}
	plannedByPath := make(map[string]forge.TreeFile, len(planned))
	for _, f := range planned {
		if _, dup := plannedByPath[f.Path]; !dup {
			plannedByPath[f.Path] = f
		}
	}
	candidateSet := make(map[string]bool)
	for _, p := range assets.RepoFiles {
		if isWorkflowFile(p) && !deleting[p] {
			candidateSet[p] = true
		}
	}
	for _, f := range planned {
		if isWorkflowFile(f.Path) && !f.Delete && !deleting[f.Path] {
			candidateSet[f.Path] = true
		}
	}
	candidates := make([]string, 0, len(candidateSet))
	for p := range candidateSet {
		candidates = append(candidates, p)
	}
	sort.Strings(candidates)

	shimPaths := resolved.ForgeConfig.WorkflowPaths
	thinCallers := scaffold.PerRepoThinCallerPaths()
	var expectedFiles map[string][]byte
	expectedLoaded := false
	for _, p := range candidates {
		var content []byte
		if f, ok := plannedByPath[p]; ok {
			if f.Delete {
				continue
			}
			data, err := f.Bytes()
			if err != nil {
				return nil, fmt.Errorf("reading planned %s: %w", scaffold.SanitizeDisplayText(p), err)
			}
			content = data
		} else {
			data, err := resolved.ForgeConfig.Client.GetFileContent(ctx, resolved.Owner, resolved.Repo, p)
			if err != nil {
				// A workflow listed in the tree that cannot be read
				// (including a 404) fails the plan: skipping it could
				// delete a reusable workflow that it still calls.
				return nil, fmt.Errorf("reading %s: %w", scaffold.SanitizeDisplayText(p), err)
			}
			content = data
		}
		if !callsAny(content, targets) {
			continue
		}
		owned := slices.Contains(shimPaths, p) || slices.Contains(thinCallers, p)
		if owned && expected != nil {
			if !expectedLoaded {
				files, err := expected()
				if err != nil {
					return nil, fmt.Errorf("rendering caller workflows: %w", err)
				}
				expectedFiles = files
				expectedLoaded = true
			}
			want, ok := expectedFiles[p]
			if !ok && slices.Contains(shimPaths, p) {
				// The shim may be installed under another accepted
				// extension than the template renders.
				for _, alt := range shimPaths {
					if c, found := expectedFiles[alt]; found {
						want, ok = c, true
						break
					}
				}
			}
			if ok && !callsAny(want, targets) {
				plan.rewrites = append(plan.rewrites, forge.TreeFile{Path: p, Content: want, Mode: "100644"})
				continue
			}
		}
		plan.blockedBy = append(plan.blockedBy, scaffold.SanitizeDisplayText(p))
	}
	if len(plan.blockedBy) > 0 {
		plan.rewrites = nil
	}
	return plan, nil
}

// isWorkflowFile reports whether p is a top-level GitHub Actions workflow.
func isWorkflowFile(p string) bool {
	rest, ok := strings.CutPrefix(p, ".github/workflows/")
	if !ok || rest == "" || strings.Contains(rest, "/") {
		return false
	}
	return strings.HasSuffix(rest, ".yml") || strings.HasSuffix(rest, ".yaml")
}

// callsAny reports whether content references any of the local workflow
// targets. Any mention counts, which errs on the side of keeping files. The
// raw text is searched, and so is every decoded YAML scalar, so a target
// written with YAML escapes (such as a d escape for the letter d) is
// still found.
func callsAny(content []byte, targets []string) bool {
	s := string(content)
	for _, t := range targets {
		if strings.Contains(s, t) {
			return true
		}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return false
	}
	return nodeMentionsAny(&doc, targets)
}

// nodeMentionsAny reports whether any decoded scalar under n contains one of
// the targets.
func nodeMentionsAny(n *yaml.Node, targets []string) bool {
	if n == nil {
		return false
	}
	if n.Kind == yaml.ScalarNode {
		for _, t := range targets {
			if strings.Contains(n.Value, t) {
				return true
			}
		}
	}
	// Alias nodes are not followed: the anchored node is part of the
	// document and is visited directly, and following would risk cycles.
	for _, c := range n.Content {
		if nodeMentionsAny(c, targets) {
			return true
		}
	}
	return false
}

// establishedCallerContent renders the vendor-free caller workflows an
// established installation converges to. It needs a target ref (the
// manifest's fullsend_ref or the running release); without one convergence
// cannot render scaffold content, so it returns nil and callers that still
// use local targets keep the stale assets in place.
func establishedCallerContent(ctx context.Context, resolved ResolvedConfig, dcfg DriftConfig, refResolver *RefResolver) callerContentFunc {
	return func() (map[string][]byte, error) {
		if resolved.FullsendRef == "" {
			if dcfg.UpstreamRef == "" {
				return nil, nil
			}
			resolved.FullsendRef = dcfg.UpstreamRef
		}
		files, err := ExpectedScaffoldContent(ctx, resolved, dcfg, refResolver)
		if err != nil {
			return nil, err
		}
		return treeFileContents(files), nil
	}
}

// freshCallerContent renders the vendor-free caller workflows a fresh
// install writes. A fresh install always renders its callers, falling back
// to the default upstream ref when none is configured.
func freshCallerContent(resolved ResolvedConfig, dcfg DriftConfig) callerContentFunc {
	return func() (map[string][]byte, error) {
		cfg := driftInstallConfig(resolved, dcfg)
		if resolved.FullsendRef == "" {
			cfg.UpstreamRef = dcfg.UpstreamRef
			cfg.UpstreamTag = dcfg.UpstreamTag
		}
		files, err := BuildScaffoldFiles(cfg)
		if err != nil {
			return nil, err
		}
		return treeFileContents(files), nil
	}
}

func treeFileContents(files []forge.TreeFile) map[string][]byte {
	out := make(map[string][]byte, len(files))
	for _, f := range files {
		if !f.Delete {
			out[f.Path] = f.Content
		}
	}
	return out
}

// staleVendoredSummary describes the stale files for display. Paths are
// owned paths, which never contain control characters.
func staleVendoredSummary(plan *staleVendoredPlan) string {
	s := fmt.Sprintf("%d stale vendored file(s): %s", len(plan.stale), strings.Join(plan.stale, ", "))
	if plan.ignored > 0 {
		s += fmt.Sprintf("; kept %d vendor-manifest entr%s not owned by Fullsend", plan.ignored, pluralSuffix(plan.ignored, "y", "ies"))
	}
	return s
}

// staleVendoredBlockedDetail explains why stale files were kept.
func staleVendoredBlockedDetail(plan *staleVendoredPlan) string {
	return fmt.Sprintf("kept %d stale vendored file(s): %s still call local reusable workflows from an earlier vendored install; "+
		"point them at %s/.github/workflows/<name>@<ref> (for Fullsend's own callers, set fullsend_ref in repos.yaml or use a release build so repos install can rewrite them) and re-run repos install",
		len(plan.stale), strings.Join(plan.blockedBy, ", "), config.DefaultUpstreamRepo)
}

func pluralSuffix(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// applyStaleVendoredPlan returns files with the plan's caller rewrites
// (replacing any queued version of the same path) and deletions added, and
// the actions describing them. Deletions never cover a path files writes.
func applyStaleVendoredPlan(files []forge.TreeFile, plan *staleVendoredPlan, dryRun bool, repoFullName string, progress ProgressFunc) ([]forge.TreeFile, []ComponentAction) {
	if plan == nil || len(plan.stale) == 0 {
		return files, nil
	}
	if !plan.deletable() {
		detail := staleVendoredBlockedDetail(plan)
		progress(repoFullName, "warning", detail)
		return files, []ComponentAction{{Component: staleVendoredComponent, Action: ActionPending, Detail: detail}}
	}

	var actions []ComponentAction
	out := slices.Clone(files)
	for _, rw := range plan.rewrites {
		replaced := false
		for i := range out {
			if out[i].Path == rw.Path {
				out[i] = rw
				replaced = true
			}
		}
		if !replaced {
			out = append(out, rw)
		}
		verb := "rewrote"
		if dryRun {
			verb = "would rewrite"
		}
		actions = append(actions, ComponentAction{
			Component: rw.Path,
			Action:    "update",
			Detail:    fmt.Sprintf("%s %s to call the upstream reusable workflows instead of local vendored copies", verb, rw.Path),
		})
	}

	writing := make(map[string]bool, len(out))
	for _, f := range out {
		if !f.Delete {
			writing[f.Path] = true
		}
	}
	for _, p := range plan.stale {
		if !writing[p] {
			out = append(out, forge.TreeFile{Path: p, Delete: true})
		}
	}

	summary := staleVendoredSummary(plan)
	if dryRun {
		progress(repoFullName, "dry-run", "Would remove "+summary)
		actions = append(actions, ComponentAction{Component: staleVendoredComponent, Action: "delete", Detail: "would remove " + summary})
	} else {
		progress(repoFullName, "vendor", "Removing "+summary)
		actions = append(actions, ComponentAction{Component: staleVendoredComponent, Action: "delete", Detail: "remove " + summary})
	}
	return out, actions
}

// staleVendoredDriftConfig is the DriftConfig convergence renders callers with.
func staleVendoredDriftConfig(d convergeDiscovery, cfg ConvergeConfig) DriftConfig {
	return DriftConfig{
		UpstreamRef:       cfg.UpstreamRef,
		UpstreamTag:       cfg.UpstreamTag,
		ReviewAppClientID: d.reviewClientID,
		AgentRunnerTags:   gitlabAgentRunnerTags(cfg.Manifest),
		ControlRunnerTags: gitlabControlRunnerTags(cfg.Manifest),
	}
}

// checkStaleVendoredAssets reports stale vendored assets as drift, using the
// same plan install and dry run use: pending cleanup is drift, and so is
// cleanup that waits on callers of the local reusable workflows.
func checkStaleVendoredAssets(ctx context.Context, cfg ResolvedConfig, dcfg DriftConfig, resolver *RefResolver, fresh bool, status *RepoStatus) {
	assets, err := findStaleVendored(ctx, cfg)
	if err != nil {
		status.Error = fmt.Sprintf("checking stale vendored assets for %s/%s: %v", cfg.Owner, cfg.Repo, err)
		return
	}
	expected := establishedCallerContent(ctx, cfg, dcfg, resolver)
	if fresh {
		expected = freshCallerContent(cfg, dcfg)
	}
	plan, err := planStaleVendoredCleanup(ctx, cfg, assets, nil, expected)
	if err != nil {
		status.Error = fmt.Sprintf("checking stale vendored assets for %s/%s: %v", cfg.Owner, cfg.Repo, err)
		return
	}
	if plan == nil {
		return
	}
	actual := fmt.Sprintf("%d stale vendored file(s) pending removal", len(plan.stale))
	if !plan.deletable() {
		actual += fmt.Sprintf(" (kept until %s stop calling the local reusable workflows)", strings.Join(plan.blockedBy, ", "))
	}
	status.Drifts = append(status.Drifts, Drift{
		Field:    staleVendoredComponent,
		Expected: "absent",
		Actual:   actual,
	})
}
