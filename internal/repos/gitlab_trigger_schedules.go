package repos

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// ---------------------------------------------------------------------------
// Trigger-account ownership of the managed polling schedules (#8508)
// ---------------------------------------------------------------------------
//
// A GitLab pipeline schedule creates every pipeline as its owner. While the
// installer owns the managed polling schedules (PipelineScheduleSpecs), a
// scheduled poll runs as the installer and dispatches through the Poller api
// route. Once the trigger-dispatch activation gate passes and the activation is
// recorded, the schedules are recreated under the trigger-only account, so a
// scheduled poll runs as that account and dispatches the agent through the
// CI_JOB_TOKEN child-pipeline route (source=pipeline).
//
// GitLab records the calling user as a schedule's owner, so the account must
// create its schedules itself. A short-lived bootstrap PAT is minted for that
// while the account stays at Developer. The activation gate first verifies
// that the account also has merge permission on the protected branch, which
// GitLab requires for scheduled pipelines; a deploy-key push grant does not
// provide it. Otherwise the schedules remain installer-owned. The PAT reuses
// the ledger's bootstrap-PAT
// obligation (pat_pending, pat_id), so an interrupted run is contained by
// containBootstrap exactly like an interrupted bootstrap, and the ledger gains
// no field an older CLI would reject.
//
// Every move creates the replacement before deleting the original, so an
// interruption leaves a duplicate (removed on the next run) rather than no
// polling schedule at all:
//
//   - adoption: a managed schedule owned by anyone else is replaced by an
//     account-owned one on the protected branch, keeping its active state. A
//     disabled replacement is created disabled (the create request carries
//     active=false), so it is never active; while the original survives it is
//     still the record of the intended state, and a replacement that does not
//     match it is replaced on the next run;
//   - drift: an account-owned managed schedule on another ref, with another
//     cron or timezone, or with pipeline variables is replaced the same way;
//   - hand-back: whenever the account may not own the schedules (the gate or
//     the account deferred, the account is about to be raised to Maintainer,
//     or its generation is retired by rotation or repair), every account-owned
//     managed schedule is replaced by an installer-owned copy, because a
//     schedule fires on the server's clock and its poll job would run as the
//     account without a usable dispatch route or with Maintainer access;
//   - uninstall deletes the account-owned managed schedules without a copy,
//     because uninstall removes every fullsend schedule.
//
// A schedule the account owns whose description is not a managed one is
// never adopted, repaired or removed: checkForeignResources fails closed on
// it.

// GitLabTriggerSchedulesResult reports what one schedule-ownership run did.
type GitLabTriggerSchedulesResult struct {
	// Created lists the schedules created in this run, owned by the trigger
	// account (ConvergeGitLabTriggerSchedules) or by the installer (hand-back).
	Created []int64
	// Removed lists the schedules deleted in this run.
	Removed []int64
}

// errGitLabTriggerDispatchInactive refuses to move the schedules while no
// activation bound to the current ledger generation is recorded.
var errGitLabTriggerDispatchInactive = errors.New("GitLab trigger account: the managed polling schedules move to the trigger account only after the dispatch activation is recorded for the current generation")

// ReconcileGitLabTriggerScheduleOwnership applies the ownership decision for
// the managed polling schedules after an install pass. With dispatchActive set
// (the activation gate passed and SetGitLabTriggerDispatchActive recorded the
// activation) the schedules move to the trigger account; otherwise every
// account-owned managed schedule is handed back to the installer.
func ReconcileGitLabTriggerScheduleOwnership(ctx context.Context, opts GitLabTriggerAccountOptions, dispatchActive bool) (GitLabTriggerSchedulesResult, error) {
	if dispatchActive {
		return ConvergeGitLabTriggerSchedules(ctx, opts)
	}
	return ReleaseGitLabTriggerSchedules(ctx, opts)
}

