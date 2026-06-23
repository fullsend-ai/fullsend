package authorization

import "fmt"

// StickyMarker returns the HTML marker for auth gate sticky comments.
func StickyMarker(gate Gate) string {
	return fmt.Sprintf("<!-- fullsend:auth:%s -->", gate.Name)
}

// CommentBody returns the sticky comment body for the given phase and status.
func CommentBody(gate Gate, phase Phase, status Status) string {
	switch status {
	case StatusStale:
		return fmt.Sprintf(`## Workflow change authorization expired

The %q label was removed because a non-collaborator posted an agent-influencing comment after authorization.

A repository collaborator must re-apply the %q label, then re-trigger the agent (`+"`/fs-code` on the issue or `/fs-fix` on the PR)."+`

<sub>Posted by fullsend authorization gate (%s / %s)</sub>`,
			gate.AllowedLabel, gate.AllowedLabel, phase, status)

	case StatusUnauthorizedPush:
		return fmt.Sprintf(`## Workflow file push blocked

The agent changed files under %s but did not have authorization to push workflow changes.

The %q label has been applied. A repository collaborator must add %q, then re-trigger the agent (`+"`/fs-code` on the issue or `/fs-fix` on the PR)."+`

<sub>Posted by fullsend authorization gate (%s / %s)</sub>`,
			".github/workflows/", gate.NeededLabel, gate.AllowedLabel, phase, status)

	case StatusBlocked:
		switch phase {
		case PhaseMint:
			return fmt.Sprintf(`## Workflow change authorization required

This issue needs changes to GitHub Actions workflow files. The run will proceed **without** %q permission unless a collaborator adds the %q label before minting.

If workflow files must be edited, add %q and re-trigger with `+"`/fs-code`."+`

<sub>Posted by fullsend authorization gate (%s / %s)</sub>`,
				"workflows: write", gate.AllowedLabel, gate.AllowedLabel, phase, status)
		default:
			return fmt.Sprintf(`## Workflow change authorization required

The agent cannot start until a repository collaborator adds the %q label.

After authorization, re-trigger with `+"`/fs-code` on the issue or `/fs-fix` on the PR."+`

<sub>Posted by fullsend authorization gate (%s / %s)</sub>`,
				gate.AllowedLabel, phase, status)
		}

	default:
		return ""
	}
}
