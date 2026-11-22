package repos

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"slices"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// lintComponent is the ComponentAction component of the managed lint check
// (#9789).
const lintComponent = "lint"

// lintCurrentDetail is the action detail for a caller that needs no change.
// It is not printed.
const lintCurrentDetail = scaffold.LintCallerPath + " is current"

const reusableLintWorkflowPath = ".github/workflows/reusable-fullsend-lint.yml"

// shimPinPattern captures the ref and optional tag comment of the first
// upstream reusable workflow a workflow calls: the shim or the lint caller.
var shimPinPattern = regexp.MustCompile(
	`uses:\s+` + shimOwner + `/` + shimRepo + `/\.github/workflows/[^@\s]+@(\S+)(?:[ \t]+#[ \t]*(\S+))?`,
)

// lintDelivery is how a run delivers the managed GitHub lint caller.
type lintDelivery struct {
	// pin returns the caller's pin (lintPin), given the installed managed
	// caller, if any.
	pin func(installed []byte) scaffold.RenderOptions
	// stage queues a file for the scaffold commit.
	stage func(forge.TreeFile)
	// dryRun reports the planned action and stages nothing.
	dryRun bool
	// verify checks that a ref contains the reusable lint workflow before
	// the caller is staged. Nil skips the check.
	verify func(ref string) error
	// progress prints what install did to the caller.
	progress func(detail string)
}

// lintPin returns the ref and tag comment the lint caller is pinned to:
//   - the shim's, when shim calls an upstream reusable workflow, so the check
//     and the agents run one version;
//   - else (a vendored shim) fullsendRef, the manifest's fullsend_ref as
//     written;
//   - else the installed caller's own pin, so it does not follow the CLI
//     release that runs install;
//   - else the running release.
//
// Install and status both use it, so status reports drift exactly when
// install would rewrite the caller.
func lintPin(ctx context.Context, shim, installed []byte, fullsendRef, upstreamRef, upstreamTag string, refResolver *RefResolver) scaffold.RenderOptions {
	if m := shimPinPattern.FindSubmatch(shim); m != nil {
		return scaffold.RenderOptions{UpstreamRef: string(m[1]), UpstreamTag: string(m[2])}
	}
	if fullsendRef == "" {
		if m := shimPinPattern.FindSubmatch(installed); m != nil {
			return scaffold.RenderOptions{UpstreamRef: string(m[1]), UpstreamTag: string(m[2])}
		}
	}
	target := resolveTargetRef(ctx, fullsendRef, upstreamRef, upstreamTag, refResolver)
	tag := target.tag
	// The running CLI's release tag does not describe a commit or branch pin.
	if target.manifestRef != "" && !isSemver(target.manifestRef) {
		tag = ""
	}
	return scaffold.RenderOptions{UpstreamRef: target.ref, UpstreamTag: tag}
}

// stagedOrInstalledShim returns the shim queued in files for the scaffold
// commit, else the installed one, else nil.
func stagedOrInstalledShim(ctx context.Context, resolved ResolvedConfig, files []forge.TreeFile) ([]byte, error) {
	fc := resolved.ForgeConfig
	for _, f := range files {
		if !f.Delete && slices.Contains(fc.WorkflowPaths, f.Path) {
			return f.Content, nil
		}
	}
	content, _, err := readWorkflowContent(ctx, fc.Client, resolved.Owner, resolved.Repo, fc)
	return content, err
}

// hasLintWorkflow reports whether ref (the default upstream ref when empty)
// contains the reusable lint workflow, and returns the ref it checked.
func hasLintWorkflow(ctx context.Context, refResolver *RefResolver, ref string) (string, bool, error) {
	if ref == "" {
		ref = config.DefaultUpstreamRef
	}
	_, err := refResolver.client.GetFileContentAtRef(ctx, shimOwner, shimRepo, reusableLintWorkflowPath, ref)
	if forge.IsNotFound(err) {
		return ref, false, nil
	}
	if err != nil {
		return ref, false, fmt.Errorf("checking %s at %q: %w", reusableLintWorkflowPath, ref, err)
	}
	return ref, true, nil
}

// verifyLintWorkflowAt returns a lintDelivery.verify function, or nil without
// an upstream client.
func verifyLintWorkflowAt(ctx context.Context, refResolver *RefResolver) func(string) error {
	if refResolver == nil {
		return nil
	}
	return func(ref string) error {
		ref, found, err := hasLintWorkflow(ctx, refResolver, ref)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("the fullsend workflow is pinned to %q, which does not include %s required by lint: true; upgrade fullsend_ref to a ref that includes it, or set lint to false",
				ref, reusableLintWorkflowPath)
		}
		return nil
	}
}