// ConvergeGitLabTriggerSchedules recreates the managed polling schedules under
// the trigger account. It runs only on a ready, published ledger whose
// dispatch activation is recorded for the current generation, and it never
// raises the account above Developer.
//
// A spec with no live schedule at all is left alone: convergeSchedules owns
// that creation decision (it waits for compatible templates), and the next
// install moves the schedule it creates.
func ConvergeGitLabTriggerSchedules(ctx context.Context, opts GitLabTriggerAccountOptions) (GitLabTriggerSchedulesResult, error) {
	var res GitLabTriggerSchedulesResult
	if err := requireGitLabTriggerLease(opts); err != nil {
		return res, err
	}
	if opts.Bootstrap == nil {
		return res, errors.New("GitLab trigger account: a bootstrap client factory is required to create the account's schedules")
	}
	op, err := newGitLabTriggerAccountOp(ctx, opts)
	if err != nil {
		return res, err
	}
	if !op.exists || op.st.Phase != gitLabTriggerPhaseReady || op.st.AccountID <= 0 || !op.st.TriggerPublished {
		return res, errors.New("GitLab trigger account: the managed polling schedules move only to a ready trigger account with a published trigger token")
	}
	if op.st.Branch != opts.Branch {
		return res, errors.New(branchChangeReason(op.st.Branch, opts.Branch))
	}
	if slices.Contains(opts.PollerUserIDs, op.st.AccountID) {
		return res, fmt.Errorf("GitLab trigger account %d is also a Poller role identity; the trigger identity must stay separate from the Poller credential", op.st.AccountID)
	}
	active, err := op.dispatchActivationRecorded(ctx)
	if err != nil {
		return res, err
	}
	if !active {
		return res, errGitLabTriggerDispatchInactive
	}
	// A bootstrap PAT left by an interrupted run is revoked before anything
	// else, so the foreign-PAT check below sees only foreign tokens.
	if err := op.containBootstrap(ctx); err != nil {
		return res, err
	}
	if _, found, err := op.findOwnedAccount(ctx); err != nil {
		return res, err
	} else if !found {
		return res, fmt.Errorf("recorded GitLab trigger account %d no longer exists; re-run install to repair it", op.st.AccountID)
	}
	if err := op.checkForeignPATs(ctx); err != nil {
		return res, err
	}
	if err := op.checkForeignResources(ctx); err != nil {
		return res, err
	}
	if err := op.ensureMembership(ctx, forge.GitLabAccessLevelDeveloper); err != nil {
		return res, err
	}
	creates, removals, removalTargets, err := op.planAccountSchedules(ctx)
	if err != nil {
		return res, err
	}
	if len(creates) > 0 {
		created, err := op.createAccountSchedules(ctx, creates)
		res.Created = created
		if err != nil {
			// The originals are kept: nothing is deleted unless every
			// replacement exists and is owned by the account.
			return res, err
		}
	}
	// removals keeps the order planAccountSchedules chose for each spec. A global
	// re-sort by ID would delete a newer schedule that holds the intended active
	// state before an older one that does not.
	removed, err := op.deleteSchedules(ctx, removals, removalTargets)
	res.Removed = removed
	return res, err
}

