---
sidebar_label: fullsend dispatch
---

# fullsend dispatch

Evaluate harness CEL triggers for one forge event and print the dispatch matrix
(`--output-driver gha-matrix` or `json`). The dispatch workflow's
`Harness dispatch` job uses it.

## Usage

```bash
fullsend dispatch [flags]
```

## Routing a built-in agent

`--route <agent>` (`review`, `retro`, `triage` or `prioritize`) instead decides
routing for that built-in agent and prints one JSON line:

```json
{"agent":"<agent>","decision":"run|skip|bash","reason":"…","harness":"config:<source>|agents@<sha>"}
```

`harness` is optional: it is omitted when the harness cannot be resolved or the
decision did not need it, as for `bash`.

The decision is one of:

- `run` or `skip` when the repository set `agents[].trigger` on the agent's
  entry
  ([Choosing when review runs](../guides/user/customizing-agents.md#choosing-when-review-runs),
  [Choosing when retro runs](../guides/user/customizing-agents.md#choosing-when-retro-runs),
  [Choosing when triage and prioritize run](../guides/user/customizing-agents.md#choosing-when-triage-and-prioritize-run)).
- `bash` to leave the decision to the workflow's built-in rules.

Built-in agents resolve from a configured Forgejo instance when
`FULLSEND_FORGEJO_URL` is set, as in
[`fullsend run`](run.md#agent-definitions-from-a-forgejo-instance). The
dispatch workflow's `Route` job runs it.

## Bot actor role lookup

With `--input-driver gha-event`, a GitHub bot actor's registered fullsend role
is looked up first through the mint (a layered `mint_url`, else
`FULLSEND_MINT_URL`) with the job's Actions OIDC token, so the job needs
`id-token: write`
([ADR 0107](../ADRs/0107-bot-identity-resolution-for-dispatch-authorization.md)).
Label-added events skip the lookup, and an unregistered bot or a failed lookup
is denied (an empty matrix, or a `skip` decision) rather than failing the
command.

An absent config falls back to `FULLSEND_MINT_URL`, but a config that fails to
load or parse is a failed lookup, not a fallback.

## Forgejo events

`--input-driver forgejo-event` maps a Forgejo Actions event payload
(`FORGEJO_EVENT_PATH`, bound to `FORGEJO_SERVER_URL` and the repository). It is
the default when `FORGEJO_ACTIONS` or `GITEA_ACTIONS` is `true`, even when
`GITHUB_EVENT_PATH` is also set.

This changes the default for pipelines on Forgejo or Gitea runners that relied
on the implicit `gha-event` driver; pass `--input-driver gha-event` to restore
the previous behavior.

No Forgejo permission lookup is wired yet, so its events are denied by the
authorization gate
([Forgejo adapter notes](../normative/normalized-event/v1/README.md#forgejo-adapter-notes)).
