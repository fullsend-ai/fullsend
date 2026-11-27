---
sidebar_label: fullsend prepare
---

# fullsend prepare

Prepare the environment for an agent without running it. `fullsend prepare`
resolves the agent's sandbox image the same way [`fullsend run`](run.md) does.
It pre-pulls a private image with the CI job's registry credential (the workflow
token for `ghcr.io` on GitHub Actions, the job's registry login for the
instance's container registry on GitLab CI), as `fullsend run` does. Then it creates and deletes a minimal sandbox. The OpenShell gateway pulls
the image into its own image store when that sandbox starts, which also checks
that the gateway can start one.

It runs no pre-script, agent, validation, post-script, or inference, and it
mints no tokens.

Run it once before you start many `fullsend run` invocations in parallel, for
example in an evaluation harness. The image is then pulled once instead of by
every run, and a broken environment fails once, with a named cause, instead of
failing every parallel run.

## Usage

```bash
fullsend prepare <agent-name> [flags]
```

## Flags

| Flag | Description |
|------|-------------|
| `--fullsend-dir` | Path to the `.fullsend` configuration directory (default `.fullsend`) |
| `--env-file` | Load environment variables from a dotenv file (repeatable), as with `fullsend run` |
| `--forge` | Forge platform (`github`, `gitlab`). When omitted, resolved as `fullsend run` resolves it |
| `--event-file` | Normalized event JSON file for overlay resolution. When omitted, the dispatch payload is read, as with `fullsend run` |
| `--json` | Print the result as JSON on stdout. Progress goes to stderr |
| `--offline` | Reject network fetches; only use cached remote resources |

Pass the same `--fullsend-dir`, `--forge`, `--event-file`, and environment
that the runs use. The image can depend on all of them.

## What it does