// ReleaseGitLabTriggerSchedules hands every account-owned managed schedule of
// the current and retired generations back to the installer. It needs no
// Poller inventory and creates nothing under the trigger account. It is a
// no-op when no ledger exists or no account owns a managed schedule.
func ReleaseGitLabTriggerSchedules(ctx context.Context, opts GitLabTriggerAccountOptions) (GitLabTriggerSchedulesResult, error) {
	var res GitLabTriggerSchedulesResult
	if err := requireGitLabTriggerLease(opts); err != nil {
		return res, err
	}
	if opts.API == nil || opts.State == nil {
		return res, errors.New("GitLab trigger account: API and state store are required")
	}
	op, err := loadGitLabTriggerAccountOp(ctx, opts)
	if err != nil {
		return res, err
	}
	if !op.exists {
		return res, nil
	}
	var errs []error
	for _, gen := range op.generations() {
		r, err := gen.releaseOwnedSchedules(ctx, nil)
		res.Created = append(res.Created, r.Created...)
		res.Removed = append(res.Removed, r.Removed...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return res, errors.Join(errs...)
}

// generations returns the working copy of the ledger's current generation
// followed by every retired one.
func (op *gitLabTriggerAccountOp) generations() []*gitLabTriggerAccountOp {
	ids := map[int64]bool{int64(op.st.AccountID): true}
	for _, old := range op.st.Retired {
		ids[int64(old.AccountID)] = true
	}
	op.generationIDs = ids
	gens := []*gitLabTriggerAccountOp{op}
	for i := range op.st.Retired {
		gens = append(gens, &gitLabTriggerAccountOp{opts: op.opts, st: op.st.Retired[i], exists: true, retired: true, generationIDs: ids})
	}
	return gens
}

func (op *gitLabTriggerAccountOp) recordedGenerationOwner(id int64) bool {
	if id <= 0 {
		return false
	}
	if id == int64(op.st.AccountID) || op.generationIDs[id] {
		return true
	}
	for _, old := range op.st.Retired {
		if id == int64(old.AccountID) {
			return true
		}
	}
	return false
}

// installerScheduleOwner accepts the current installer and another current
// project Maintainer who could have installed these exact-name managed
// schedules before a credential change. Unknown owners, Developers, and
// trigger generations cannot supply an installer fallback copy.
func (op *gitLabTriggerAccountOp) installerScheduleOwner(ctx context.Context, id, installerID int64) (bool, error) {
	if id <= 0 || op.recordedGenerationOwner(id) {
		return false, nil
	}
	if id == installerID {
		return true, nil
	}
	level, err := op.opts.API.GetProjectMemberAccessLevel(ctx, op.opts.Owner, op.opts.Repo, id)
	if forge.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, safeAPIError("verifying a prior installer schedule owner", err)
	}
	return level >= forge.GitLabAccessLevelMaintainer, nil
}

// releaseOwnedSchedules hands the managed schedules of one generation's
// recorded, nonce-named account back to the installer. When needsRelease is
// set it is first asked, for the generation's account-owned managed schedules,
// whether to release at all. A generation without an account, or whose account
// is not found, is skipped.
func (op *gitLabTriggerAccountOp) releaseOwnedSchedules(ctx context.Context, needsRelease func(owned []forge.PipelineSchedule) (bool, error)) (GitLabTriggerSchedulesResult, error) {
	if op.st.AccountID == 0 {
		return GitLabTriggerSchedulesResult{}, nil
	}
	// Only the recorded, nonce-named account's schedules are touched.
	if _, found, err := op.findOwnedAccount(ctx); err != nil || !found {
		return GitLabTriggerSchedulesResult{}, err
	}
	if needsRelease != nil {
		owned, _, _, err := op.accountSchedules(ctx)
		if err != nil || len(owned) == 0 {
			return GitLabTriggerSchedulesResult{}, err
		}
		release, err := needsRelease(owned)
		if err != nil || !release {
			return GitLabTriggerSchedulesResult{}, err
		}
	}
	return op.releaseSchedules(ctx, true)
}

// RepairGitLabTriggerSchedules hands managed polling schedules the trigger
// account owns back to the installer when any of them carries pipeline
// variables. Install runs it right after early containment, before
// convergence: the pipeline-variable migration preflight that convergence
// reaches reads every managed schedule whatever its owner and rejects
// variables the installer cannot remove from an account-owned one. Containment
// has already withdrawn the dispatch activation, so the account may not keep
// the schedules; the ownership stage that follows the install adopts them
// again only once the activation is recorded. It does not depend on the
// current generation being ready or published: every ownership-verified
// generation, current and retired, is inspected, so a containment-cleared
// publication or an interrupted rotation does not hide a blocking schedule.
// It does nothing, and returns no error, when no ledger exists or no
// generation's account owns a managed schedule that carries variables.
func RepairGitLabTriggerSchedules(ctx context.Context, opts GitLabTriggerAccountOptions) (GitLabTriggerSchedulesResult, error) {
	var res GitLabTriggerSchedulesResult
	if err := requireGitLabTriggerLease(opts); err != nil {
		return res, err
	}
	if opts.API == nil || opts.State == nil {
		return res, errors.New("GitLab trigger account: API and state store are required")
	}
	op, err := loadGitLabTriggerAccountOp(ctx, opts)
	if err != nil {
		return res, err
	}
	if !op.exists {
		return res, nil
	}
	var errs []error
	for _, gen := range op.generations() {
		r, err := gen.releaseOwnedSchedules(ctx, func(owned []forge.PipelineSchedule) (bool, error) {
			for _, s := range owned {
				clean, err := gen.scheduleHasNoVariables(ctx, s)
				if err != nil || !clean {
					return !clean, err
				}
			}
			return false, nil
		})
		res.Created = append(res.Created, r.Created...)
		res.Removed = append(res.Removed, r.Removed...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return res, errors.Join(errs...)
}

// dispatchActivationRecorded reports whether the recorded dispatch activation
// is bound to the working copy of the ledger.
func (op *gitLabTriggerAccountOp) dispatchActivationRecorded(ctx context.Context) (bool, error) {
	raw, exists, err := op.opts.State.GetRepoVariable(ctx, op.opts.Owner, op.opts.Repo, forge.VarGitLabDispatchActivation)
	if err != nil {
		return false, safeAPIError("reading "+forge.VarGitLabDispatchActivation, err)
	}
	if !exists {
		return false, nil
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var rec gitLabDispatchActivation
	if err := dec.Decode(&rec); err != nil {
		return false, nil
	}
	// The record is one JSON object: trailing content or a repeated key means
	// it was not written by SetGitLabTriggerDispatchActive.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return false, nil
	}
	if rejectDuplicateJSONKeys(raw) != nil {
		return false, nil
	}
	return rec == op.activationFor(), nil
}

// accountSchedules returns the account's managed schedules and the IDs of the
// schedules it owns that are not managed, plus the project's full schedule
// list (empty when the account owns none). A schedule is managed when its
// description is one of PipelineScheduleSpecs. Only holders of the account's
// credentials can make it a schedule's owner, and fullsend holds the only one
// (checkForeignPATs fails closed on any other).
func (op *gitLabTriggerAccountOp) accountSchedules(ctx context.Context) (managed, all []forge.PipelineSchedule, foreign []int, err error) {
	o, r := op.opts.Owner, op.opts.Repo
	ids, err := op.opts.API.AccountScheduleIDs(ctx, o, r, op.st.AccountID)
	if err != nil {
		return nil, nil, nil, safeAPIError("listing the GitLab trigger account's pipeline schedules", err)
	}
	if len(ids) == 0 {
		return nil, nil, nil, nil
	}
	all, err = op.opts.State.ListPipelineSchedules(ctx, o, r)
	if err != nil {
		return nil, nil, nil, safeAPIError("listing pipeline schedules", err)
	}
	for _, id := range ids {
		idx := slices.IndexFunc(all, func(s forge.PipelineSchedule) bool { return s.ID == int64(id) })
		if idx >= 0 && all[idx].OwnerID != int64(op.st.AccountID) {
			return nil, nil, nil, fmt.Errorf("pipeline schedule %d changed owner while trigger-account ownership was being verified", id)
		}
		if idx < 0 || !isManagedPipelineSchedule(all[idx].Description) {
			foreign = append(foreign, id)
			continue
		}
		managed = append(managed, all[idx])
	}
	slices.SortFunc(managed, compareScheduleID)
	return managed, all, foreign, nil
}

// compareScheduleID orders schedules oldest first.
func compareScheduleID(a, b forge.PipelineSchedule) int {
	return cmp.Compare(a.ID, b.ID)
}

// accountScheduleCreate is one schedule to create under the trigger account.
type accountScheduleCreate struct {
	spec   ScheduleSpec
	active bool
}

// planAccountSchedules decides, per managed spec, which schedule the account
// keeps, which it must create, and which schedules are deleted once every
// creation succeeded.
func (op *gitLabTriggerAccountOp) planAccountSchedules(ctx context.Context) ([]accountScheduleCreate, []int64, map[int64]scheduleDeleteTarget, error) {
	owned, all, _, err := op.accountSchedules(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(owned) == 0 {
		// accountSchedules lists the project only when the account owns a
		// schedule.
		if all, err = op.opts.State.ListPipelineSchedules(ctx, op.opts.Owner, op.opts.Repo); err != nil {
			return nil, nil, nil, safeAPIError("listing pipeline schedules", err)
		}
	}
	installerID, err := op.opts.API.GetAuthenticatedUserID(ctx)
	if err != nil {
		return nil, nil, nil, safeAPIError("verifying the installer schedule owner", err)
	}
	if installerID <= 0 {
		return nil, nil, nil, fmt.Errorf("installer schedule owner has no usable user ID")
	}
	ownedIDs := map[int64]bool{}
	for _, s := range owned {
		ownedIDs[s.ID] = true
	}
	var creates []accountScheduleCreate
	var removals []int64
	removalTargets := map[int64]scheduleDeleteTarget{}
	for _, spec := range PipelineScheduleSpecs() {
		var mine, others []forge.PipelineSchedule
		for _, s := range all {
			if s.Description != spec.Description {
				continue
			}
			if ownedIDs[s.ID] {
				mine = append(mine, s)
			} else {
				if op.recordedGenerationOwner(s.OwnerID) && s.OwnerID != int64(op.st.AccountID) {
					continue
				}
				trusted, err := op.installerScheduleOwner(ctx, s.OwnerID, int64(installerID))
				if err != nil {
					return nil, nil, nil, err
				}
				if !trusted {
					return nil, nil, nil, fmt.Errorf("managed schedule %d (%q) is not owned by a verified installer Maintainer; refusing adoption", s.ID, s.Description)
				}
				others = append(others, s)
			}
		}
		slices.SortFunc(mine, compareScheduleID)
		slices.SortFunc(others, compareScheduleID)
		// The intended active state of the account's schedule; see
		// intendedScheduleActive.
		wantActive := intendedScheduleActive(mine, others, op.opts.ReactivateSchedules)
		kept := false
		var stale []forge.PipelineSchedule
		for _, s := range mine {
			if !kept {
				inSync, err := op.accountScheduleInSync(ctx, s, spec)
				if err != nil {
					return nil, nil, nil, err
				}
				if inSync && s.Active != wantActive {
					inSync = false
				}
				if inSync {
					kept = true
					continue
				}
			}
			stale = append(stale, s)
		}
		stale = append(stale, others...)
		// A deletion cut short must leave schedules from which a retry derives
		// the same intended state (see intendedScheduleActive). Schedules that
		// disagree with it go first, so the ones that hold it outlive them
		// whatever their owner; within each group the newest goes first, so the
		// oldest record outlives the rest.
		slices.SortFunc(stale, func(a, b forge.PipelineSchedule) int {
			if da, db := a.Active != wantActive, b.Active != wantActive; da != db {
				if da {
					return -1
				}
				return 1
			}
			return cmp.Compare(b.ID, a.ID)
		})
		for _, s := range stale {
			removals = append(removals, s.ID)
			removalTargets[s.ID] = scheduleDeleteTarget{ownerID: s.OwnerID, description: s.Description}
		}
		if kept {
			continue
		}
		// The replacement keeps the state of the schedule it replaces: an
		// operator who disabled scheduled polling keeps it disabled, unless
		// --reactivate-schedules asked for it to run again.
		if len(mine) > 0 || len(others) > 0 {
			creates = append(creates, accountScheduleCreate{spec: spec, active: wantActive})
		}
	}
	return creates, removals, removalTargets, nil
}

// intendedScheduleActive derives the active state a managed schedule must keep
// from the schedules with its description, split by owner. The oldest of them
// is the record: a replacement is created with the intended active state, but
// a GitLab that ignores the create request's active attribute would leave a
// disabled original's replacement active, looking in sync but not being so.
// An installer-owned schedule created after the account's first one is a
// hand-back copy. An active copy says nothing, but a disabled copy is only
// created when polling was meant to be disabled. It therefore disables the
// result even when the record that asked for that has since been deleted by an
// interrupted cleanup. reactivate (--reactivate-schedules) overrides all of it; with no
// schedule at all the state is disabled.
func intendedScheduleActive(account, installer []forge.PipelineSchedule, reactivate bool) bool {
	if reactivate {
		return true
	}
	var oldest *forge.PipelineSchedule
	firstAccountID := int64(math.MaxInt64)
	for i := range account {
		firstAccountID = min(firstAccountID, account[i].ID)
		if oldest == nil || account[i].ID < oldest.ID {
			oldest = &account[i]
		}
	}
	for i := range installer {
		if oldest == nil || installer[i].ID < oldest.ID {
			oldest = &installer[i]
		}
	}
	if oldest == nil || !oldest.Active {
		return false
	}
	for _, c := range installer {
		if c.ID > firstAccountID && !c.Active {
			return false
		}
	}
	return true
}

// scheduleRefIsBranch reports whether a schedule's ref names branch, whether
// GitLab reports it as the branch name or in its refs/heads/ form.
func scheduleRefIsBranch(ref, branch string) bool {
	return ref == branch || ref == "refs/heads/"+branch
}

// accountScheduleInSync reports whether an account-owned managed schedule
// runs the spec on the recorded branch without pipeline variables.
func (op *gitLabTriggerAccountOp) accountScheduleInSync(ctx context.Context, s forge.PipelineSchedule, spec ScheduleSpec) (bool, error) {
	if !scheduleRefIsBranch(s.Ref, op.st.Branch) || s.Cron != spec.Cron || s.CronTimezone != "UTC" {
		return false, nil
	}
	if op.opts.ReactivateSchedules && !s.Active {
		// --reactivate-schedules asks for a disabled schedule to run again.
		return false, nil
	}
	return op.scheduleHasNoVariables(ctx, s)
}

// scheduleHasNoVariables reads the schedule's details, which the list endpoint
// omits, and reports whether it carries no pipeline variables.
func (op *gitLabTriggerAccountOp) scheduleHasNoVariables(ctx context.Context, s forge.PipelineSchedule) (bool, error) {
	detail, err := op.opts.State.GetPipelineSchedule(ctx, op.opts.Owner, op.opts.Repo, s.ID)
	if err != nil {
		return false, safeAPIError("reading the pipeline schedule", err)
	}
	if detail == nil || detail.ID != s.ID || detail.Description != s.Description {
		return false, fmt.Errorf("pipeline schedule %d changed while it was being verified", s.ID)
	}
	return len(detail.Variables) == 0, nil
}

// handBackCopyMatches reports whether an installer-owned schedule is a
// complete hand-back copy of the account-owned schedule from: the recorded
// branch (not the account-owned schedule's own ref, which may have drifted),
// the managed spec's UTC cron, the intended active state, and no pipeline variables.
func (op *gitLabTriggerAccountOp) handBackCopyMatches(ctx context.Context, c forge.PipelineSchedule, spec ScheduleSpec, wantActive bool) (bool, error) {
	if !scheduleRefIsBranch(c.Ref, op.st.Branch) || c.Cron != spec.Cron || c.CronTimezone != "UTC" || c.Active != wantActive {
		return false, nil
	}
	return op.scheduleHasNoVariables(ctx, c)
}

// verifyCreatedSchedule checks the server's stored schedule before its
// predecessor can be deleted. A successful create response does not prove that
// GitLab honored active=false or even returned a usable schedule ID.
func (op *gitLabTriggerAccountOp) verifyCreatedSchedule(ctx context.Context, id int64, spec ScheduleSpec, active bool, ownerID int64) error {
	if id <= 0 {
		return fmt.Errorf("creating the %q schedule: response carried no schedule ID", spec.Description)
	}
	detail, err := op.opts.State.GetPipelineSchedule(ctx, op.opts.Owner, op.opts.Repo, id)
	if err != nil {
		return safeAPIError("verifying the created "+spec.Description+" schedule", err)
	}
	if detail == nil || detail.ID != id || detail.OwnerID != ownerID || detail.Description != spec.Description ||
		!scheduleRefIsBranch(detail.Ref, op.st.Branch) || detail.Cron != spec.Cron ||
		detail.CronTimezone != "UTC" || detail.Active != active || len(detail.Variables) != 0 {
		return fmt.Errorf("created %q schedule %d does not match its required owner, ref, cron, timezone, active state, or variables; the original is kept", spec.Description, id)
	}
	return nil
}

// createAccountSchedules creates the planned schedules with a short-lived
// bootstrap PAT while the account stays at Developer, then always revokes the
// PAT and confirms Developer access (endBootstrap). Every created schedule
// must be reported as owned by the account; otherwise the run fails and the
// originals are kept.
func (op *gitLabTriggerAccountOp) createAccountSchedules(ctx context.Context, creates []accountScheduleCreate) ([]int64, error) {
	created, err := op.createWithSchedulePAT(ctx, creates)
	cleanup := func(cause error) ([]int64, error) {
		if len(created) == 0 {
			return created, cause
		}
		cctx, cancel := gitlabCleanupContext(ctx)
		defer cancel()
		targets := make(map[int64]scheduleDeleteTarget, len(created))
		for i, id := range created {
			targets[id] = scheduleDeleteTarget{ownerID: int64(op.st.AccountID), description: creates[i].spec.Description}
		}
		_, cleanupErr := op.deleteSchedules(cctx, created, targets)
		return created, errors.Join(cause, cleanupErr)
	}
	if cleanupErr := op.endBootstrap(ctx); cleanupErr != nil {
		return cleanup(errors.Join(err, cleanupErr))
	}
	if err != nil {
		return cleanup(err)
	}
	ids, err := op.opts.API.AccountScheduleIDs(ctx, op.opts.Owner, op.opts.Repo, op.st.AccountID)
	if err != nil {
		return cleanup(safeAPIError("confirming the GitLab trigger account's pipeline schedules", err))
	}
	for i, id := range created {
		if !slices.Contains(ids, int(id)) {
			return cleanup(fmt.Errorf("pipeline schedule %d is not reported as owned by GitLab trigger account %d; the installer-owned schedules are kept", id, op.st.AccountID))
		}
		if err := op.verifyCreatedSchedule(ctx, id, creates[i].spec, creates[i].active, int64(op.st.AccountID)); err != nil {
			return cleanup(err)
		}
	}
	return created, nil
}

func (op *gitLabTriggerAccountOp) createWithSchedulePAT(ctx context.Context, creates []accountScheduleCreate) ([]int64, error) {
	o, r := op.opts.Owner, op.opts.Repo
	// Record the obligation before the create call, as bootstrap does: a lost
	// response leaves an active PAT whose ID is unknown.
	op.st.PATPending = true
	if err := op.save(ctx); err != nil {
		return nil, err
	}
	expires := op.opts.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")
	pat, err := op.opts.API.CreateServiceAccountPAT(ctx, o, r, op.st.AccountID, op.st.patName(), gitLabTriggerBootstrapPATScopes, expires)
	if err != nil {
		return nil, safeAPIError("creating the GitLab trigger schedule token", err)
	}
	if pat == nil || pat.ID <= 0 || pat.Token == "" {
		return nil, errors.New("creating the GitLab trigger schedule token: response carried no token")
	}
	op.st.PATID = pat.ID
	if err := op.save(ctx); err != nil {
		return nil, err
	}
	client, err := op.opts.Bootstrap(pat.Token)
	if err != nil {
		return nil, safeAPIError("authenticating as the GitLab trigger account", err)
	}
	var created []int64
	for _, c := range creates {
		// A disabled replacement is created disabled: GitLab lets only the
		// schedule's owner edit it, and a separate disable after an active
		// create could be lost or come too late to stop the first run.
		id, err := client.CreatePipelineSchedule(ctx, o, r, "refs/heads/"+op.st.Branch, c.spec.Description, c.spec.Cron, c.active)
		if err != nil {
			return created, safeAPIError("creating the "+c.spec.Description+" schedule as the GitLab trigger account", err)
		}
		if id <= 0 {
			return created, fmt.Errorf("creating the %q schedule as the GitLab trigger account: response carried no schedule ID", c.spec.Description)
		}
		created = append(created, id)
	}
	return created, nil
}

// releaseSchedules replaces every managed schedule the account owns. With
// recreate set, an installer-owned copy (same description and cron, the
// recorded branch, the intended active state, no pipeline variables) is
// created first unless an installer-owned schedule with that description
// already is such a copy. Exactly one such copy is kept; every other
// installer-owned schedule with that description is deleted once the copy
// exists. Without recreate the account's schedules are only deleted.
// Schedules the account owns that are not managed are left for
// checkForeignResources to refuse.
func (op *gitLabTriggerAccountOp) releaseSchedules(ctx context.Context, recreate bool) (GitLabTriggerSchedulesResult, error) {
	var res GitLabTriggerSchedulesResult
	if op.st.AccountID == 0 {
		return res, nil
	}
	owned, all, _, err := op.accountSchedules(ctx)
	if err != nil || len(owned) == 0 {
		return res, err
	}
	var installerID int
	if recreate {
		installerID, err = op.opts.API.GetAuthenticatedUserID(ctx)
		if err != nil {
			return res, safeAPIError("verifying the installer schedule owner", err)
		}
		if installerID <= 0 {
			return res, fmt.Errorf("installer schedule owner has no usable user ID")
		}
	}
	o, r := op.opts.Owner, op.opts.Repo
	ownedIDs := map[int64]bool{}
	for _, s := range owned {
		ownedIDs[s.ID] = true
	}
	installerOwned := map[string][]forge.PipelineSchedule{}
	for _, s := range all {
		if recreate && !ownedIDs[s.ID] && isManagedPipelineSchedule(s.Description) {
			if op.recordedGenerationOwner(s.OwnerID) {
				continue
			}
			trusted, err := op.installerScheduleOwner(ctx, s.OwnerID, int64(installerID))
			if err != nil {
				return res, err
			}
			if !trusted {
				return res, fmt.Errorf("managed schedule %d (%q) is not owned by a verified installer Maintainer; refusing hand-back", s.ID, s.Description)
			}
			installerOwned[s.Description] = append(installerOwned[s.Description], s)
		}
	}
	for _, list := range installerOwned {
		slices.SortFunc(list, compareScheduleID)
	}
	covered := map[string]bool{}
	// Installer-owned schedules and the account's own are deleted in separate
	// groups; see the ordering note before the deletion below.
	var staleInstaller, accountRemovals []int64
	removalTargets := map[int64]scheduleDeleteTarget{}
	for _, s := range owned {
		var spec ScheduleSpec
		for _, candidate := range PipelineScheduleSpecs() {
			if candidate.Description == s.Description {
				spec = candidate
				break
			}
		}
		if spec.Description == "" {
			return res, fmt.Errorf("the account owns unrecognized schedule %q; refusing to replace it", s.Description)
		}
		// After an interrupted adoption the record of the intended state is the
		// installer-owned original, which the account's replacement must not
		// override even if GitLab ignored its active attribute; after an
		// interrupted hand-back it is the account's own schedule, and a disabled
		// installer copy keeps it disabled; see intendedScheduleActive.
		var account []forge.PipelineSchedule
		for _, a := range owned {
			if a.Description == s.Description {
				account = append(account, a)
			}
		}
		wantActive := intendedScheduleActive(account, installerOwned[s.Description], op.opts.ReactivateSchedules)
		if recreate && !covered[s.Description] {
			// An installer-owned schedule with the description covers the
			// account's only when it is a complete copy. One left by an
			// interrupted move may be disabled, on another ref or cron, or carry
			// variables; the account's schedule is the record of what the copy
			// must be. A copy that differs is replaced, and is deleted with the
			// account's schedules, so only after every schedule has a copy.
			for _, c := range installerOwned[s.Description] {
				ok, err := op.handBackCopyMatches(ctx, c, spec, wantActive)
				if err != nil {
					return res, err
				}
				switch {
				case ok && !covered[s.Description]:
					covered[s.Description] = true
				default:
					// A mismatched copy, or a further complete one: only the
					// oldest complete copy is kept, because a later run returns
					// early once the account owns nothing and would never
					// remove the duplicate.
					staleInstaller = append(staleInstaller, c.ID)
					removalTargets[c.ID] = scheduleDeleteTarget{ownerID: c.OwnerID, description: c.Description}
				}
			}
		}
		if recreate && !covered[s.Description] {
			// The copy runs on the recorded branch, never on a ref the
			// account-owned schedule drifted to: the installer must not promote
			// a schedule the Developer-level account pointed elsewhere.
			// A disabled copy is created disabled; see createWithSchedulePAT.
			id, err := op.opts.State.CreatePipelineScheduleWithActive(ctx, o, r, "refs/heads/"+op.st.Branch, s.Description, spec.Cron, nil, wantActive)
			if err != nil {
				return res, safeAPIError("handing the "+s.Description+" schedule back to the installer", err)
			}
			if err := op.verifyCreatedSchedule(ctx, id, spec, wantActive, int64(installerID)); err != nil {
				if id > 0 {
					cctx, cancel := gitlabCleanupContext(ctx)
					_, cleanupErr := op.deleteSchedules(cctx, []int64{id}, map[int64]scheduleDeleteTarget{id: {ownerID: int64(installerID), description: s.Description}})
					cancel()
					return res, errors.Join(err, cleanupErr)
				}
				return res, err
			}
			res.Created = append(res.Created, id)
			covered[s.Description] = true
		}
		accountRemovals = append(accountRemovals, s.ID)
		removalTargets[s.ID] = scheduleDeleteTarget{ownerID: s.OwnerID, description: s.Description}
	}
	// Order per owner, newest first within each. Stale installer-owned
	// schedules go before the account's: a retry finds them only while the
	// account still owns a schedule (releaseSchedules returns early
	// otherwise), so deleting the account's last schedule first could strand
	// one. Within a group the oldest schedule, which records the intended
	// active state, is deleted last, so a deletion cut short leaves it behind
	// and a retry derives the same state.
	newestFirst := func(a, b int64) int { return cmp.Compare(b, a) }
	slices.SortFunc(staleInstaller, newestFirst)
	slices.SortFunc(accountRemovals, newestFirst)
	removed, err := op.deleteSchedules(ctx, append(staleInstaller, accountRemovals...), removalTargets)
	res.Removed = removed
	return res, err
}

type scheduleDeleteTarget struct {
	ownerID     int64
	description string
}

// deleteSchedules verifies the reported owner immediately before each
// deletion. GitLab has no conditional schedule delete, so an ownership change
// after this read remains an API-level race. A schedule already gone counts as
// deleted.
func (op *gitLabTriggerAccountOp) deleteSchedules(ctx context.Context, ids []int64, targets map[int64]scheduleDeleteTarget) ([]int64, error) {
	var removed []int64
	for _, id := range ids {
		target := targets[id]
		if target.ownerID <= 0 || !isManagedPipelineSchedule(target.description) {
			return removed, fmt.Errorf("cannot delete pipeline schedule %d without a verified owner and managed description", id)
		}
		detail, err := op.opts.State.GetPipelineSchedule(ctx, op.opts.Owner, op.opts.Repo, id)
		if forge.IsNotFound(err) {
			removed = append(removed, id)
			continue
		}
		if err != nil {
			return removed, safeAPIError(fmt.Sprintf("verifying pipeline schedule %d before deletion", id), err)
		}
		if detail == nil || detail.ID != id || detail.OwnerID != target.ownerID || detail.Description != target.description {
			return removed, fmt.Errorf("pipeline schedule %d changed owner or description before deletion; refusing to remove it", id)
		}
		if err := op.opts.State.DeletePipelineSchedule(ctx, op.opts.Owner, op.opts.Repo, id); err != nil && !forge.IsNotFound(err) {
			return removed, safeAPIError(fmt.Sprintf("deleting pipeline schedule %d", id), err)
		}
		removed = append(removed, id)
	}
	return removed, nil
}
