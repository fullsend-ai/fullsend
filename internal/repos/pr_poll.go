package repos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// prPollComponent is the ComponentAction component reported for the
// managed fork PR review poll caller (ADR 0171).
const prPollComponent = "pr-poll"

// prPollNowFn is the clock that stamps a new caller's since value.
// Override in tests to supply a deterministic timestamp.
var prPollNowFn = time.Now

// prPollState is a repository's fork PR review poll state, read during
// discovery before any write.
type prPollState struct {
	// enabled reports that the effective fork_pr_review_poll.enabled is
	// true: the managed caller must be present.
	enabled bool
}

// prPollRequest is the input to ensurePRPollCaller and removePRPollCaller.
// Unlike the Jira poll, the fork PR review poll needs no credentials: its
// poll job reads pull requests with GITHUB_TOKEN only.
type prPollRequest struct {
	Client forge.Client
	Owner  string
	Repo   string
	// DryRun asks for the actions that would be taken without any write.
	DryRun bool
	// Render pins the caller's reusable workflows to the ref the run's
	// scaffold uses. Only meaningful for ensurePRPollCaller.
	Render scaffold.RenderOptions
	// StageFile queues the caller's write or deletion for the run's
	// scaffold commit, so it is delivered the same way as the rest of the
	// scaffold (direct push or pull request). It is nil on a dry run.
	StageFile func(forge.TreeFile)
}

// effectiveForkPRReviewPoll reports whether the repository's effective
// fork_pr_review_poll.enabled is true: the managed configuration layered
// over the declared preset, the same layers committed as the .fullsend
// files (see effectiveJiraTracking).
func effectiveForkPRReviewPoll(resolved ResolvedConfig, preset []byte) (prPollState, error) {
	layered, err := config.LayerOnBase(resolved.Managed, preset)
	if err != nil {
		return prPollState{}, fmt.Errorf("%s/%s: reading the effective configuration: %w", resolved.Owner, resolved.Repo, err)
	}
	return prPollState{enabled: layered.ForkPRReviewPoll().PollEnabled()}, nil
}

// convergePRPoll converges the repository's managed fork PR review poll
// caller (scaffold.PRPollCallerPath) to its effective
// fork_pr_review_poll.enabled. Only GitHub manages the caller: on another
// forge an enabled poll is reported as not managed and a disabled one
// reports nothing. The caller is delivered through delivery, the same
// scaffold commit as the Jira poll caller. A dry run writes nothing.
func convergePRPoll(ctx context.Context, d convergeDiscovery, cfg ConvergeConfig, delivery jiraPollDelivery, progress ProgressFunc) []ComponentAction {
	resolved := d.resolved
	if resolved.Forge != ForgeGitHub {
		if !d.prPoll.enabled {
			return nil
		}
		detail := fmt.Sprintf("fork_pr_review_poll.enabled is true, but the fork PR review poll is not managed on %s", resolved.Forge)
		progress(resolved.Owner+"/"+resolved.Repo, prPollComponent, detail)
		return []ComponentAction{{
			Component: prPollComponent,
			Action:    "none",
			Detail:    detail,
		}}
	}
	req := prPollRequest{
		Client: resolved.ForgeConfig.Client,
		Owner:  resolved.Owner,
		Repo:   resolved.Repo,
		DryRun: cfg.DryRun,
		Render: delivery.render,
	}
	if !cfg.DryRun {
		req.StageFile = delivery.stage
	}
	if !d.prPoll.enabled {
		actions, err := removePRPollCaller(ctx, req)
		if err != nil {
			actions = append(actions, ComponentAction{
				Component: prPollComponent,
				Action:    "error",
				Detail:    fmt.Sprintf("failed to remove the fork PR review poll caller: %v", err),
			})
		}
		return actions
	}
	actions, err := ensurePRPollCaller(ctx, req)
	if err != nil {
		actions = append(actions, ComponentAction{
			Component: prPollComponent,
			Action:    "error",
			Detail:    fmt.Sprintf("failed to ensure the fork PR review poll caller: %v", err),
		})
	}
	return actions
}