1. Composes the agent's harness through the same code path as `fullsend run`:
   the agent source from `config.yaml` or the agents repository, the `base:`
   chain, the agent's `harness:` blocks, `lock.yaml` pins, and overlays. The
   agents repository can be a Forgejo repository when `FULLSEND_FORGEJO_URL` and
   the other `FULLSEND_FORGEJO_*` variables are set (see
   [Agent definitions from a Forgejo instance](run.md#agent-definitions-from-a-forgejo-instance)).
2. Applies the `FULLSEND_SANDBOX_IMAGE` override, if set.
3. Checks that the OpenShell CLI is installed and that a gateway is running.
4. Pre-pulls the image the way `fullsend run` does (see
   [Registry credentials](#registry-credentials)). This applies only to a
   private image on `ghcr.io` on GitHub Actions or on the instance's container
   registry on GitLab CI, and only into the local Podman store. It does not warm the gateway's image store, and `fullsend prepare`
   does not try to find out where that store is:
   - With no credential, or for any other image, nothing is pulled locally.
   - With a credential, an image already in the local Podman store is not
     pulled again, and a missing one is pulled with the credential. If the
     pull fails, or Podman is not installed, the command warns and goes on, as
     `fullsend run` does: the gateway pulls the image without the credential,
     so a public image keeps working. A full disk is reported by the preflight.
5. Creates one sandbox from the image, with no providers and no workload, under
   a restrictive policy (the base policy generated agents use: filesystem,
   Landlock, and process rules, and no network access). It waits until the
   sandbox is ready. The wait uses the harness `sandbox_timeout_seconds`, and
   interrupting the command stops it. The gateway pulls any image of its own
   that it needs at this point, such as its supervisor image, and the image
   counts as `ready` only when this sandbox starts from it. The command then
   deletes the sandbox. A sandbox that fails to start is also deleted. A
   sandbox that cannot be deleted fails the command with `sandbox-cleanup`,
   and the hint names the `openshell sandbox delete` command to run. The
   preflight makes one attempt, so a broken environment fails fast.

The preflight runs even when the image is already in the local Podman store.

### Registry credentials

`fullsend prepare` pulls with the same code, and the same job-scoped credential,
that `fullsend run` uses before it creates a sandbox:

- On GitHub Actions, the workflow token for an image on `ghcr.io`. `fullsend run`
  gets it as `GH_WORKFLOW_TOKEN` when it mints its agent token. `fullsend prepare`
  mints nothing, so it uses an exported `GH_WORKFLOW_TOKEN` or, when that is not
  set, the job's `GH_TOKEN` (or, when that is empty, `GITHUB_TOKEN`), which must
  look like a token. It does not export the token. See
  [GitHub Packages credentials](run.md#github-packages-credentials).
- On GitLab CI, the job's `CI_REGISTRY_USER` and `CI_REGISTRY_PASSWORD` for an
  image on the instance's container registry (`CI_REGISTRY`).

The credential is written to an auth file in a private temporary directory
and removed after the pull. It is redacted from every message.

Unlike `fullsend run`, `fullsend prepare` does not consume the runner secrets
bundle (`FULLSEND_GITHUB_RUNNER_SECRETS` and
`FULLSEND_GITHUB_RUNNER_SECRETS_FILE`), so the runs that follow can still read
it. It hides the bundle from every process it starts, such as Podman and
OpenShell, while it runs, and restores it when it exits. The bundle file is
not deleted.

## Exit status and failures

The command exits `0` when the preflight sandbox started from the image. Otherwise it exits non-zero and names the cause. When it recognizes
the cause, it also prints a hint, for example to free disk space. The `kind`
field of the JSON result names the cause:

| Kind | Cause |
|------|-------|
| `configuration` | The harness could not be resolved, or a flag, env file, event file, or `config.yaml` is invalid. An unknown agent is reported this way too |
| `openshell-unavailable` | The OpenShell CLI is not installed |
| `gateway-unavailable` | No OpenShell gateway is running |
| `disk-space` | The gateway ran out of disk space while pulling the image or starting the sandbox |
| `registry-auth` | The registry refused the gateway's pull |
| `image-unresolvable` | The image reference could not be resolved |
| `sandbox-preflight` | The preflight sandbox did not start for another reason |
| `sandbox-cleanup` | The preflight sandbox started but could not be deleted |
| `canceled` | The command was interrupted |

## JSON output

With `--json`, stdout holds one JSON object, also when the command fails:

```json
{
  "ok": true,
  "agent": "code",
  "harness": "/work/repo/.fullsend/harness/code.yaml",
  "arch": "amd64",
  "images": [
    {
      "role": "sandbox",
      "ref": "ghcr.io/fullsend-ai/fullsend-code:latest",
      "source": "harness",
      "status": "ready",
      "detail": "pulled into the local Podman store with the job's registry credential; the preflight sandbox started from it, so the gateway has pulled it"
    }
  ],
  "preflight": {
    "sandbox": "fs-cod-1a2b3c4d5e6f",
    "status": "passed"
  }
}
```

| Field | Description |
|-------|-------------|
| `ok` | `true` when preparation succeeded |
| `agent` | The agent name |
| `harness` | The harness file the agent resolved to |
| `arch` | The sandbox architecture: the host's, or `FULLSEND_SANDBOX_ARCH` when set to a supported value |
| `images[].role` | `sandbox` |
| `images[].ref` | The resolved image reference. Empty when the harness sets no image and the gateway uses its default sandbox image |
| `images[].source` | `harness`, `FULLSEND_SANDBOX_IMAGE`, or `gateway-default` |
| `images[].status` | `ready` (the preflight sandbox started from it), `failed` (the gateway could not pull or resolve it), or `unverified` (the preflight did not get that far, or failed for another reason). A local pull alone never makes it `ready` |
| `images[].detail` | What happened in the local Podman store, and what the gateway does |
| `preflight.sandbox` | The name of the preflight sandbox |
| `preflight.status` | `passed` or `failed` |
| `preflight.cleanup` | `deleted` or `failed` once a sandbox was created. `failed` means the sandbox may still exist |
| `error.kind` | The cause of a failure (see the table above) |
| `error.message` | The error message |
| `error.hint` | What to try next, when the cause is recognized |

## Example

Prepare once, then fan out:

```bash
fullsend prepare code --fullsend-dir .fullsend --json > prepare.json || exit 1
for case in cases/*; do
  fullsend run code --fullsend-dir .fullsend --target-repo "$case" &
done
wait
```
