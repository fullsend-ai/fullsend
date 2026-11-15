# Sandbox Image Topology

Fullsend agents run inside sandboxed containers. The regular sandbox image
supports agents that need general tools; the code image adds a compiled-language
toolchain. A separate tools image exposes Fullsend's pinned runtime tools for
read-only mounting into a development image.

```
ghcr.io/nvidia/openshell-community/sandboxes/base   (upstream)
  +-- fullsend-sandbox                                (base sandbox)
  |     +-- fullsend-code                             (extends base with Go)
  +-- fullsend-sandbox-tools                          (mount-only tool root)
```

`fullsend-sandbox-tools` is built from the same pinned tool stages as
`fullsend-sandbox`, with the compatibility link removed so its root can be
mounted at `/opt/fullsend`. It is not a sandbox `image:` replacement: add it
as an `image_mounts` entry and grant the destination read-only access in the
effective sandbox policy. The mount uses the sandbox's ABI and libraries, so
the development image must be compatible with the tools image. See the
[runtime tool image contract](runtime-implementation.md#fullsend-tool-image-mount-contract).

| Image | Agents | Run frequency | Key additions over parent |
|-------|--------|---------------|--------------------------|
| `fullsend-sandbox` | triage, prioritize, retro | High (most agent runs) | Claude Code, pi (pinned; opt-in runtime, #6464), Codex (pinned; opt-in runtime, #6920), ripgrep + fd (pi grep/find backends), jq, gitleaks, acli, pre-commit, gitlint, tirith |
| `fullsend-code` | code, fix, review | Lower (code/fix are the least-run agents; review runs per-PR) | Go toolchain, scan-secrets, gopls, lychee |

Harness definitions that map agents to images live in
the [`fullsend-ai/agents`](https://github.com/fullsend-ai/agents) repo (the `image:` field in each harness YAML).
The GitLab scaffold (`internal/scaffold/fullsend-repo-gitlab/`) uses the same execution model: a single generic agent template (`fullsend-agent.yml`) calls `fullsend run "${STAGE}"`, parameterized by the `stage` pipeline input set by API-triggered pipelines dispatched by the cron poller (#7850), or by `source=pipeline` child pipelines the trigger-only account's poll or dispatcher job starts with its `CI_JOB_TOKEN` (#8438) — the template bridges that input into the `STAGE` job environment variable `fullsend run` reads. `fullsend run` resolves the harness and creates the sandbox container from the harness `image:` field.
Image Containerfiles live in `images/sandbox/` and `images/code/`.
The CI build pipeline is `.github/workflows/sandbox-images.yml`.

**When reviewing CI changes:** If a PR modifies image pulling, caching,
or pre-warming logic in `action.yml`, consider which agent types are
affected. Changes that only benefit `fullsend-code` have a smaller blast
radius (fewer agent runs) than changes to `fullsend-sandbox`. A cache or
pull optimization may not be worth the complexity if it only helps the
least-frequently-run agents.
