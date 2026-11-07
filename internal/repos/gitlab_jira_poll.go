package repos

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// gitlabJiraPollReconciler manages the Fullsend-owned GitLab Jira poll
// resources (#9108).
//
// The Jira poll job is the shipped poll job in fullsend-poll.yml: its rules
// select FULLSEND_POLL_MODE=jira only for a pipeline started by the
// jiraPollScheduleSpec schedule, and run-poll-job.sh then runs
// `fullsend poll --input-driver jira-poll --target-forge gitlab` and keeps
// FULLSEND_JIRA_TOKEN and FULLSEND_JIRA_EMAIL only in that mode. The poll
// template stays identical to the shipped scaffold, which the trigger
// dispatch gate requires, so the per-repository resource this reconciler
// converges is the schedule: without it no Jira poll job is ever created.
//
// The reconciler touches only schedules with the Jira poll description. The
// managed event-polling schedules (PipelineScheduleSpecs), other schedules,
// the CI configuration and the Jira credentials are left alone.
type gitlabJiraPollReconciler struct{}

// Ensure makes exactly one current Jira poll schedule present: on the
// default branch, with the spec's UTC cron and no pipeline variables. It
// waits, reporting why, until the committed poll template and job script
// can run the Jira poll and the pipeline-variable override restriction is
// established, so a schedule never starts a pipeline that cannot run the
// Jira poller safely. A schedule an operator disabled is not reactivated.
func (gitlabJiraPollReconciler) Ensure(ctx context.Context, req jiraPollRequest) ([]ComponentAction, error) {
	client, owner, repo := req.Client, req.Owner, req.Repo
	spec := jiraPollScheduleSpec

	repoInfo, err := client.GetRepo(ctx, owner, repo)
	if err != nil {
		return nil, safeAPIError("reading the default branch for the Jira poll schedule", err)
	}
	defaultBranch := repoInfo.DefaultBranch
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	if reason, err := gitlabJiraPollDeferral(ctx, client, owner, repo, defaultBranch); err != nil {
		return nil, err
	} else if reason != "" {
		return []ComponentAction{{Component: jiraPollComponent, Action: "none", Detail: reason}}, nil
	}

	existing, err := listJiraPollSchedules(ctx, client, owner, repo)
	if err != nil {
		return nil, err
	}

	var keep *forge.PipelineSchedule
	var stale []forge.PipelineSchedule
	for i := range existing {
		s := existing[i]
		if keep == nil {
			inSync, err := jiraPollScheduleInSync(ctx, client, owner, repo, s, defaultBranch)
			if err != nil {
				return nil, err
			}
			if inSync {
				keep = &existing[i]
				continue
			}
		}
		stale = append(stale, s)
	}

	var actions []ComponentAction
	if keep == nil {
		// A replacement keeps the state of the schedule it replaces, so a
		// schedule an operator disabled stays disabled.
		active := true
		action, verb := "add", "create"
		if len(existing) > 0 {
			active = existing[0].Active
			action, verb = "update", "replace the out-of-date"
		}
		if req.DryRun {
			actions = append(actions, ComponentAction{
				Component: jiraPollComponent,
				Action:    action,
				Detail:    fmt.Sprintf("would %s pipeline schedule %q", verb, spec.Description),
			})
		} else {
			// Create the replacement before deleting the original, so an
			// interruption leaves a duplicate (removed on the next run)
			// rather than no Jira poll schedule at all.
			if _, err := client.CreatePipelineScheduleWithActive(ctx, owner, repo, defaultBranch, spec.Description, spec.Cron, nil, active); err != nil {
				return actions, safeAPIError(fmt.Sprintf("creating pipeline schedule %q", spec.Description), err)
			}
			detail := fmt.Sprintf("created pipeline schedule %q", spec.Description)
			if len(existing) > 0 {
				detail = fmt.Sprintf("replaced the out-of-date pipeline schedule %q", spec.Description)
			}
			actions = append(actions, ComponentAction{Component: jiraPollComponent, Action: action, Detail: detail})
		}
	}

	if keep != nil && len(stale) > 0 {
		verb := "removed"
		if req.DryRun {
			verb = "would remove"
		}
		actions = append(actions, ComponentAction{
			Component: jiraPollComponent,
			Action:    "update",
			Detail:    fmt.Sprintf("%s %d duplicate pipeline schedule(s) %q", verb, len(stale), spec.Description),
		})
	}
	if !req.DryRun {
		for _, s := range stale {
			if err := client.DeletePipelineSchedule(ctx, owner, repo, s.ID); err != nil {
				return actions, safeAPIError(fmt.Sprintf("deleting pipeline schedule %d (%q)", s.ID, spec.Description), err)
			}
		}
	}

	if keep != nil && len(stale) == 0 {
		detail := fmt.Sprintf("pipeline schedule %q exists", spec.Description)
		if !keep.Active {
			detail = fmt.Sprintf("pipeline schedule %q exists but is disabled; not reactivating", spec.Description)
		}
		actions = append(actions, ComponentAction{Component: jiraPollComponent, Action: "none", Detail: detail})
	}
	return actions, nil
}

