# Sandbox Image Topology

Fullsend agents run inside sandboxed containers. Two images exist in a
parent-child hierarchy; which image an agent uses depends on whether it
needs a compiled-language toolchain.

```
ghcr.io/nvidia/openshell-community/sandboxes/base   (upstream)
  +-- fullsend-sandbox                                (base sandbox)
        +-- fullsend-code                             (extends base with Go)
```

| Image | Agents | Run frequency | Key additions over parent |
|-------|--------|---------------|--------------------------|
| `fullsend-sandbox` | triage, prioritize, retro | High (most agent runs) | Claude Code, jq, gitleaks, acli, pre-commit, gitlint, tirith, ProtectAI DeBERTa model |
| `fullsend-code` | code, fix, review | Lower (code/fix are the least-run agents; review runs per-PR) | Go toolchain, scan-secrets, gopls, lychee |

Harness definitions that map agents to images live in
`internal/scaffold/fullsend-repo/harness/*.yaml` (the `image:` field).
The GitLab scaffold (`internal/scaffold/fullsend-repo-gitlab/`) uses the same execution model: a single generic agent template (`fullsend-agent.yml`) calls `fullsend run "${STAGE}"`, parameterized by the `$STAGE` pipeline variable set by the dispatch or poll templates. `fullsend run` resolves the harness and creates the sandbox container from the harness `image:` field.
Image Containerfiles live in `images/sandbox/` and `images/code/`.
The CI build pipeline is `.github/workflows/sandbox-images.yml`.

**When reviewing CI changes:** If a PR modifies image pulling, caching,
or pre-warming logic in `action.yml`, consider which agent types are
affected. Changes that only benefit `fullsend-code` have a smaller blast
radius (fewer agent runs) than changes to `fullsend-sandbox`. A cache or
pull optimization may not be worth the complexity if it only helps the
least-frequently-run agents.
