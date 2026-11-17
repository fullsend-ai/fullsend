package repos

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// convergeScheduleCadence replaces each managed polling schedule whose cron
// differs from the repository's effective cron (specs, built from
// poll_schedules by PipelineScheduleSpecsFor) with one on the effective cron
// (#9527). Without it a poll_schedules change would reach only schedules
// created after it, because convergeSchedules only creates missing schedules
// and reactivates disabled ones. Callers pass only the specs whose cron
// poll_schedules sets explicitly, so a cron an operator edited by hand is
// kept while its key is unset.
//
// GitLab lets only a schedule's owner edit it, so the installer cannot
// change the cron in place on a schedule someone else created. Each
// replacement is therefore created before its predecessor is deleted, as the
// Jira poll reconciler and the trigger-account stage do: an interruption
// leaves a duplicate on the effective cron, which the next run keeps while
// it deletes the out-of-date one, rather than no polling schedule at all.
// A replacement keeps the ref and active state of the schedule it replaces,
// so a schedule an operator disabled stays disabled.
//
// Schedules owned by a recorded trigger-account generation are left alone:
// the installer cannot create them as the account, and the trigger-account
// stage replaces an account-owned schedule whose cron differs from its
// spec.
//
// A replacement is created as the installer and runs pipeline code as the
// installer, so only schedules the installer could have created are
// replaced: the owner must be a current project Maintainer or higher (the
// installer's own level) and the ref must be the default branch the
// installer creates them on. A schedule with a Developer, unknown or
// unreadable owner, or on another ref, is left alone, and a lookup failure
// fails closed. Likewise a schedule counts as an existing replacement only
// when it is owned by a trusted owner, is on the same ref and has the same
// active state as the one it replaces. Replacements wait for the same
// committed wrapper, template and restriction readiness as
// convergeSchedules (gitlabScheduleMutationPreflight).
func convergeScheduleCadence(ctx context.Context,
	resolved ResolvedConfig,
	specs []ScheduleSpec,
	dryRun bool,
	progress ProgressFunc) []ComponentAction {

	owner, repo := resolved.Owner, resolved.Repo
	repoFullName := owner + "/" + repo
	client := resolved.ForgeConfig.Client

	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.ComponentName)
	}
	schedules, err := client.ListPipelineSchedules(ctx, owner, repo)
	if err != nil {
		return scheduleErrorActions(names, fmt.Sprintf("listing pipeline schedules to check their cron: %v", safeAPIError("listing pipeline schedules", err)))
	}

	// installerLevel reports whether id is a current project Maintainer or
	// higher, the access level installing requires. Results are cached; an
	// unknown owner or a user without access is not trusted, and any other
	// lookup failure is returned so the caller fails closed.
	trust := map[int64]bool{}
	installerLevel := func(id int64) (bool, error) {
		if id <= 0 {
			return false, nil
		}
		if v, ok := trust[id]; ok {
			return v, nil
		}
		level, err := client.GetProjectMemberAccessLevel(ctx, owner, repo, id)
		if forge.IsNotFound(err) {
			trust[id] = false
			return false, nil
		}
		if err != nil {
			return false, safeAPIError("verifying a pipeline schedule owner", err)
		}
		trust[id] = level >= forge.GitLabAccessLevelMaintainer
		return trust[id], nil
	}
	defaultBranch := ""
	getDefaultBranch := func() (string, error) {
		if defaultBranch == "" {
			info, err := client.GetRepo(ctx, owner, repo)
			if err != nil {
				return "", safeAPIError("reading the repository", err)
			}
			defaultBranch = info.DefaultBranch
			if defaultBranch == "" {
				defaultBranch = "main"
			}
		}
		return defaultBranch, nil
	}
	preflightDone := false
	var preflight schedulePreflight
	var preflightDetail string

	var actions []ComponentAction
	for _, spec := range specs {
		name := DriftFieldName(spec.ComponentName)
		var synced, stale []forge.PipelineSchedule
		for _, s := range schedules {
			if s.Description != spec.Description {
				continue
			}
			if s.Cron == spec.Cron {
				synced = append(synced, s)
				continue
			}
			if s.Cron == "" {
				// GitLab always reports a schedule's cron; without one
				// there is nothing to compare, so the schedule is left
				// alone rather than replaced on a guess.
				continue
			}
			if recordedTriggerScheduleOwner(ctx, client, owner, repo, s.OwnerID) {
				continue
			}
			stale = append(stale, s)
		}
		if len(stale) == 0 {
			continue
		}
		branch, err := getDefaultBranch()
		if err != nil {
			actions = append(actions, ComponentAction{
				Component: spec.ComponentName,
				Action:    "error",
				Detail:    fmt.Sprintf("failed to get repo info to replace %s: %v", name, err),
			})
			continue
		}

		// Replace only schedules the installer could have created: a
		// trusted owner on the default branch. Anything else would let a
		// replacement run as the installer on a ref or at the request of
		// someone the installer never vouched for.
		var replace []forge.PipelineSchedule
		var lookupFailure string
		for _, s := range stale {
			if !sameScheduleRef(s.Ref, branch, branch) {
				actions = append(actions, ComponentAction{
					Component: spec.ComponentName,
					Action:    "none",
					Detail:    fmt.Sprintf("not replacing %s (ID %d) on ref %q: only the default branch %q is replaced", name, s.ID, s.Ref, branch),
				})
				continue
			}
			trusted, err := installerLevel(s.OwnerID)
			if err != nil {
				lookupFailure = fmt.Sprintf("failed to verify the owner of %s (ID %d): %v", name, s.ID, err)
				break
			}
			if !trusted {
				actions = append(actions, ComponentAction{
					Component: spec.ComponentName,
					Action:    "none",
					Detail:    fmt.Sprintf("not replacing %s (ID %d): its owner is not a verified project Maintainer", name, s.ID),
				})
				progress(repoFullName, "warning",
					fmt.Sprintf("Pipeline schedule %s (ID %d) is on an out-of-date cron but its owner is not a verified Maintainer; leaving it", name, s.ID))
				continue
			}
			replace = append(replace, s)
		}
		if lookupFailure != "" {
			actions = append(actions, ComponentAction{Component: spec.ComponentName, Action: "error", Detail: lookupFailure})
			continue
		}
		if len(replace) == 0 {
			continue
		}
		slices.SortFunc(replace, compareScheduleID)
		if dryRun {
			actions = append(actions, ComponentAction{
				Component: spec.ComponentName,
				Action:    "update",
				Detail:    fmt.Sprintf("would change the cron of %s from %q to %q", name, replace[0].Cron, spec.Cron),
			})
			progress(repoFullName, "dry-run",
				fmt.Sprintf("Would change the cron of pipeline schedule %s to %q", name, spec.Cron))
			continue
		}

		if !preflightDone {
			preflight, preflightDetail = gitlabScheduleMutationPreflight(ctx, client, owner, repo)
			preflightDone = true
		}
		switch preflight {
		case schedulePreflightDefer:
			actions = append(actions, ComponentAction{Component: spec.ComponentName, Action: "none", Detail: preflightDetail})
			continue
		case schedulePreflightError:
			actions = append(actions, ComponentAction{Component: spec.ComponentName, Action: "error", Detail: preflightDetail})
			continue
		}

		// Only a schedule the installer could have created, or the trigger
		// account owns, stands in for a replacement.
		var covers []forge.PipelineSchedule
		for _, c := range synced {
			if recordedTriggerScheduleOwner(ctx, client, owner, repo, c.OwnerID) {
				covers = append(covers, c)
				continue
			}
			trusted, err := installerLevel(c.OwnerID)
			if err != nil {
				lookupFailure = fmt.Sprintf("failed to verify the owner of %s (ID %d): %v", name, c.ID, err)
				break
			}
			if trusted {
				covers = append(covers, c)
			}
		}
		if lookupFailure != "" {
			actions = append(actions, ComponentAction{Component: spec.ComponentName, Action: "error", Detail: lookupFailure})
			continue
		}

		var failure string
		for _, s := range replace {
			ref := s.Ref
			if ref == "" {
				ref = branch
			}
			// A schedule on the effective cron with the same ref and active
			// state, such as a replacement an interrupted run created,
			// already takes this one's place.
			covered := slices.ContainsFunc(covers, func(c forge.PipelineSchedule) bool {
				return c.Active == s.Active && sameScheduleRef(c.Ref, ref, branch)
			})
			if !covered {
				id, err := client.CreatePipelineScheduleWithActive(ctx, owner, repo, ref, spec.Description, spec.Cron, spec.Variables, s.Active)
				if err != nil {
					failure = fmt.Sprintf("failed to create %s on cron %q: %v", name, spec.Cron, safeAPIError("creating the pipeline schedule", err))
					break
				}
				covers = append(covers, forge.PipelineSchedule{ID: id, Description: spec.Description, Ref: ref, Cron: spec.Cron, Active: s.Active})
			}
			if err := client.DeletePipelineSchedule(ctx, owner, repo, s.ID); err != nil && !forge.IsNotFound(err) {
				failure = fmt.Sprintf("failed to delete %s (ID %d) on out-of-date cron %q: %v", name, s.ID, s.Cron, safeAPIError("deleting the pipeline schedule", err))
				break
			}
		}
		if failure != "" {
			actions = append(actions, ComponentAction{Component: spec.ComponentName, Action: "error", Detail: failure})
			continue
		}
		actions = append(actions, ComponentAction{
			Component: spec.ComponentName,
			Action:    "update",
			Detail:    fmt.Sprintf("changed the cron of %s from %q to %q", name, replace[0].Cron, spec.Cron),
		})
		progress(repoFullName, "sync",
			fmt.Sprintf("Changed the cron of pipeline schedule %s to %q", name, spec.Cron))
	}
	return actions
}

// sameScheduleRef reports whether two schedule refs name the same branch. An
// empty ref means the default branch, and GitLab accepts a branch with or
// without the refs/heads/ prefix.
func sameScheduleRef(a, b, defaultBranch string) bool {
	norm := func(r string) string {
		if r == "" {
			r = defaultBranch
		}
		return strings.TrimPrefix(r, "refs/heads/")
	}
	return norm(a) == norm(b)
}
