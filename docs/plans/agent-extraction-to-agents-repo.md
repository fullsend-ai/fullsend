# Plan: Extract remaining agents from fullsend to fullsend-ai/agents

**Date:** 2026-07-02
**Implements:** [ADR 0058](../ADRs/0058-agent-registration.md) Phase 4 (extended to all agents)
**Prerequisite:** ADR 0058 Phases 1–3 are already implemented.

## Current state

### Triage agent (already extracted)

The triage agent has been replicated to `fullsend-ai/agents` and is
running from that repo. The scaffold still contains the triage source
files (Phase 4 deletion has not been done yet).

### Triage agent: fullsend vs. agents repo differences

Files that are **identical** between `upstream/main` scaffold and
`fullsend-ai/agents@main`:

| File | Path in scaffold | Path in agents repo |
|------|-----------------|-------------------|
| Agent prompt | `agents/triage.md` | `agents/triage.md` |
| Pre-script | `scripts/pre-triage.sh` | `scripts/pre-triage.sh` |
| Post-script | `scripts/post-triage.sh` | `scripts/post-triage.sh` |
| Post-script tests | `scripts/post-triage-test.sh` | `scripts/post-triage-test.sh` |
| Output schema | `schemas/triage-result.schema.json` | `schemas/triage-result.schema.json` |
| Validation script | `scripts/validate-output-schema.sh` | `scripts/validate-output-schema.sh` |
| Validation tests | `scripts/validate-output-schema-test.sh` | `scripts/validate-output-schema-test.sh` |
| Issue-labels skill | `skills/issue-labels/SKILL.md` | `skills/issue-labels/SKILL.md` |

Files that **differ** between the two repos:

#### 1. `harness/triage.yaml` — three changes required for external-repo operation

```diff
 host_files:
-  - src: env/gcp-vertex.env
+  - src: common/env/gcp-vertex.env
     dest: /sandbox/workspace/.env.d/gcp-vertex.env
     expand: true
   - src: ${GOOGLE_APPLICATION_CREDENTIALS}
     dest: /tmp/.gcp-credentials.json
   - src: ${GCP_OIDC_TOKEN_FILE}
     dest: /sandbox/workspace/.gcp-oidc-token
     optional: true
+  - src: env/triage.env
+    dest: /sandbox/workspace/.env.d/triage.env
+    expand: true

 validation_loop:
   script: scripts/validate-output-schema.sh
+  schema: schemas/triage-result.schema.json
   max_iterations: 2

-env:
-  runner:
-    FULLSEND_OUTPUT_SCHEMA: ${FULLSEND_DIR}/schemas/triage-result.schema.json
```

**Why each change is needed:**

1. **`env/gcp-vertex.env` → `common/env/gcp-vertex.env`**: In the
   scaffold, each agent references `env/gcp-vertex.env` directly. In
   the agents repo, shared env files are placed in `common/env/` to
   avoid duplication when multiple agents coexist. The content is
   identical.

2. **Added `env/triage.env` host_file**: In the scaffold, the forge
   section's `env.runner`/`env.sandbox` injects `GITHUB_ISSUE_URL`
   and `GH_TOKEN` into the agent's environment. In the agents repo,
   these are instead provided via a dedicated env file
   (`env/triage.env`) loaded as a host_file. This is the
   self-contained alternative to relying on the forge section's env
   injection, which requires `${FULLSEND_DIR}` resolution that only
   works when the harness is loaded from the scaffold.

3. **`validation_loop.schema` replaces `env.runner.FULLSEND_OUTPUT_SCHEMA`**:
   The scaffold sets `FULLSEND_OUTPUT_SCHEMA` as a runner env var
   using `${FULLSEND_DIR}` expansion. In an external repo,
   `${FULLSEND_DIR}` does not resolve to the correct path. The
   `validation_loop.schema` field tells the harness to set
   `FULLSEND_OUTPUT_SCHEMA` itself after resolving the schema path
   relative to the harness file — a feature that was added to
   `run.go:validationEnv()` to support exactly this use case.

#### 2. `policies/triage.yaml` — cosmetic only

The scaffold version has a YAML document marker (`---`) at line 1;
the agents repo omits it. No functional difference. When extracting
remaining agents, include the `---` document marker to stay
consistent with the scaffold originals.

#### 3. Additional files in agents repo (not in scaffold)

