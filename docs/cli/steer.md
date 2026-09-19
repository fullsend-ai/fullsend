---
sidebar_label: fullsend steer
---

# fullsend steer

Reach an agent run that is already working on an issue or pull request, from a
workstation.

`fullsend steer` posts a stage slash command on the work item, as you. That
comment fires the repository's fullsend shim like any other event, and the run
already in flight on that item absorbs it instead of being cancelled and
restarted ([ADR 0113](../ADRs/0113-steer-the-running-agent-on-work-item-updates.md)).

There is no steer-specific command. The runner accepts a follow-up run on
provenance, never on which words the comment opened with, so the run in flight
absorbs the comment whichever stage command produced it — and if no run is in
flight, the comment simply dispatches a normal one.

## Usage

```bash
fullsend steer <work-item-url> <text> [flags]
```

| Flag | Description |
|------|-------------|
| `--stage` | Stage to steer: `review`, `fix` or `triage`. Default: `review` for a pull request, `triage` for an issue |

A pull request posts `/fs-review` by default and an issue posts `/fs-triage`.
`--stage fix` posts `/fs-fix` and needs write permission on the repository; the
others need triage.

Authentication is `GH_TOKEN`, then `GITHUB_TOKEN`, then `gh auth token`. The CLI
proves nothing on its own: the comment is posted as you, the route job
authorizes it, and the runner verifies provenance.

## Example

```bash
fullsend steer https://github.com/org/repo/pull/123 "head moved; re-check the migration"
```

> Not executed here. Running it posts a real comment on a real work item, so the
> output below is omitted rather than invented. The error paths in
> [Troubleshooting](#troubleshooting) were executed and their output is real.

On success the command prints the comment it posted and the URL it posted to,
and exits 0. The run in flight picks the comment up on its next poll — within
`steer.poll_interval_seconds`, 30 seconds by default — or at its next turn end,
whichever comes first. Nothing happens instantly: if the agent is mid-turn, the
update is delivered when that turn ends.

## Troubleshooting

Every message below is the command's real output. All of these exit 1.

| Error | Cause and action |
|---|---|
| `steering is not supported on "gitlab" yet; post a stage command such as /fs-review on the merge request by hand` | The watcher is GitHub-only. Post the stage command on the merge request yourself. |
| `unsupported forge host "example.com"` | The URL is not a recognised GitHub or GitLab work item. Pass the item's `https://` URL. |
| `resolving org/repo#1: not found` | The work item does not exist, or your token cannot see it. Check the number and the token's repository access. |
| `--stage must be review, fix or triage, got "deploy"` | Unknown stage. Note the work item is resolved first, so a bad URL is reported before a bad stage. |
| `--stage "review" applies to a pull request, and the forge reports this number is an issue` | The forge says the number is an issue. Use `--stage triage`, or omit the flag. |

## Related

- [Steering a run in flight](../contributing/steering.md) — how the run in
  flight discovers and absorbs the comment.
- [`fullsend run` § Run baseline](run.md#run-baseline) — what a run is told
  about the work item it started on.