// Remove deletes every Jira poll schedule. It never touches another
// schedule, the CI configuration, or the Jira credentials.
func (gitlabJiraPollReconciler) Remove(ctx context.Context, req jiraPollRequest) ([]ComponentAction, error) {
	spec := jiraPollScheduleSpec
	existing, err := listJiraPollSchedules(ctx, req.Client, req.Owner, req.Repo)
	if err != nil {
		return nil, err
	}
	if len(existing) == 0 {
		return nil, nil
	}
	if req.DryRun {
		return []ComponentAction{{
			Component: jiraPollComponent,
			Action:    "delete",
			Detail:    fmt.Sprintf("would remove pipeline schedule %q", spec.Description),
		}}, nil
	}
	for _, s := range existing {
		if err := req.Client.DeletePipelineSchedule(ctx, req.Owner, req.Repo, s.ID); err != nil {
			return nil, safeAPIError(fmt.Sprintf("deleting pipeline schedule %d (%q)", s.ID, spec.Description), err)
		}
	}
	return []ComponentAction{{
		Component: jiraPollComponent,
		Action:    "delete",
		Detail:    fmt.Sprintf("removed pipeline schedule %q", spec.Description),
	}}, nil
}

// listJiraPollSchedules returns the project's Jira poll schedules, oldest
// first.
func listJiraPollSchedules(ctx context.Context, client forge.Client, owner, repo string) ([]forge.PipelineSchedule, error) {
	all, err := client.ListPipelineSchedules(ctx, owner, repo)
	if err != nil {
		return nil, safeAPIError("listing pipeline schedules", err)
	}
	var matches []forge.PipelineSchedule
	for _, s := range all {
		if s.Description == jiraPollScheduleSpec.Description {
			matches = append(matches, s)
		}
	}
	slices.SortFunc(matches, compareScheduleID)
	return matches, nil
}

// jiraPollScheduleInSync reports whether s runs the Jira poll spec on the
// default branch without pipeline variables.
func jiraPollScheduleInSync(ctx context.Context, client forge.Client, owner, repo string, s forge.PipelineSchedule, defaultBranch string) (bool, error) {
	if !scheduleRefIsBranch(s.Ref, defaultBranch) || s.Cron != jiraPollScheduleSpec.Cron || s.CronTimezone != "UTC" {
		return false, nil
	}
	// The list endpoint omits schedule variables.
	detail, err := client.GetPipelineSchedule(ctx, owner, repo, s.ID)
	if err != nil {
		return false, safeAPIError("reading the Jira poll pipeline schedule", err)
	}
	if detail == nil || detail.ID != s.ID || detail.Description != s.Description {
		return false, fmt.Errorf("pipeline schedule %d changed while it was being verified", s.ID)
	}
	return len(detail.Variables) == 0, nil
}

// gitlabJiraPollDeferral returns why the Jira poll schedule cannot be
// created or repaired yet, or "" when it can. The committed poll template
// must select the Jira poll mode from the schedule identity and the
// committed job script must run the Jira poller; until a scaffold update
// carrying both is merged, a Jira schedule would start a pipeline with no
// Jira poll job. The committed root workflow and include rules, the wrapper
// include rules, the poll stage, and the poll job's own rules must also admit
// a pipeline carrying the Jira schedule description, the same chain verified
// for the managed polling schedules. Like those schedules
// (convergeSchedules), a credential-bearing schedule also waits for the
// pipeline-variable override restriction.
func gitlabJiraPollDeferral(ctx context.Context, client forge.Client, owner, repo, defaultBranch string) (string, error) {
	const pending = "deferring the Jira poll schedule until the poll template and job script that run the Jira poller land on the default branch; merge the scaffold update and rerun repos install"
	template, err := client.GetFileContent(ctx, owner, repo, fullsendPollTemplatePath)
	if forge.IsNotFound(err) {
		return pending, nil
	}
	if err != nil {
		return "", safeAPIError("reading "+fullsendPollTemplatePath, err)
	}
	if !bytes.Contains(template, fmt.Appendf(nil, "CI_PIPELINE_SCHEDULE_DESCRIPTION == %q", jiraPollScheduleSpec.Description)) {
		return pending, nil
	}
	script, err := client.GetFileContent(ctx, owner, repo, gitlabPollJobScriptPath)
	if forge.IsNotFound(err) {
		return pending, nil
	}
	if err != nil {
		return "", safeAPIError("reading "+gitlabPollJobScriptPath, err)
	}
	if !bytes.Contains(script, []byte("--input-driver jira-poll")) {
		return pending, nil
	}
	if problem := gitlabScheduledPollProblemFor(ctx, client, owner, repo, defaultBranch, []string{jiraPollScheduleSpec.Description}); problem != "" {
		return "deferring the Jira poll schedule: " + problem, nil
	}
	role, err := client.GetPipelineVariablesMinimumOverrideRole(ctx, owner, repo)
	if err != nil {
		return "", safeAPIError("reading the pipeline-variable override role", err)
	}
	if role != forge.PipelineVarOverrideNoOneAllowed {
		return fmt.Sprintf("deferring the Jira poll schedule until the pipeline-variable override restriction is established (ci_pipeline_variables_minimum_override_role is %s, not no_one_allowed); rerun repos install once it is", displayPipelineVarRole(role)), nil
	}
	return "", nil
}