| File | Purpose |
|------|---------|
| `common/env/gcp-vertex.env` | Shared GCP Vertex env template (same content as scaffold's `env/gcp-vertex.env`) |
| `env/triage.env` | Agent-specific env file exporting `GITHUB_ISSUE_URL` and `GH_TOKEN` |
| `.github/workflows/fullsend.yaml` | Fullsend shim workflow (auto-managed by enrollment) |
| `docs/triage.md` | Agent documentation (slightly simplified from scaffold's `docs/agents/triage.md`) |
| `docs/icons/triage.png` | Agent icon |

#### 4. Files NOT present in agents repo (remain in fullsend)

| File | Why it stays |
|------|-------------|
| `env/gcp-vertex.env` | Replaced by `common/env/gcp-vertex.env` in agents repo |
| `.github/workflows/triage.yml` | Scaffold workflow template with placeholders; not needed in agents repo |
| `.github/workflows/reusable-triage.yml` | Reusable workflow stays in fullsend; agents repo does not host workflow infrastructure |
| `eval/triage/` | Eval framework stays in fullsend for now |

---

## Agents remaining to extract

| Agent | Skills | Plugins | Image | Env files | Shared skills |
|-------|--------|---------|-------|-----------|---------------|
| **code** | `code-implementation` | `gopls-lsp` | `fullsend-code:latest` | `code-agent.env`, `gcp-vertex.env` | — |
| **fix** | `fix-review` | — | `fullsend-code:latest` | `fix-agent.env`, `gcp-vertex.env` | — |
| **review** | `pr-review`, `code-review`, `docs-review`, `issue-labels` | — | `fullsend-code:latest` | `review.env`, `gcp-vertex.env` | `issue-labels` (shared with triage) |
| **retro** | `retro-analysis`, `finding-agent-runs`, `agent-scaffolding`, `autonomy-readiness` | — | `fullsend-sandbox:latest` | `retro.env`, `gcp-vertex.env` | — |
| **prioritize** | — | — | `fullsend-sandbox:latest` | `gcp-vertex.env` | — |

### Skills inventory and sharing

```
Skill                    Used by        Files
─────────────────────    ──────────     ─────────────────────────────────────────
issue-labels             triage,review  skills/issue-labels/SKILL.md
code-implementation      code           skills/code-implementation/SKILL.md
code-review              review         skills/code-review/SKILL.md
docs-review              review         skills/docs-review/SKILL.md
fix-review               fix            skills/fix-review/SKILL.md
pr-review                review         skills/pr-review/SKILL.md
                                        skills/pr-review/meta-prompt.md
                                        skills/pr-review/sub-agents/*.md (7 files)
retro-analysis           retro          skills/retro-analysis/SKILL.md
finding-agent-runs       retro          skills/finding-agent-runs/SKILL.md
agent-scaffolding        retro          skills/agent-scaffolding/SKILL.md
autonomy-readiness       retro          skills/autonomy-readiness/SKILL.md
```

### Plugin inventory

```
Plugin                   Used by        Files
─────────────────────    ──────────     ─────────────────────────
gopls-lsp                code           plugins/gopls-lsp/.lsp.json
                                        plugins/gopls-lsp/plugin.json
```

---

## Preserving git history during extraction

Files must be moved with their commit history intact so that
`git log --follow` and `git blame` work in the agents repo. A plain
file copy loses all history. Use `git filter-repo` to extract the
relevant paths from fullsend into a temporary clone, rewrite paths
to match the agents repo layout, and merge the result.

### Procedure (run once per agent or batched for all)

```bash
# 1. Create a disposable clone of fullsend for history extraction.
git clone --no-checkout https://github.com/fullsend-ai/fullsend.git /tmp/fullsend-extract
cd /tmp/fullsend-extract

# 2. Use git filter-repo to keep only the agent's files and rewrite
#    paths from the scaffold prefix to the agents repo root.
#
#    --path selects files to keep (repeat for each path).
#    --path-rename strips the scaffold prefix so the files land at
#    the correct location in the agents repo.
#
#    Example for the code agent:
git filter-repo \
  --path internal/scaffold/fullsend-repo/agents/code.md \
  --path internal/scaffold/fullsend-repo/harness/code.yaml \
  --path internal/scaffold/fullsend-repo/policies/code.yaml \
  --path internal/scaffold/fullsend-repo/schemas/code-result.schema.json \
  --path internal/scaffold/fullsend-repo/scripts/pre-code.sh \
  --path internal/scaffold/fullsend-repo/scripts/post-code.sh \
  --path internal/scaffold/fullsend-repo/scripts/post-code-test.sh \
  --path internal/scaffold/fullsend-repo/env/code-agent.env \
  --path internal/scaffold/fullsend-repo/skills/code-implementation/ \
  --path internal/scaffold/fullsend-repo/plugins/gopls-lsp/ \
  --path-rename internal/scaffold/fullsend-repo/: \
  --force

# Also include docs from the top-level docs/ directory:
# (run a second filter-repo pass or handle docs separately)

# 3. From the agents repo, add the filtered clone as a remote and
#    merge its history.
cd /path/to/fullsend-agents
git remote add extract /tmp/fullsend-extract
git fetch extract
git merge extract/main --allow-unrelated-histories \
  -m "feat: import code agent with history from fullsend-ai/fullsend"
git remote remove extract

# 4. Apply harness adaptations (see "Harness adaptation pattern") as
#    a follow-up commit on the same branch.

# 5. Clean up.
rm -rf /tmp/fullsend-extract
```

### Path renames

`--path-rename internal/scaffold/fullsend-repo/:` strips the scaffold
prefix, placing files at the repo root. Files that need further
renaming (e.g., `env/code-agent.env` → `env/code.env`) should use an
additional `--path-rename` flag:

```bash
--path-rename env/code-agent.env:env/code.env
```

### Docs and icons

Agent docs live at `docs/agents/<agent>.md` in fullsend (outside the
scaffold prefix). Include them with a separate `--path` and rename:

```bash
--path docs/agents/code.md \
--path docs/agents/icons/coder.png \
--path-rename docs/agents/:docs/
```

### Batching multiple agents

To extract all remaining agents in a single pass, list all paths
for all agents in one `git filter-repo` invocation. This produces a
single merge commit with combined history. Individual harness
adaptations can then be done as separate commits.

### Shared files (gcp-vertex.env, validate-output-schema.sh)

Files already present in the agents repo (from the triage extraction)
should NOT be re-imported — they would create merge conflicts. Exclude
them from the `--path` list:

- `env/gcp-vertex.env` — already at `common/env/gcp-vertex.env`
- `scripts/validate-output-schema.sh` — already present
- `scripts/validate-output-schema-test.sh` — already present
- `skills/issue-labels/` — already present

If the scaffold version has diverged from the agents repo copy,
reconcile manually after the merge.

### Verifying history

After the merge, verify history is intact:

```bash
git log --follow -- agents/code.md
git log --follow -- scripts/post-code.sh
```

Each file should show commits from its life in the fullsend repo
(under the scaffold prefix) followed by the merge commit.

---

## Per-agent file manifest

For each agent, these files must be extracted to the agents repo with
history preserved (see above). The table shows the scaffold source
path (relative to `internal/scaffold/fullsend-repo/`) and the target
path in the agents repo.

### Code agent

| Scaffold path | Agents repo path | Notes |
|---------------|-----------------|-------|
| `agents/code.md` | `agents/code.md` | Copy as-is |
| `harness/code.yaml` | `harness/code.yaml` | Needs adaptation (see below) |
| `policies/code.yaml` | `policies/code.yaml` | Copy as-is (retain `---` document marker) |
| `schemas/code-result.schema.json` | `schemas/code-result.schema.json` | Copy as-is |
| `scripts/pre-code.sh` | `scripts/pre-code.sh` | Copy as-is |
| `scripts/post-code.sh` | `scripts/post-code.sh` | Copy as-is |
| `scripts/post-code-test.sh` | `scripts/post-code-test.sh` | Copy as-is |
| `env/code-agent.env` | `env/code.env` | Rename for consistency |
| `skills/code-implementation/` | `skills/code-implementation/` | Copy directory |
| `plugins/gopls-lsp/` | `plugins/gopls-lsp/` | Copy directory |
| `docs/agents/code.md` | `docs/code.md` | Adapt paths/links |
| `docs/agents/icons/coder.png` | `docs/icons/coder.png` | Copy |

### Fix agent

| Scaffold path | Agents repo path | Notes |
|---------------|-----------------|-------|
| `agents/fix.md` | `agents/fix.md` | Copy as-is |
| `harness/fix.yaml` | `harness/fix.yaml` | Needs adaptation |
| `policies/fix.yaml` | `policies/fix.yaml` | Copy as-is |
| `schemas/fix-result.schema.json` | `schemas/fix-result.schema.json` | Copy as-is |
| `scripts/pre-fix.sh` | `scripts/pre-fix.sh` | Copy as-is |
| `scripts/post-fix.sh` | `scripts/post-fix.sh` | Copy as-is |
| `scripts/post-fix-test.sh` | `scripts/post-fix-test.sh` | Copy as-is |
| `env/fix-agent.env` | `env/fix.env` | Rename for consistency |
| `skills/fix-review/` | `skills/fix-review/` | Copy directory |
| `docs/agents/fix.md` | `docs/fix.md` | Adapt paths/links |

### Review agent

| Scaffold path | Agents repo path | Notes |
|---------------|-----------------|-------|
| `agents/review.md` | `agents/review.md` | Copy as-is |
| `harness/review.yaml` | `harness/review.yaml` | Needs adaptation |
| `policies/review.yaml` | `policies/review.yaml` | Copy as-is |
| `schemas/review-result.schema.json` | `schemas/review-result.schema.json` | Copy as-is |
| `scripts/pre-review.sh` | `scripts/pre-review.sh` | Copy as-is |
| `scripts/post-review.sh` | `scripts/post-review.sh` | Copy as-is |
| `scripts/post-review-test.sh` | `scripts/post-review-test.sh` | Copy as-is |
| `env/review.env` | `env/review.env` | Copy as-is |
| `skills/pr-review/` | `skills/pr-review/` | Copy full directory (includes sub-agents) |
| `skills/code-review/` | `skills/code-review/` | Copy directory |
| `skills/docs-review/` | `skills/docs-review/` | Copy directory |
| `skills/issue-labels/` | Already exists | Shared with triage — already in agents repo |
| `docs/agents/review.md` | `docs/review.md` | Adapt paths/links |
| `docs/agents/icons/review.png` | `docs/icons/review.png` | Copy |

### Retro agent

| Scaffold path | Agents repo path | Notes |
|---------------|-----------------|-------|
| `agents/retro.md` | `agents/retro.md` | Copy as-is |
| `harness/retro.yaml` | `harness/retro.yaml` | Needs adaptation |
| `policies/retro.yaml` | `policies/retro.yaml` | Copy as-is |
| `schemas/retro-result.schema.json` | `schemas/retro-result.schema.json` | Copy as-is |
| `scripts/pre-retro.sh` | `scripts/pre-retro.sh` | Copy as-is |
| `scripts/post-retro.sh` | `scripts/post-retro.sh` | Copy as-is |
| `scripts/post-retro-test.sh` | `scripts/post-retro-test.sh` | Copy as-is |
| `env/retro.env` | `env/retro.env` | Copy as-is |
| `skills/retro-analysis/` | `skills/retro-analysis/` | Copy directory |
| `skills/finding-agent-runs/` | `skills/finding-agent-runs/` | Copy directory |
| `skills/agent-scaffolding/` | `skills/agent-scaffolding/` | Copy directory |
| `skills/autonomy-readiness/` | `skills/autonomy-readiness/` | Copy directory |
| `docs/agents/retro.md` | `docs/retro.md` | Adapt paths/links |
| `docs/agents/icons/retro.png` | `docs/icons/retro.png` | Copy |

### Prioritize agent

| Scaffold path | Agents repo path | Notes |
|---------------|-----------------|-------|
| `agents/prioritize.md` | `agents/prioritize.md` | Copy as-is |
| `harness/prioritize.yaml` | `harness/prioritize.yaml` | Needs adaptation |
| `policies/prioritize.yaml` | `policies/prioritize.yaml` | Copy as-is |
| `schemas/prioritize-result.schema.json` | `schemas/prioritize-result.schema.json` | Copy as-is |
| `scripts/pre-prioritize.sh` | `scripts/pre-prioritize.sh` | Copy as-is |
| `scripts/post-prioritize.sh` | `scripts/post-prioritize.sh` | Copy as-is |
| `scripts/post-prioritize-test.sh` | `scripts/post-prioritize-test.sh` | Copy as-is |
| `docs/agents/prioritize.md` | `docs/prioritize.md` | Adapt paths/links |
| `docs/agents/icons/prioritize.png` | `docs/icons/prioritize.png` | Copy |

---

## Harness adaptation pattern

Every harness YAML needs three categories of changes when moved to
the agents repo. These are the same changes already applied to the
triage agent's harness:

### 1. Move `env/gcp-vertex.env` reference to `common/env/`

```yaml
# Scaffold (before):
host_files:
  - src: env/gcp-vertex.env
    dest: /sandbox/workspace/.env.d/gcp-vertex.env
    expand: true

# Agents repo (after):
host_files:
  - src: common/env/gcp-vertex.env
    dest: /sandbox/workspace/.env.d/gcp-vertex.env
    expand: true
```

`common/env/gcp-vertex.env` already exists in the agents repo. All
agents share this file.

### 2. Add agent-specific env file as host_file

Each agent that has forge-section env vars (`GITHUB_ISSUE_URL`,
`GH_TOKEN`, `TARGET_BRANCH`, etc.) needs a dedicated env file that
exports these variables. This replaces the scaffold's
`forge.github.env.sandbox` mechanism.

Create `env/<agent>.env` with the variables the agent needs:

**Example — `env/code.env`:**
```bash
export GITHUB_ISSUE_URL="${GITHUB_ISSUE_URL}"
export GH_TOKEN=${GH_TOKEN}
```

Then add it to `host_files`:
```yaml
host_files:
  - src: env/code.env
    dest: /sandbox/workspace/.env.d/code.env
    expand: true
```

The specific variables per agent are documented in each agent's
`forge.github.env` section in the scaffold harness.

### 3. Use `validation_loop.schema` instead of `env.runner.FULLSEND_OUTPUT_SCHEMA`

```yaml
# Scaffold (before):
validation_loop:
  script: scripts/validate-output-schema.sh
  max_iterations: 2

env:
  runner:
    FULLSEND_OUTPUT_SCHEMA: ${FULLSEND_DIR}/schemas/<agent>-result.schema.json

# Agents repo (after):
validation_loop:
  script: scripts/validate-output-schema.sh
  schema: schemas/<agent>-result.schema.json
  max_iterations: 2
```

The `validation_loop.schema` field tells the harness to resolve the
schema path relative to the harness file and set
`FULLSEND_OUTPUT_SCHEMA` automatically. This avoids the
`${FULLSEND_DIR}` reference that doesn't resolve correctly for
externally-loaded harnesses.

### 4. Handle remaining `env.runner` / `runner_env` variables

Some agents have additional runner env vars beyond
`FULLSEND_OUTPUT_SCHEMA`. These need to be evaluated case-by-case:

- **Code agent**: `CODE_ALLOWED_TARGET_BRANCHES` — keep in
  `env.runner` (or move to `env/code.env` if appropriate)
- **Fix agent**: `TARGET_BRANCH`, `TRIGGER_SOURCE`,
  `HUMAN_INSTRUCTION`, `FIX_ITERATION`, `REVIEW_BODY_FILE`,
  `PRE_AGENT_HEAD` — these are set by the reusable workflow and
  passed through `env.runner`; keep in harness `env.runner`
- **Retro/Prioritize**: check for any runner env vars

### 5. Retain `forge` section

The `forge.github` section should be preserved in the harness. It
contains forge-specific pre/post script paths and env var mappings
that the harness runtime uses when running on GitHub. The forge
section env vars are the authoritative source for what gets passed
to pre/post scripts on the runner side.

---

## Shared resources in the agents repo

### `common/` directory

Shared files that multiple agents reference go in `common/`. Currently:
- `common/env/gcp-vertex.env` — GCP Vertex AI configuration

### `scripts/validate-output-schema.sh`

This script is shared by all agents. It already exists in the agents
repo. Each new agent reuses it without duplication.

### `scripts/validate-output-schema-test.sh`

This test file validates the schema validation script against multiple
agent schemas. It already references `fix-result.schema.json` and
`review-result.schema.json` — those schemas must exist in the agents
repo for the tests to work. As agents are added, the tests will
naturally cover more schemas.

### `skills/issue-labels/`

Shared between triage and review. Already exists in agents repo.

---

## Execution plan

### Step 1: Extract code agent (PR in agents repo)

1. Run `git filter-repo` to extract code agent files with history
   (see "Preserving git history during extraction")
2. Merge the extracted history into the agents repo
3. Create `env/code.env` with the agent's required env vars
4. Adapt `harness/code.yaml` per the harness adaptation pattern
5. Adapt `docs/code.md` links
6. Verify history: `git log --follow -- agents/code.md`
7. Run `scripts/post-code-test.sh` to verify post-script works
8. Verify `scripts/validate-output-schema-test.sh` still passes with
   the code schema present

### Step 2: Extract fix agent (PR in agents repo)

1. Run `git filter-repo` to extract fix agent files with history
2. Merge the extracted history into the agents repo
3. Create `env/fix.env` with required env vars
4. Adapt `harness/fix.yaml`
5. Adapt `docs/fix.md` links
6. Verify history: `git log --follow -- agents/fix.md`
7. Run post-fix tests

### Step 3: Extract review agent (PR in agents repo)

1. Run `git filter-repo` to extract review agent files with history
   (include `skills/pr-review/`, `skills/code-review/`,
   `skills/docs-review/`; exclude `skills/issue-labels/` — already
   present)
2. Merge the extracted history into the agents repo
3. Create `env/review.env` with required env vars (may already exist)
4. Adapt `harness/review.yaml`
5. Adapt `docs/review.md` links
6. Verify history: `git log --follow -- agents/review.md`
7. Run post-review tests

### Step 4: Extract retro agent (PR in agents repo)

1. Run `git filter-repo` to extract retro agent files with history
   (include `skills/retro-analysis/`, `skills/finding-agent-runs/`,
   `skills/agent-scaffolding/`, `skills/autonomy-readiness/`)
2. Merge the extracted history into the agents repo
3. Create `env/retro.env` (may already exist in scaffold)
4. Adapt `harness/retro.yaml`
5. Adapt `docs/retro.md` links
6. Verify history: `git log --follow -- agents/retro.md`
7. Run post-retro tests

### Step 5: Extract prioritize agent (PR in agents repo)

1. Run `git filter-repo` to extract prioritize agent files with
   history
2. Merge the extracted history into the agents repo
3. Adapt `harness/prioritize.yaml`
4. Adapt `docs/prioritize.md` links
5. Verify history: `git log --follow -- agents/prioritize.md`
6. Run post-prioritize tests

### Step 6: Register agents in fullsend-ai org config (PR in fullsend-ai/.fullsend)

Convert the fullsend-ai organization to use agents from the agents
repo, mirroring what was already done for the triage agent.

The current `fullsend-ai/.fullsend/config.yaml` has:

```yaml
agents:
    - source: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/triage.yaml#sha256=<hash>
allowed_remote_resources:
    - https://raw.githubusercontent.com/fullsend-ai/fullsend/
    - https://raw.githubusercontent.com/fullsend-ai/agents/
```

For each extracted agent, register it the same way triage was
registered — using `fullsend agent add` from the `.fullsend` config
repo checkout, or by manually adding a pinned URL entry to the
`agents:` list.

#### 6a. Add each agent to the config

Run `fullsend agent add` for each agent (from a checkout of
`fullsend-ai/.fullsend`):

```bash
fullsend agent add https://github.com/fullsend-ai/agents/blob/main/harness/code.yaml
fullsend agent add https://github.com/fullsend-ai/agents/blob/main/harness/fix.yaml
fullsend agent add https://github.com/fullsend-ai/agents/blob/main/harness/review.yaml
fullsend agent add https://github.com/fullsend-ai/agents/blob/main/harness/retro.yaml
fullsend agent add https://github.com/fullsend-ai/agents/blob/main/harness/prioritize.yaml
```

Each `agent add` auto-pins the URL to the current commit SHA of
`fullsend-ai/agents` and computes the `#sha256=` integrity hash. The
`allowed_remote_resources` entry for `fullsend-ai/agents/` already
exists, so no allowlist changes are needed.

The resulting `config.yaml` `agents:` section should look like:

```yaml
agents:
    - source: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/triage.yaml#sha256=<hash>
    - source: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/code.yaml#sha256=<hash>
    - source: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/fix.yaml#sha256=<hash>
    - source: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/review.yaml#sha256=<hash>
    - source: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/retro.yaml#sha256=<hash>
    - source: https://raw.githubusercontent.com/fullsend-ai/agents/<sha>/harness/prioritize.yaml#sha256=<hash>
```

To selectively disable an agent, add an `enabled: false` entry:

```yaml
agents:
    - name: retro
      enabled: false
```

#### 6b. Verify agent resolution

Run `fullsend agent list` from the `.fullsend` checkout to verify all
agents resolve correctly and show source as the agents repo URL
instead of scaffold:

```bash
fullsend agent list --fullsend-dir .
```

Expected output: all 6 agents listed with their agents-repo URLs,
overriding the scaffold defaults.

#### 6c. Verify dispatch routing

Confirm that the `.fullsend` config repo's dispatch workflow and
per-stage workflow files (`triage.yml`, `code.yml`, `fix.yml`,
`review.yml`, `retro.yml`, `prioritize.yml`) do not need changes.
The dispatch routes by `# fullsend-stage:` markers in workflow files
and the reusable workflows resolve agents from config at runtime —
the harness source URL is transparent to dispatch.

#### 6d. Smoke test each agent

After the config PR merges, trigger each agent on a test issue/PR in
an enrolled repo (e.g., `fullsend-ai/agents` itself or
`fullsend-ai/experiments`) to verify end-to-end operation:

| Agent | Trigger | What to verify |
|-------|---------|---------------|
| triage | Open a test issue or `/fs-triage` | Agent runs, posts triage comment, applies labels |
| code | Apply `ready-to-code` label or `/fs-code` | Agent runs, creates branch and commits |
| review | Open a PR or `/fs-review` | Agent runs, posts review |
| fix | Bot review with changes_requested or `/fs-fix` | Agent runs, pushes fix commits |
| retro | Close/merge a PR | Agent runs, posts retro analysis |
| prioritize | `/fs-prioritize` | Agent runs, posts priority scores |

#### 6e. Roll out to enrolled repos

Since the `agents:` config is in the org-level `.fullsend` repo,
all enrolled repos under `fullsend-ai` (fullsend, agents,
experiments, metrics) automatically use the new agent sources. No
per-repo changes are needed.

Verify that each enrolled repo's agent runs succeed by monitoring
the first few natural triggers after the config change merges. Check
the GitHub Actions runs in `fullsend-ai/.fullsend` for any failures.

#### 6f. Pin management

After initial rollout, agent URLs are pinned to the commit SHA at
registration time. When the agents repo is updated (bug fixes,
prompt improvements), run `fullsend agent update <name>` to re-pin
to the latest commit:

```bash
fullsend agent update triage
fullsend agent update code
# ... etc
```

This can be automated via a scheduled workflow or done manually after
verifying changes in the agents repo.

### Step 7: Transition to authoritative config and remove scaffold agents (ADR 0058 Phase 5)

**Prerequisite:** All fullsend customers must be upgraded to a
version that supports config-driven agents and have registered
agents-repo agents in their `.fullsend/config.yaml`. Until then,
the scaffold-embedded agents serve as the default fallback — deleting
them would break customers who haven't migrated.

Once all customers are migrated:

1. Update install seeding to use agents repo URLs exclusively
2. Remove scaffold fallback from `MergedAgents()`
3. Remove `HarnessNames()` (or restrict to install-time seeding)
4. Remove `HarnessWrappersLayer`
5. Delete scaffold agent files from `internal/scaffold/fullsend-repo/`:
   - `agents/*.md` (all 6)
   - `harness/*.yaml` (all 6)
   - `policies/*.yaml` (all 6)
   - `schemas/*-result.schema.json` (all 6)
   - `scripts/pre-*.sh`, `scripts/post-*.sh`, `scripts/post-*-test.sh`
     (all agent-specific scripts)
   - `env/code-agent.env`, `env/fix-agent.env`, `env/review.env`,
     `env/retro.env` (agent-specific env files)
   - `env/gcp-vertex.env` (moved to agents repo's `common/`)
   - All skill directories under `skills/` (moved to agents repo)
   - `plugins/gopls-lsp/` (moved to agents repo)
6. Update `internal/scaffold/scaffold.go` — remove deleted files from
   `executableFiles` map
7. Update `Makefile` — remove deleted test scripts from `script-test`
   target
8. Update `scripts/validate-output-schema-test.sh` — it references
   schemas that no longer exist; either update references or move the
   test file entirely to the agents repo
9. Update `docs/agents/*.md` — update source links to point to
   `fullsend-ai/agents`
10. Update test files:
    - `internal/scaffold/scaffold_test.go`
    - `internal/scaffold/baseurl_test.go`
    - `internal/harness/scaffold_integration_test.go`
    - `internal/layers/harnesswrappers_test.go`
    - `internal/scaffold/vendormanifest_test.go`
    - `internal/layers/workflows_test.go`
11. File tracking issue per the ADR plan

---

## Agent-specific env var inventory

Variables each agent needs in its env file (derived from
`forge.github.env.sandbox` in each scaffold harness):

| Agent | Runner vars | Sandbox vars |
|-------|-------------|-------------|
| **triage** | `GITHUB_ISSUE_URL`, `GH_TOKEN` | `GITHUB_ISSUE_URL`, `GH_TOKEN` |
| **code** | `GITHUB_ISSUE_URL`, `GH_TOKEN`, `CODE_ALLOWED_TARGET_BRANCHES` | `GITHUB_ISSUE_URL`, `GH_TOKEN` |
| **fix** | `TARGET_BRANCH`, `TRIGGER_SOURCE`, `HUMAN_INSTRUCTION`, `FIX_ITERATION`, `REVIEW_BODY_FILE`, `PRE_AGENT_HEAD` | `GITHUB_PR_URL`, `GH_TOKEN`, `TARGET_BRANCH` |
| **review** | `GITHUB_PR_URL`, `GH_TOKEN`, `ORG` | `GITHUB_PR_URL`, `GH_TOKEN` |
| **retro** | `GITHUB_PR_URL`, `GH_TOKEN` | `GITHUB_PR_URL`, `GH_TOKEN` |
| **prioritize** | `GITHUB_ISSUE_URL`, `GH_TOKEN`, `ORG`, `PROJECT_NUMBER` | `GITHUB_ISSUE_URL`, `GH_TOKEN` |

The `env/<agent>.env` file in the agents repo should export the
**sandbox** variables (those are what the agent process sees). The
**runner** variables remain in the harness `forge.github.env.runner`
section (they're used by pre/post scripts on the host, not by the
agent itself).

---

## Ordering recommendation

Extract agents in this order, from simplest to most complex:

1. **Prioritize** — simplest: no skills, no plugins, sandbox image
2. **Retro** — read-only agent, sandbox image, unique skills
3. **Code** — has plugins (gopls-lsp), code image
4. **Fix** — shares role/slug with code, complex env vars
5. **Review** — most complex: 4 skills (one shared), 7 sub-agents

Each extraction can be done as an independent PR in the agents repo.
Steps 1–5 are parallelizable (no dependencies between agents), but
doing them in complexity order reduces risk.

---

## Risks and considerations

1. **Shared skill divergence**: `issue-labels` is used by both triage
   and review. It already exists in the agents repo. When review is
   extracted, verify it matches the latest scaffold version. If the
   scaffold version is updated after extraction, both repos need
   syncing until Phase 5 removes the scaffold copies.

2. **Reusable workflows stay in fullsend**: The `reusable-*.yml`
   workflows in `.github/workflows/` remain in the fullsend repo.
   They reference agent harnesses at runtime via config, not via
   scaffold embedding. This is the design from ADR 0058.

3. **Eval framework stays in fullsend**: The `eval/` directory with
   functional test cases stays in the fullsend repo. Evals test the
   harness + agent integration and are run from fullsend CI.

4. **Scaffold workflow templates stay in fullsend**: The per-stage
   workflow stubs (`.github/workflows/triage.yml` etc.) in the
   scaffold are templates with `__REUSABLE_WORKFLOW__` and
   `__GH_RUNNER__` placeholders. They are instantiated during
   enrollment. These stay in fullsend and do not need to be in the
   agents repo.

5. **`validate-output-schema-test.sh` references multiple schemas**:
   The test file currently references `fix-result.schema.json` and
   `review-result.schema.json` via relative paths. All agent schemas
   need to be present in the agents repo for these tests to pass.
   This is naturally resolved as agents are extracted.

6. **AGENTS.md in scaffold**: The scaffold's `AGENTS.md` file
   contains shared agent rules that all agents reference. This file
   should be copied to the agents repo or made available via the
   harness layering mechanism.

7. **`validate-output-schema.sh` and `validate-output-schema-test.sh`
   are shared**: These scripts are agent-agnostic. They already exist
   in the agents repo. As more schemas are added, the test file
   exercises more of them.
