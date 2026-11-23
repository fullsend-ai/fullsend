---
sidebar_label: fullsend prepare
---

# fullsend prepare

Prepare the environment for an agent without running it. `fullsend prepare`
resolves the agent's sandbox image the same way [`fullsend run`](run.md) does.
It pulls the image into the OpenShell gateway's image store when the image is
not there yet. Then it creates and deletes a minimal sandbox to check that the
gateway can start one.

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
| `--max-depth` | Maximum dependency depth for transitive resolution (0 disables) |
| `--max-resources` | Maximum total remote resources per harness |

Pass the same `--fullsend-dir`, `--forge`, `--event-file`, and environment
that the runs use. The image can depend on all of them.

## What it does

1. Composes the agent's harness through the same code path as `fullsend run`:
   the agent source from `config.yaml` or the agents repository, the `base:`
   chain, the agent's `harness:` blocks, `lock.yaml` pins, and overlays.
2. Applies the `FULLSEND_SANDBOX_IMAGE` override, if set.
3. Checks that the OpenShell CLI is installed and that a gateway is running.
4. Warms the image:
   - On a local gateway, it checks the local Podman store, which the gateway's
     Podman driver pulls into. A cached image is not pulled again. A missing
     image is pulled with the registry credential `fullsend run` uses (see
     below), or anonymously when none applies.
   - When `OPENSHELL_GATEWAY_ENDPOINT` is set, the gateway may keep its images
     on another host. `fullsend prepare` does not pull into the local store,
     and it reports the image as `skipped`. The preflight sandbox in the next
     step makes the gateway pull the image into its own store, anonymously.
   - When Podman is not installed, the image is reported as `skipped` and the
     preflight sandbox pulls it through the gateway.
5. Creates one sandbox from the image, with no providers, no policy, and no
   workload, and waits until it is ready. The wait uses the harness
   `sandbox_timeout_seconds`. The gateway pulls any image of its own that it
   needs at this point, such as its supervisor image. The command then deletes
   the sandbox. A sandbox that fails to start is also deleted. The preflight
   makes one attempt, so a broken environment fails fast.

The preflight runs even when the image is already cached.

### Registry credentials

`fullsend prepare` pulls with the same job-scoped credential that
`fullsend run` uses before it creates a sandbox:

- On GitHub Actions, `GH_WORKFLOW_TOKEN` for an image on `ghcr.io`. See
  [GitHub Packages credentials](run.md#github-packages-credentials).
- On GitLab CI, `CI_REGISTRY_USER` and `CI_REGISTRY_PASSWORD` for an image on
  the instance's container registry (`CI_REGISTRY`).

The credential is written to an auth file in a private temporary directory
and removed after the pull. It is redacted from every message.

Unlike `fullsend run`, `fullsend prepare` leaves the runner secrets bundle
(`FULLSEND_GITHUB_RUNNER_SECRETS`) in place, so the runs that follow can still
read it.

## Exit status and failures

The command exits `0` when the image is in place and the preflight sandbox
started. Otherwise it exits non-zero and names the cause. When it recognizes
the cause, it also prints a hint, for example to free disk space. The `kind`
field of the JSON result names the cause:

| Kind | Cause |
|------|-------|
| `configuration` | The harness could not be resolved, or a flag, env file, event file, or `config.yaml` is invalid. An unknown agent is reported this way too |
| `openshell-unavailable` | The OpenShell CLI is not installed |
| `gateway-unavailable` | No OpenShell gateway is running |
| `disk-space` | The pull or the sandbox ran out of disk space |
| `registry-auth` | The registry refused the pull |
| `image-unresolvable` | The image reference could not be resolved |
| `image-pull` | The pull failed for another reason |
| `sandbox-preflight` | The preflight sandbox did not start for another reason |

## JSON output

With `--json`, stdout holds one JSON object, also when the command fails:

```json
{
  "ok": true,
  "agent": "code",
  "harness": "/work/repo/.fullsend/harness/code.yaml",
  "arch": "amd64",
  "gateway": "local",
  "images": [
    {
      "role": "sandbox",
      "ref": "ghcr.io/fullsend-ai/fullsend-code:latest",
      "source": "harness",
      "status": "pulled"
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
| `gateway` | `local`, or `remote` when `OPENSHELL_GATEWAY_ENDPOINT` is set |
| `images[].role` | `sandbox` |
| `images[].ref` | The resolved image reference. Empty when the harness sets no image and the gateway uses its default sandbox image |
| `images[].source` | `harness`, `FULLSEND_SANDBOX_IMAGE`, or `gateway-default` |
| `images[].status` | `cached`, `pulled`, `skipped` (left to the preflight sandbox), or `failed` |
| `images[].detail` | Why the image was skipped, when it was |
| `preflight.sandbox` | The name of the preflight sandbox |
| `preflight.status` | `passed` or `failed` |
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
