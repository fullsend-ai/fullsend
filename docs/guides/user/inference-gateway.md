---
sidebar_label: Using an inference gateway
---

# Using an inference gateway

> Background: [How fullsend authenticates](how-fullsend-authenticates.md).

> **Before you start: who you need.** This route takes up to three roles
> ([Who does what](how-fullsend-authenticates.md#who-does-what)):
>
> - **Gateway operator**, the provider admin: runs the gateway
>   ([operator guide](../infrastructure/inference-gateway-operator.md)) and hands over the
>   **gateway URL**, the **audience**, and confirmation that your repository is on the gateway's
>   **allowed list**. None of them is a secret. In the `api-key` mode the operator hands over a
>   gateway key instead of the audience.
> - **Repo admin**: writes the `inference.gateway` block with those values and lists the models
>   the agents use ([Write the block](#write-the-block)). In the `api-key` mode, also stores the
>   key as a repository secret.
> - **Repo writer**: adds a workflow with `id-token: write` to run the live check in
>   [Check the block](#check-the-block). Optional.

Agents on the [pi](../../runtimes/pi.md), [Claude Code](../../runtimes/claude.md) and
[Codex](../../runtimes/codex.md) runtimes can reach their models through an
OpenAI/Anthropic-compatible inference gateway that trusts the job's GitHub Actions OIDC token
([ADR 0137](../../ADRs/0137-inference-gateway-credential-route.md)). Codex needs a gateway that
serves the Responses API. The gateway holds the
upstream provider credential, so on the recommended path your repository stores no reusable key.
This page follows the [agentgateway](https://github.com/agentgateway/agentgateway) setup in the
[operator guide](../infrastructure/inference-gateway-operator.md). The flags and fields are
listed in [`fullsend github setup`](../../cli/github.md) and the
[config reference](../../reference/config-reference.md); this page shows how they fit together.

The examples use `example-org/example-repo` and `https://gateway.example.com`.

## Prerequisites

For the repo admin:

- The gateway URL and the audience from the operator, and confirmation that your repository is
  on the allowed list.
- A repository [set up with fullsend](../getting-started/configuring-github.md) on the pi, codex or Claude Code runtime.
  pi needs the model list below; codex and Claude Code do not need one. See
  [Claude Code § Inference gateway route](../../runtimes/claude.md#inference-gateway-route) and
  [Codex › Models through an inference gateway](../../runtimes/codex.md#models-through-an-inference-gateway-experimental).
- The latest [fullsend](https://github.com/fullsend-ai/fullsend/releases) CLI.

## The config block

The route is one block in `.fullsend/config.yaml`:

```yaml
inference:
  gateway:
    url: https://gateway.example.com
    audience: fullsend-inference
    models:
      claude-sonnet-4-6:
        api: anthropic-messages
      gemini-2.5-flash:
        api: openai-completions
```

- **`url`** is the gateway's origin: `https`, port 443. If the gateway serves its API under a path
  prefix (for example behind a shared ingress), add the prefix without the trailing `/v1`:
  `https://gateway.example.com/team-a`. The runner adds the `/v1/...` paths itself.
- **`audience`** is the string the operator configured on the gateway, such as
  `fullsend-inference`. It is an agreed value, not necessarily the URL, and must match the
  gateway's character for character.
- **The model list** names every model the agents may call through the gateway, and the API the
  gateway serves it on: `anthropic-messages` for Claude, `openai-completions` for Gemini and
  open-weight models, `openai-responses` for GPT. pi runs offline and cannot ask the gateway for its
  models, so a `gateway/` model needs this list. Write it inline as `models`, or point
  `models_file` at a
  [pi-inference-gateway config file](https://github.com/fullsend-ai/pi-inference-gateway/blob/v0.1.1/docs/configuration.md#config-file)
  in the repository. Set one or the other, not both.

An agent selects the route through its model: `gateway/claude-sonnet-4-6` goes through the
gateway. `openai/` models and every other provider keep their own routes, so the block moves no
existing model onto the gateway. One run can mix them.

## Choosing `auth`

The block's `auth` field names the credential the runner presents. There is no fallback between
the modes: if the credential for the chosen mode is missing, or the gateway refuses it, the run
fails.

- **`auth: oidc`** is the default and the recommended mode. It is direct OIDC trust (no token
  exchange): the job's GitHub OIDC token, minted for `audience`, is itself the credential the
  gateway validates, and no reusable key is stored anywhere. The runner refreshes the token
  before it expires, so long runs keep working.
- **`auth: api-key`: use with caution.** It is for a gateway that cannot trust forge OIDC. The
  runner reads the gateway key from `FULLSEND_INFERENCE_GATEWAY_API_KEY`: a repository secret in
  CI, or the environment on a local run. The block needs no `audience` in this mode. It is a
  supported mode, but not the primary or safest one, because it relies on a long-lived secret:
  a leaked key works until the operator revokes it. Use `oidc` whenever the gateway supports it
  ([ADR 0138](../../ADRs/0138-inference-gateway-api-key-credential-mode.md)).

By default the key travels as `Authorization: Bearer`. If your operator says the gateway reads the
key from `x-api-key` on its Messages API, add `messages_auth_header: x-api-key` to an `api-key`
block. Claude Code and pi `anthropic-messages` models then send it as `x-api-key`, and every other
model keeps `Authorization: Bearer`. The setting is refused with `auth: oidc`
([ADR 0142](../../ADRs/0142-inference-gateway-x-api-key-header-for-messages-models.md),
[config reference](../../reference/config-reference.md)).

The `api-key` mode also applies to local runs: a local `fullsend run` with
`FULLSEND_INFERENCE_GATEWAY_API_KEY` in its environment takes the same route as CI. The `oidc`
mode applies only to runs that have a GitHub OIDC endpoint, so a local run without one leaves the
route to the [harness-plugin setup](running-agents-locally.md#using-an-inference-gateway-experimental).

For `api-key`, store the key the operator gave you as a repository secret:

```bash
fullsend github set example-org/example-repo FULLSEND_INFERENCE_GATEWAY_API_KEY '<gateway-key>'
```

```text
  • Setting repo secret FULLSEND_INFERENCE_GATEWAY_API_KEY on example-org/example-repo
  ✓ Set repo secret FULLSEND_INFERENCE_GATEWAY_API_KEY on example-org/example-repo
```

Or, with the GitHub CLI, which prompts for the value so it stays out of your shell history:

```bash
gh secret set FULLSEND_INFERENCE_GATEWAY_API_KEY -R example-org/example-repo
```

```text
? Paste your secret:
```

## Write the block

Pass the operator's values and your models to `fullsend github setup`:

```bash
fullsend github setup example-org/example-repo \
  --inference-gateway-url https://gateway.example.com \
  --inference-gateway-audience fullsend-inference \
  --inference-gateway-model claude-sonnet-4-6=anthropic-messages \
  --inference-gateway-model gemini-2.5-flash=openai-completions
```

```text
⚡ fullsend <version>
  Autonomous agentic development for Git-hosted organizations

→ Setting up per-repo fullsend for example-org/example-repo

    Updating existing .fullsend/config.yaml: inference.gateway (other keys kept; comments are not preserved)
  • Checking token permissions
  ✓ Token permissions verified

  • Creating scaffold PR for example-org/example-repo (target: main)
    User example-user has write access — pushing directly to example-org/example-repo
  ✓ Created PR #1: https://github.com/example-org/example-repo/pull/1
    Merge the PR to apply these changes
  • Configuring repository variables
  ✓ Set 5 repository variables
  • Configuring repository secrets
  ✓ Set 0 repository secrets

  ✓ Per-repo setup complete for example-org/example-repo
```

Use `--inference-gateway-models-file <file>` instead of the `--inference-gateway-model` flags to
commit a pi-inference-gateway config file. Setup changes only the keys you pass and opens a pull
request with the change. The pull request's `config.yaml` diff, on a repository already set up
with `--runtime pi`:

```diff
@@ -20,3 +20,12 @@ create_issues:
         repos:
             - example-org/example-repo
             - fullsend-ai/fullsend
+inference:
+    gateway:
+        url: https://gateway.example.com
+        audience: fullsend-inference
+        models:
+            claude-sonnet-4-6:
+                api: anthropic-messages
+            gemini-2.5-flash:
+                api: openai-completions
```

For the key mode, pass `--inference-gateway-auth api-key` and leave out
`--inference-gateway-audience`:

```bash
fullsend github setup example-org/example-repo \
  --inference-gateway-url https://gateway.example.com \
  --inference-gateway-auth api-key \
  --inference-gateway-model claude-sonnet-4-6=anthropic-messages \
  --inference-gateway-model gemini-2.5-flash=openai-completions
```

The output is the same as above. The pull request's diff:

```diff
@@ -20,3 +20,12 @@ create_issues:
         repos:
             - example-org/example-repo
             - fullsend-ai/fullsend
+inference:
+    gateway:
+        url: https://gateway.example.com
+        auth: api-key
+        models:
+            claude-sonnet-4-6:
+                api: anthropic-messages
+            gemini-2.5-flash:
+                api: openai-completions
```

Merge the pull request to apply the change.

## Check the block

Run `fullsend inference gateway status` from a checkout of the repository:

```bash
fullsend inference gateway status example-org/example-repo
```

In the `oidc` mode, outside GitHub Actions, it checks the block and stops:

```text
⚡ fullsend <version>
  Autonomous agentic development for Git-hosted organizations

→ Inference Gateway Status: example-org/example-repo

    url: https://gateway.example.com (from config.yaml)
    audience: fullsend-inference (from config.yaml)
    auth: oidc (default)
    models: claude-sonnet-4-6, gemini-2.5-flash (from config.yaml)

  ✓ url and audience are set

    Not inside a GitHub Actions job with id-token: write
    The assertion and gateway request can only be tested from a GitHub Actions workflow
```

Run the same command in a GitHub Actions job in the repository, with `id-token: write`, and it
also fetches one OIDC token for the audience and lists the models the gateway allows the
repository (`GET <url>/v1/models`, under the URL's path prefix if it has one). It never prints the
token.

> **Not executed here:** this walkthrough had no gateway to check against, so the output for a
> healthy gateway is not shown. A maintainer should add it from a run in GitHub Actions.

In the `api-key` mode the command reports the mode, warns that it relies on a long-lived secret,
and says whether `FULLSEND_INFERENCE_GATEWAY_API_KEY` is set (never its value). It does not call
the gateway:

```text
⚡ fullsend <version>
  Autonomous agentic development for Git-hosted organizations

→ Inference Gateway Status: example-org/example-repo

    url: https://gateway.example.com (from config.yaml)
    audience: (not set)
    auth: api-key (from config.yaml)
    models: claude-sonnet-4-6, gemini-2.5-flash (from config.yaml)

  ✓ url is set
  ! auth is api-key: the route relies on a long-lived gateway API key (FULLSEND_INFERENCE_GATEWAY_API_KEY); use it with caution, and prefer auth: oidc when the gateway can validate forge OIDC tokens
    FULLSEND_INFERENCE_GATEWAY_API_KEY is not set in this environment; a run in the api-key mode fails without it
```

A repository with no block fails the check:

```text
⚡ fullsend <version>
  Autonomous agentic development for Git-hosted organizations

→ Inference Gateway Status: example-org/example-repo

    url: (not set)
    audience: (not set)
    auth: oidc (default)
    models: (not set; pi gateway/ models need a model list)

  ✗ No inference.gateway block configured
    Configure it with 'fullsend github setup --inference-gateway-url <url> --inference-gateway-audience <aud>'
Error: no inference.gateway block configured for example-org/example-repo
```

## The workflow permission

In the `oidc` mode the job that runs the agent needs `id-token: write`, or it cannot mint the
token. The workflow `fullsend github setup` installs already grants it on its `dispatch` job:

```yaml
    permissions:
      actions: write
      id-token: write
```

If you call fullsend from a workflow of your own, add `id-token: write` to that job's
`permissions`.

## Running locally

A block with `auth: api-key` also applies to local runs; see [Choosing `auth`](#choosing-auth).
For pi, the harness-plugin path for local runs is in
[Running agents locally § Using an inference gateway](running-agents-locally.md#using-an-inference-gateway-experimental).

## Troubleshooting the gateway route

| You see | Cause | Action |
|---|---|---|
| `inference.gateway is partial: missing audience (the oidc mode needs url and audience)`, or from `status`: `inference.gateway is partially configured: missing audience` | The block, after its layers merge, lacks `url`, or lacks `audience` in the `oidc` mode. | Set the missing field with `fullsend github setup --inference-gateway-url` or `--inference-gateway-audience`, or remove the block. |
| `pi gateway/ models need a model list: set inference.gateway.models or inference.gateway.models_file (pi runs offline and cannot discover gateway models)` | An agent uses a `gateway/` model, and the block has no model list. | List the models with `--inference-gateway-model id=api` or `--inference-gateway-models-file`. |
| `inference.gateway.auth is "api-key" but FULLSEND_INFERENCE_GATEWAY_API_KEY is not set; set it as a forge secret (or in the local environment)` | The block is in the `api-key` mode and the run has no key. | Set the repository secret as shown in [Choosing `auth`](#choosing-auth), or export the variable for a local run. |
| From `status`: `Gateway refused the assertion (HTTP 401)` or `(HTTP 403)`; in a run, the model call fails with the gateway's `401` or `403` | The gateway did not accept the credential: a different audience, a repository not on its allowed list, or a revoked key. | Check the audience and allowed repositories with the operator. The gateway's exact error text and its fixes are in the operator guide's [troubleshooting table](../infrastructure/inference-gateway-operator.md#troubleshooting). |
| Model calls fail just after the host running the run wakes from sleep (a laptop or a suspended VM), in the `oidc` mode: the gateway's `401` on Claude Code, or an OpenShell `500` on pi and codex | The gateway token expired while the host slept. The refresher checks the wall clock every 30 seconds and refreshes on the first check after wake if the token is due or already expired; requests sent before that check can still fail. Only Claude Code's placeholder carries a run deadline past the token's expiry, so a stale token reaches the gateway and gets its `401`; on pi and codex the placeholder expires with the token and OpenShell answers `500`. | None if the run log then shows `Inference gateway token refreshed for … (next expiry` or `Inference gateway token handed off for …`. If it shows the refresh gave up or stopped, re-run. |
| Any other gateway error, such as `authentication failure: ...` or `model_authorization_denied` | The gateway refused the request for its own reasons. | See the operator guide's [troubleshooting table](../infrastructure/inference-gateway-operator.md#troubleshooting). |