// checkGitHubLintWorkflow fails, before any write, when the target ref lacks
// the reusable lint workflow (#9787). It passes for a user-owned caller, and
// for a repository whose installed shim has a ref: lintDelivery.verify then
// checks the ref the shim ends up on.
func checkGitHubLintWorkflow(ctx context.Context, resolved ResolvedConfig, cfg ConvergeConfig, refResolver *RefResolver) error {
	if !resolved.Lint || resolved.Forge != ForgeGitHub {
		return nil
	}
	repo := resolved.Owner + "/" + resolved.Repo
	caller, exists, err := readLintCaller(ctx, resolved.ForgeConfig.Client, resolved.Owner, resolved.Repo)
	if err != nil {
		return fmt.Errorf("%s: checking the managed lint caller before install: %w", repo, err)
	}
	if exists && !scaffold.IsManagedLintCaller(caller) {
		return nil
	}

	target := resolveTargetRef(ctx, resolved.FullsendRef, cfg.UpstreamRef, cfg.UpstreamTag, refResolver)
	source := "release-default ref"
	if target.manifestRef != "" {
		source = "pinned fullsend_ref"
	}
	if refResolver == nil {
		if target.manifestRef != "" {
			return fmt.Errorf("%s: cannot verify pinned fullsend_ref %q contains %s without an upstream GitHub client; use a ref that includes the lint workflow",
				repo, target.manifestRef, reusableLintWorkflowPath)
		}
		return nil
	}
	ref, found, err := hasLintWorkflow(ctx, refResolver, target.ref)
	if err != nil {
		return fmt.Errorf("%s: before install: %w", repo, err)
	}
	if found {
		return nil
	}
	// The caller follows an installed shim, which may stay on another ref
	// (a refused downgrade). lintDelivery.verify decides then.
	shim, err := stagedOrInstalledShim(ctx, resolved, nil)
	if err != nil {
		return fmt.Errorf("%s: checking the installed fullsend workflow before install: %w", repo, err)
	}
	if shimPinPattern.Match(shim) {
		return nil
	}
	return fmt.Errorf("%s: %s %q does not include %s required by lint: true; upgrade fullsend_ref to a ref that includes it, or set lint to false",
		repo, source, ref, reusableLintWorkflowPath)
}

// convergeLint makes the managed lint caller present and current when lint is
// true, and absent otherwise. It never touches a caller without
// scaffold.LintCallerMarker. A failure is returned as an "error" action.
func convergeLint(ctx context.Context, resolved ResolvedConfig, delivery lintDelivery) []ComponentAction {
	if resolved.Forge != ForgeGitHub {
		if !resolved.Lint {
			return nil
		}
		detail := fmt.Sprintf("lint is true, but this release does not manage the lint check on %s yet", resolved.Forge)
		delivery.report(detail)
		return []ComponentAction{{Component: lintComponent, Action: "none", Detail: detail}}
	}
	var actions []ComponentAction
	var err error
	if resolved.Lint {
		actions, err = ensureLintCaller(ctx, resolved, delivery)
	} else {
		actions, err = removeLintCaller(ctx, resolved.ForgeConfig.Client, resolved.Owner, resolved.Repo, delivery)
	}
	for _, a := range actions {
		delivery.report(a.Detail)
	}
	if err != nil {
		actions = append(actions, ComponentAction{
			Component: lintComponent,
			Action:    "error",
			Detail:    fmt.Sprintf("failed to converge the lint workflow: %v", err),
		})
	}
	return actions
}

// report prints detail, except the routine "is current" result.
func (d lintDelivery) report(detail string) {
	if d.progress != nil && detail != lintCurrentDetail {
		d.progress(detail)
	}
}

