package repos

import (
	"context"
	"fmt"
	"slices"

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

	var actions []ComponentAction
	defaultBranch := ""
	for _, spec := range specs {
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
		slices.SortFunc(stale, compareScheduleID)
		name := DriftFieldName(spec.ComponentName)
		if dryRun {
			actions = append(actions, ComponentAction{
				Component: spec.ComponentName,
				Action:    "update",
				Detail:    fmt.Sprintf("would change the cron of %s from %q to %q", name, stale[0].Cron, spec.Cron),
			})
			continue
		}

		var failure string
		for _, s := range stale {
			// A schedule on the effective cron with the same active state,
			// such as a replacement an interrupted run created, already
			// takes this one's place.
			covered := slices.ContainsFunc(synced, func(c forge.PipelineSchedule) bool { return c.Active == s.Active })
			if !covered {
				ref := s.Ref
				if ref == "" {
					if defaultBranch == "" {
						info, err := client.GetRepo(ctx, owner, repo)
						if err != nil {
							failure = fmt.Sprintf("failed to get repo info to replace %s: %v", name, safeAPIError("reading the repository", err))
							break
						}
						defaultBranch = info.DefaultBranch
						if defaultBranch == "" {
							defaultBranch = "main"
						}
					}
					ref = defaultBranch
				}
				id, err := client.CreatePipelineScheduleWithActive(ctx, owner, repo, ref, spec.Description, spec.Cron, spec.Variables, s.Active)
				if err != nil {
					failure = fmt.Sprintf("failed to create %s on cron %q: %v", name, spec.Cron, safeAPIError("creating the pipeline schedule", err))
					break
				}
				synced = append(synced, forge.PipelineSchedule{ID: id, Description: spec.Description, Ref: ref, Cron: spec.Cron, Active: s.Active})
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
			Detail:    fmt.Sprintf("changed the cron of %s from %q to %q", name, stale[0].Cron, spec.Cron),
		})
		progress(repoFullName, "sync",
			fmt.Sprintf("Changed the cron of pipeline schedule %s to %q", name, spec.Cron))
	}
	return actions
}
