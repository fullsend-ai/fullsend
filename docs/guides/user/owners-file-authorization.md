# Using Prow-style OWNERS for agent dispatch authorization

Fullsend can use Prow-style `OWNERS` files to authorize users to run
agents without needing direct collaborator roles on the forge. You can
introduce these files whether your project already uses Prow or not.

> **GitHub only.** The `owners_file` provider is read on the GitHub
> webhook dispatch path (slash commands, label triggers, and event
> triggers). The Go poll path used for GitLab and Jira does not read
> OWNERS — authorization on those platforms uses native forge roles only.

For background on the default forge-permission model (write vs. triage
thresholds, dispatch-path differences), see
[Agent dispatch authorization](customizing-agents.md#agent-dispatch-authorization).

## Setup

1. Add the `owners_file` provider to `.fullsend/config.yaml`:

   ```yaml
   authorization:
     - provider: owners_file
   ```

2. Create (or update) an `OWNERS` file at the repository root with
   `approvers` and/or `reviewers` lists:

   ```yaml
   approvers:
     - alice
     - bob
   reviewers:
     - carol
   ```

   `approvers` receive `write`-equivalent access — all agent slash
   commands and custom agents registered under `agents:`.
   **Exception:** `/fs-fix-stop` always checks the forge's collaborator
   API (or PR authorship) and does not read OWNERS. `reviewers` receive
   `triage`-equivalent access — `/fs-triage` and `/fs-review` only
   (custom agents still require `write`).

3. _(Optional)_ Define aliases in an `OWNERS_ALIASES` file at the repository
   root:

   ```yaml
   aliases:
     backend-team:
       - alice
       - bob
   ```

   Then reference the alias key in `OWNERS`:

   ```yaml
   approvers:
     - backend-team
   ```

## How OWNERS interacts with forge permissions

OWNERS can only **raise** a user's effective role, never lower it. Users not
found in OWNERS fall through to the forge's permission API. The OWNERS file
is read from a trusted ref so that PR authors cannot add themselves:
`pull_request_target` and `pull_request_review` events use the PR's
**base-branch SHA**; all other events — including slash commands posted on a
PR (`issue_comment`) — use the **default branch**.

For edge cases (parse failures, alias restrictions, character validation),
see the
[`authorization` config reference](../../reference/config-reference.md#authorization).

## See also

- [Configuring Agent Behavior](customizing-agents.md) — harness overrides, model selection, and agent toggling
- [Customizing Agents](customizing-overview.md) — overview of all customization approaches
- [Authorization Contract](../../normative/authorization/v1/README.md) — normative role hierarchy and exception rules