// ensureLintCaller makes the managed caller present and current.
func ensureLintCaller(ctx context.Context, resolved ResolvedConfig, delivery lintDelivery) ([]ComponentAction, error) {
	have, exists, err := readLintCaller(ctx, resolved.ForgeConfig.Client, resolved.Owner, resolved.Repo)
	if err != nil {
		return nil, err
	}
	if exists && !scaffold.IsManagedLintCaller(have) {
		return []ComponentAction{{
			Component: lintComponent,
			Action:    "none",
			Detail: fmt.Sprintf("%s exists but is not managed by fullsend; left it untouched (remove it to let repos install manage the lint workflow)",
				scaffold.LintCallerPath),
		}}, nil
	}
	pin := delivery.pin(have)
	want, err := scaffold.RenderLintCaller(pin)
	if err != nil {
		return nil, err
	}
	if exists && bytes.Equal(have, want) {
		return []ComponentAction{{
			Component: lintComponent,
			Action:    "none",
			Detail:    lintCurrentDetail,
		}}, nil
	}
	action := "add"
	if exists {
		action = "update"
	}
	if delivery.verify != nil {
		if err := delivery.verify(pin.UpstreamRef); err != nil {
			return nil, err
		}
	}
	if delivery.dryRun {
		return []ComponentAction{{
			Component: lintComponent,
			Action:    action,
			Detail:    fmt.Sprintf("would %s %s", action, scaffold.LintCallerPath),
		}}, nil
	}
	delivery.stage(forge.TreeFile{Path: scaffold.LintCallerPath, Content: want, Mode: "100644"})
	return []ComponentAction{{
		Component: lintComponent,
		Action:    action,
		Detail:    fmt.Sprintf("%s %s with the scaffold", action, scaffold.LintCallerPath),
	}}, nil
}

// removeLintCaller deletes the managed caller. An absent or user-owned file
// yields no action.
func removeLintCaller(ctx context.Context, client forge.Client, owner, repo string, delivery lintDelivery) ([]ComponentAction, error) {
	have, exists, err := readLintCaller(ctx, client, owner, repo)
	if err != nil {
		return nil, err
	}
	if !exists || !scaffold.IsManagedLintCaller(have) {
		return nil, nil
	}
	if delivery.dryRun {
		return []ComponentAction{{
			Component: lintComponent,
			Action:    "delete",
			Detail:    "would delete " + scaffold.LintCallerPath,
		}}, nil
	}
	delivery.stage(forge.TreeFile{Path: scaffold.LintCallerPath, Delete: true})
	return []ComponentAction{{
		Component: lintComponent,
		Action:    "delete",
		Detail:    "delete " + scaffold.LintCallerPath + " with the scaffold",
	}}, nil
}

// checkLintDrift reports a managed lint caller that is missing or outdated
// while lint is true, or present while it is not. A user-owned caller is
// never drift.
func checkLintDrift(ctx context.Context, cfg ResolvedConfig, dcfg DriftConfig, refResolver *RefResolver, status *RepoStatus) {
	if cfg.Forge != ForgeGitHub {
		return
	}
	have, exists, err := readLintCaller(ctx, cfg.ForgeConfig.Client, cfg.Owner, cfg.Repo)
	if err != nil {
		status.Error = fmt.Sprintf("checking the lint workflow for %s/%s: %v", cfg.Owner, cfg.Repo, err)
		return
	}
	managed := exists && scaffold.IsManagedLintCaller(have)
	switch {
	case exists && !managed:
	case !cfg.Lint && managed:
		status.Drifts = append(status.Drifts, Drift{Field: scaffold.LintCallerPath, Expected: "absent", Actual: "present (lint is not enabled)"})
	case cfg.Lint && !exists:
		status.Drifts = append(status.Drifts, Drift{Field: scaffold.LintCallerPath, Expected: "present", Actual: "missing"})
	case cfg.Lint:
		shim, shimErr := stagedOrInstalledShim(ctx, cfg, nil)
		if shimErr != nil {
			status.Error = fmt.Sprintf("checking the lint workflow for %s/%s: %v", cfg.Owner, cfg.Repo, shimErr)
			return
		}
		want, renderErr := scaffold.RenderLintCaller(lintPin(ctx, shim, have, cfg.FullsendRef, dcfg.UpstreamRef, dcfg.UpstreamTag, refResolver))
		if renderErr != nil {
			status.Error = fmt.Sprintf("rendering the lint workflow for %s/%s: %v", cfg.Owner, cfg.Repo, renderErr)
			return
		}
		if !bytes.Equal(have, want) {
			status.Drifts = append(status.Drifts, Drift{Field: scaffold.LintCallerPath, Expected: "current template", Actual: "installed content differs"})
		}
	}
}

// readLintCaller reads the caller from the default branch; a missing file is
// not an error.
func readLintCaller(ctx context.Context, client forge.Client, owner, repo string) ([]byte, bool, error) {
	content, err := client.GetFileContent(ctx, owner, repo, scaffold.LintCallerPath)
	if err != nil {
		if forge.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("reading %s: %w", scaffold.LintCallerPath, err)
	}
	return content, true, nil
}