// ensurePRPollCaller makes the managed caller present and current. A file
// at scaffold.PRPollCallerPath without scaffold.PRPollCallerMarker is
// user-owned and left untouched. The since stamp of an existing managed
// caller is kept, so later converges never move the enable time; without
// one (no managed caller yet, or an unreadable stamp) the current time is
// stamped, so re-enabling after a removal starts from the new enable time
// and reviews no backlog (ADR 0171).
func ensurePRPollCaller(ctx context.Context, req prPollRequest) ([]ComponentAction, error) {
	have, exists, err := readPRPollCaller(ctx, req)
	if err != nil {
		return nil, err
	}
	if exists && !scaffold.IsManagedPRPollCaller(have) {
		return []ComponentAction{{
			Component: prPollComponent,
			Action:    "none",
			Detail: fmt.Sprintf("%s exists but is not managed by fullsend; left it untouched (remove it to let repos install manage the fork PR review poll workflow)",
				scaffold.PRPollCallerPath),
		}}, nil
	}
	since, ok := "", false
	if exists {
		since, ok = scaffold.PRPollCallerSince(have)
	}
	if !ok {
		since = prPollNowFn().UTC().Format(time.RFC3339)
	}
	want, err := scaffold.RenderPRPollCaller(req.Render, since)
	if err != nil {
		return nil, err
	}
	if exists && bytes.Equal(have, want) {
		return []ComponentAction{{
			Component: prPollComponent,
			Action:    "none",
			Detail:    scaffold.PRPollCallerPath + " is current",
		}}, nil
	}
	action := "add"
	if exists {
		action = "update"
	}
	if req.DryRun {
		return []ComponentAction{{
			Component: prPollComponent,
			Action:    action,
			Detail:    fmt.Sprintf("would %s %s", action, scaffold.PRPollCallerPath),
		}}, nil
	}
	if req.StageFile == nil {
		return nil, errors.New("internal error: no scaffold commit to deliver " + scaffold.PRPollCallerPath)
	}
	req.StageFile(forge.TreeFile{Path: scaffold.PRPollCallerPath, Content: want, Mode: "100644"})
	return []ComponentAction{{
		Component: prPollComponent,
		Action:    action,
		Detail:    fmt.Sprintf("%s %s with the scaffold", action, scaffold.PRPollCallerPath),
	}}, nil
}

// removePRPollCaller deletes the managed caller if present. An absent or
// user-owned file reports nothing, so repositories that never enabled the
// poll see no action.
func removePRPollCaller(ctx context.Context, req prPollRequest) ([]ComponentAction, error) {
	have, exists, err := readPRPollCaller(ctx, req)
	if err != nil {
		return nil, err
	}
	if !exists || !scaffold.IsManagedPRPollCaller(have) {
		return nil, nil
	}
	if req.DryRun {
		return []ComponentAction{{
			Component: prPollComponent,
			Action:    "delete",
			Detail:    "would delete " + scaffold.PRPollCallerPath,
		}}, nil
	}
	if req.StageFile == nil {
		return nil, errors.New("internal error: no scaffold commit to delete " + scaffold.PRPollCallerPath)
	}
	req.StageFile(forge.TreeFile{Path: scaffold.PRPollCallerPath, Delete: true})
	return []ComponentAction{{
		Component: prPollComponent,
		Action:    "delete",
		Detail:    "delete " + scaffold.PRPollCallerPath + " with the scaffold",
	}}, nil
}

// readPRPollCaller reads the caller from the default branch. A missing file
// is not an error.
func readPRPollCaller(ctx context.Context, req prPollRequest) ([]byte, bool, error) {
	content, err := req.Client.GetFileContent(ctx, req.Owner, req.Repo, scaffold.PRPollCallerPath)
	if err != nil {
		if forge.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("reading %s: %w", scaffold.PRPollCallerPath, err)
	}
	return content, true, nil
}
