---
name: author-fullsend-augmentations
description: >-
  Guide for writing fullsend augmentation skills and sub-agents that work
  alongside shipped defaults. Discovers current agents, skills, schemas, and
  sub-agent rosters from fullsend-ai/agents and fullsend-ai/fullsend — never
  from a hardcoded inventory.
---

# Author fullsend augmentations

Help users write skills (and sub-agents) that **augment** default fullsend
agent behavior without fighting it. An augmentation adds domain knowledge,
tightens constraints, or reformats output — it does not replace the default
skill's procedure unless the user explicitly chooses override.

## When to use

- The user wants to create a new skill for their repo to augment an existing
  agent or skill shipped by fullsend.
- The user has a skill that is being ignored or overshadowed by a default
  skill.
- The user wants to understand what a default skill or sub-agent already
  covers before writing their own.
- The user wants to add or extend a **sub-agent** under an orchestrator skill
  (for example review).
- The user is deciding whether to augment or fully override a default skill.

## Hard rule: discover, do not memorize

Shipped agents, skills, harnesses, schemas, and sub-agent rosters **change**.
Do **not** rely on remembered names or static tables in this skill.

## Hard rule: ask where it ships before writing files

Discovery uses `AGENTS_ROOT` / `FULLSEND_ROOT` (or shallow clones) **only to
read** harnesses, schemas, and shipped skills.

**Never** create or edit augmentation skills under:

- The discovery checkout (`AGENTS_ROOT`, `FULLSEND_ROOT`)
- A neutral test or discovery directory unless the user
  explicitly says that directory is the shipping target
- The current working directory by default

Before `mkdir`, `write`, or "create the skill", ask with **concrete choices**
(numbered options the user can pick, plus a type-your-own path) — not a blank
open question and not a stop with no options:

1. **What artifact?** (see **Hard rule: classify artifact** below — sub-agent
   vs augmentation skill vs org fork)
2. **Which repo gets it?** (app repo, org skills repo, `.fullsend`
   config repo, or upstream contribution)
3. **Which placement?** (see step 6 — repo `.agents/skills/` vs org harness
   URL vs `sub-agents/` under an orchestrator)

Until the user names the target path, **output the draft in chat only** (or
write to a path the user gives you — their org skills repo, app repo, or
`.fullsend` config repo).

## Hard rule: classify artifact before writing

Sub-agents and augmentation skills are **different file shapes**. Classify
from the user's words **before** creating directories. State your choice in
one line (for example: "Artifact: sub-agent under `pr-review`").

| User intent | Create this | Also required (fixed roster) | Do **not** create |
|-------------|-------------|------------------------------|-------------------|
| "sub-agent for `pr-review`" / "new review dimension" | `<orchestrator>/sub-agents/<name>.md` (plus parent edits **or** file-level override if discovery shows it) | Discover shipping mechanism (upstream / file-level / whole-skill fork) | `<name>/SKILL.md` wrapper skill |
| "skill to shorten retro/triage output" / domain rules | `skills/<unique-name>/SKILL.md` | Harness `skills:` pin if org-wide | Files under `sub-agents/` |
| "replace orchestrator roster/procedure" | Whatever discovery says is required (often whole-skill fork **today**) | Derived harness in `.fullsend` when replacing the skill entry | A lone sub-agent file without a dispatch path |

**Anti-pattern:** User asked for a **sub-agent** → you created
`wrapper-name/SKILL.md` (or any `*/SKILL.md`) with the dimension
embedded inside. That is wrong. Sub-agent content belongs in
`sub-agents/<name>.md` beside siblings like `correctness.md`, matching their
frontmatter and **Own** / **Do not own** sections.

**Anti-pattern:** User asked for a **sub-agent** on a **fixed roster** → you
only added `sub-agents/new.md` without editing the parent `SKILL.md` roster,
keyword table, and dispatch steps. That file will never run.

If ambiguous ("add a new dimension to review"), ask with concrete choices:
**new sub-agent** vs **separate augmentation skill** — short descriptions
and a type-your-own option.

Always fetch current facts from:

| Repo | Why |
|------|-----|
| [`fullsend-ai/agents`](https://github.com/fullsend-ai/agents) | Runtime harnesses, agent defs, skills, schemas, scripts, sub-agents |
| [`fullsend-ai/fullsend`](https://github.com/fullsend-ai/fullsend) | User docs, ADRs, scaffold copies, CLI/config guidance |

Prefer a local clone when available; otherwise use `gh api` / raw GitHub URLs
against `main` (or the pin the user's org already uses).

**Do not confuse** fullsend's repo-root `skills/` / `.claude/skills/` (local
Cursor/Claude tooling) with **agent sandbox skills** under `agents` →
`skills/`.

## How composition works

Multiple skills can load in one session. On overlapping topics, the model
follows the **more specific** language. Vague augmentation ("be concise")
loses to hard defaults every time.

### Skill loading and name collisions

Read the current wording in fullsend:

[`docs/guides/user/customizing-with-skills.md`](https://github.com/fullsend-ai/fullsend/blob/main/docs/guides/user/customizing-with-skills.md)

**Same name as a built-in:** If your repo skill directory matches a built-in
skill name (for example `retro-analysis`), the agent **never loads your
version** — the built-in wins and Fullsend logs a warning. Use a **unique
directory name** (for example `my-org-retro`, not `retro-analysis`) to extend
the agent, or use `base:` harness composition for an intentional override.

**Different name:** Your skill loads **next to** built-ins. To change behavior,
be **more specific** than the default — exact fields, word limits, templates.
Do not rely on soft guidance like "prefer concise."

Also read the **agent definition** — it often owns output fields more tightly
than any skill.

## Procedure

### 0. Find or clone the source repos

Before discovery, point at local clones of `fullsend-ai/agents` and
`fullsend-ai/fullsend`. If they are not on disk, shallow-clone them to a temp
path. Everything below reads from those trees — not from memory.

```bash
# Agents repo (runtime source of truth)
AGENTS_ROOT="${AGENTS_ROOT:-}"   # set if already cloned
if [[ -z "$AGENTS_ROOT" ]]; then
  # try common local paths, else clone shallow
  for d in "$HOME/development/agents" ./agents; do
    [[ -d "$d/harness" && -d "$d/agents" && -d "$d/schemas" ]] && AGENTS_ROOT="$d" && break
  done
fi
if [[ -z "$AGENTS_ROOT" ]]; then
  git clone --depth 1 https://github.com/fullsend-ai/agents.git /tmp/fullsend-agents
  AGENTS_ROOT=/tmp/fullsend-agents
fi

# Fullsend repo (docs + scaffold)
FULLSEND_ROOT="${FULLSEND_ROOT:-}"
if [[ -z "$FULLSEND_ROOT" ]]; then
  for d in "$HOME/development/fullsend" ./fullsend; do
    [[ -d "$d/docs/guides/user" ]] && FULLSEND_ROOT="$d" && break
  done
fi
if [[ -z "$FULLSEND_ROOT" ]]; then
  git clone --depth 1 https://github.com/fullsend-ai/fullsend.git /tmp/fullsend-core
  FULLSEND_ROOT=/tmp/fullsend-core
fi
```

### 1. Discover the target agent and what it already loads

Ask which agent(s) the user wants to affect. Then **list** — do not assume.

```bash
cd "$AGENTS_ROOT"

# Agents that have harnesses today
ls harness/*.yaml

# Skills each harness loads (authoritative for that checkout)
rg -n '^skills:' -A 20 harness/<agent>.yaml

# Agent system prompt / field contracts
sed -n '1,120p' agents/<agent>.md

# Output schema = required/optional JSON fields
cat schemas/<agent>-result.schema.json

# What the post-script posts to humans (comment vs issues vs review body)
rg -n 'jq -r|SUMMARY|COMMENT|body|findings|gh api|gh pr' scripts/post-<agent>.sh
```

Also skim fullsend user docs for extension points (names that unlock
behavior when present even if not in the harness `skills:` list):

```bash
cd "$FULLSEND_ROOT"
rg -n 'extension point|skills/|\.agents/skills' docs/guides/user/customizing-with-skills.md docs/agents/<agent>.md
```

For each overlapping skill you find, open its `SKILL.md` and note:

- Fields or outputs it owns
- Procedures / steps it defines
- Hard rules vs soft guidance
- Whether it orchestrates **sub-agents** (next step)

### 1b. Discover sub-agents (when relevant)

Some skills are orchestrators. They dispatch specialized sub-agents whose
definitions live beside the skill.

```bash
cd "$AGENTS_ROOT"

# Find every shipped sub-agent tree
find skills -type d -name sub-agents

# List roster for a skill (example pattern — use whatever find returns)
ls skills/<orchestrator-skill>/sub-agents/

# How the parent selects/dispatches them
rg -n 'sub-agent|sub_agents|roster|Dispatch' skills/<orchestrator-skill>/SKILL.md

# Read a sub-agent's ownership contract (frontmatter + Own/Do not own)
sed -n '1,80p' skills/<orchestrator-skill>/sub-agents/<name>.md
```

**Before inventing a new sub-agent:**

1. Confirm the parent skill's dispatch model:
   - **Fixed roster / named selection** → dropping a new file alone is not
     enough; the parent `SKILL.md` must list and dispatch the new name.
     See **Fixed-roster orchestrators** below — do not suggest legacy overlay
     directories.
   - **Directory discovery** → a uniquely named file may be enough; verify
     in the parent `SKILL.md`.
2. Check for an existing sub-agent that already owns the dimension
   (read **Own** / **Do not own** sections). Prefer augmenting that
   sub-agent's instructions over cloning a parallel one.
3. Match shipped frontmatter conventions (name, description, model, tools,
   permissionMode, etc.) by copying structure from a sibling file — still
   read a current sibling; do not invent fields from memory.

#### Fixed-roster orchestrators (for example `pr-review`)

If discovery shows a **fixed roster** (parent `SKILL.md` names sub-agents
explicitly), a lone `sub-agents/<name>.md` is never enough by itself — the
parent must learn the name **or** the platform must support merging that file
into the upstream skill tree.

**Do not hardcode today's shipping options.** After reading the parent
orchestrator, discover what fullsend currently supports for intra-skill /
sub-agent customization:

```bash
cd "$FULLSEND_ROOT"
# User-facing guidance (preferred path lives here when features ship)
rg -n 'file-level|sub-agent|override|skills:' \
  docs/guides/user/customizing-with-skills.md \
  docs/guides/user/customizing-agents.md \
  docs/agents/topics/default-vs-custom.md

# Whether harness skills: accepts object/map overrides (schema + types)
rg -n 'SkillEntry|file_overrides|Overrides' \
  docs/ ADRs/ internal/harness/ 2>/dev/null | head -40
```

Present shipping choices from **what discovery found**, not from memory.
Typical families (names and availability change — verify each run):

| Family | When it applies | What to ship |
|--------|-----------------|--------------|
| **Upstream** | Change helps everyone | Sub-agent + parent roster/dispatch in `fullsend-ai/agents` |
| **File-level skill override** | Docs/schema show per-file add/replace inside a pinned skill | Only the changed file(s) (e.g. `sub-agents/<name>.md`) + harness `skills:` override entry; leave the rest upstream |
| **Whole-skill / org fork** | No file-level merge yet, or you must replace most of the tree | Full orchestrator directory in the org skills repo + harness pin that **replaces** (not appends) the upstream skill |
| **Augment without roster change** | Constraints on an existing dimension only | Unique-name skill; no new roster slot |

**Prefer** the lightest mechanism discovery shows as supported and documented.
When file-level overrides exist in the checkout you are reading, prefer them
over whole-skill forks for single-file / few-file changes.

**Harness `skills:` merge caveat (verify in [Harness Field Reference](../../docs/contributing/harness-fields.md)):**
`base:` composition merges skill entries with deduplication by basename —
a child entry whose directory name matches a base skill **replaces** it
(child wins), not loads alongside. Say that plainly when recommending a fork.

**Do not recommend** `customized/` or `.fullsend/customized/` overlay paths.

When the user wants a new sub-agent but has not named the dimension: after the
roster summary, ask with **concrete choices** (Performance, Observability,
… plus type-your-own), derived from Own/Do-not-own gaps — not a blank
"what do you want?"

### 1c. Map human-facing surfaces

JSON length ≠ human noise. Derive surfaces from **schema + post-script**,
not from guesses:

| Question | Where to look |
|----------|----------------|
| What is posted as an issue/PR comment? | `scripts/post-<agent>.sh` fields read via `jq` |
| What becomes separate GitHub issues? | Same post-script — loops that `gh issue create` |
| What is review body vs findings? | Review schema + post-review / pr-review skill |

Compress only the human-facing field you intend to change. Leaving detail
fields intact (for example retro proposal bodies) is often correct.

### 2. Classify the change

| Intent | Approach |
|--------|----------|
| Domain knowledge no default covers | **New capability** — low conflict risk |
| Change how an output field *looks* | **Augmentation** — declare field ownership |
| New review/analysis dimension | **Sub-agent** — discover how to register it (file-level override vs fork vs upstream) |
| Replace a default skill's procedure or most of a skill tree | **Whole-skill override / derived** — only when discovery shows no lighter path |
| Constraints on top of default behavior | **Augmentation** — hard rules |

For derived harnesses and agent registration, read **only** these fullsend
entry points (skip "layered configuration" / "overriding built-in skills"
sections in older user guides — they describe removed overlay mechanics):

- `docs/agents/topics/default-vs-custom.md` — augment vs derived
- `docs/ADRs/0045-forge-portable-harness-schema.md` — `base:` merge rules
- `docs/ADRs/0058-agent-registration.md` — registering harness URLs
- `docs/guides/user/building-custom-agents.md` — new or derived agents

Use `docs/guides/user/customizing-with-skills.md` for repo skill layout,
built-in skill names, **and** whatever override mechanisms that guide
currently documents (re-read it every run — it changes when features ship).

### 2b. Lock artifact type (mandatory — do before step 4)

From the user's request and step 2, pick **exactly one** primary artifact:

```
sub-agent.md  |  augmentation SKILL.md  |  org-fork orchestrator tree
```

Confirm aloud:

- **Sub-agent:** parent orchestrator name (for example `pr-review`), sub-agent
  filename (for example `performance.md`), fixed roster? (yes → path A or B
  from 1b)
- **Augmentation skill:** unique skill directory name (not a built-in name)
- **Org fork:** which orchestrator directory is copied wholesale

If the user said **sub-agent**, step 4 follows the **Sub-agent branch** only.
Do not open the Skills branch unless they change the request.

### 3. Analyze for conflicts

Compare the user's intent/draft to everything discovered above:

- **Field ownership collision** — two instructions dictate the same field
- **Procedure overlap** — two step lists for the same task
- **Tone conflict** — soft ("prefer") vs hard ("must" / "≤ N words")
- **Scope ambiguity** — "shorten everything" vs fields that must stay complete
- **Agent-def collision** — agent markdown already hard-requires content
- **Sub-agent roster collision** — new dimension already owned; or parent
  will never dispatch an unknown name
- **Wrong artifact** — user asked for sub-agent but draft is a `SKILL.md` or
  wrapper skill; user asked for augmentation skill but draft is `sub-agents/`

### 4. Write the deliverable (branch on artifact from step 2b)

**Stop.** If you have not stated artifact type and target repo, do not write
files.

#### Sub-agent branch (when user asked for a sub-agent)

**File to create:** `<target>/skills/<orchestrator>/sub-agents/<name>.md`

**Not** `<target>/skills/<name>/SKILL.md`. **Not** a harness line that loads a
new wrapper skill unless the user explicitly asked for a separate augmentation
skill.

1. Read two siblings from discovery (for example `correctness.md`, `security.md`).
2. Copy their frontmatter shape (`name`, `description`, `model`, `tools`,
   `permissionMode`, `background`, etc.) — do not invent fields.
3. Write **Own** / **Do not own** for the new dimension.
4. If fixed roster (step 1b): ensure the new name is dispatchable — usually
   parent `SKILL.md` roster + keyword + dispatch edits **in the same change
   set**, unless discovery showed a supported file-level override / registration
   path that covers dispatch without a full parent fork.
5. Placement: use the shipping family discovery selected (upstream,
   file-level override, or whole-skill fork) — not a silent drop under the
   target app's `.agents/skills/` unless discovery says that works.

**Sub-agent deliverable checklist (fixed roster):**

- [ ] `sub-agents/<name>.md` matches sibling conventions
- [ ] Dispatch path verified from **current** parent skill + platform docs
      (roster edit, file-level override, or other discovered mechanism)
- [ ] No new `<name>/SKILL.md` wrapper directory was created
- [ ] Harness / pin matches the chosen shipping family

#### Augmentation skill branch (when user asked for a skill, not a sub-agent)

Use this branch for output-format constraints, domain rules, and
capabilities that load **alongside** defaults — not for new `pr-review`
dimensions (those are sub-agents).

**File to create:** `<target>/skills/<unique-name>/SKILL.md` (or repo
`.agents/skills/<unique-name>/SKILL.md`)

##### Skills — required patterns

**Declare field ownership**

```markdown
## What this skill controls
- <field>: <aspects>

## What this skill does NOT control
- <fields owned by discovered defaults>
```

**Hard limits, not soft preferences**

| Weak | Strong |
|------|--------|
| Prefer shorter comments | Limit `<field>` to N words maximum |
| Try to avoid filler | Never start with "Thanks" / "I've reviewed" |
| Consider bullets | Use bullets, not paragraphs |

**Templates + before/after** for the exact field you own.

**Fail checks** the model can self-apply before finishing.

**Acknowledge defaults by name** (use the names you discovered):

```markdown
`<discovered-skill>` owns <X>.
This skill owns <field> <aspect> only.
```

#### Sub-agents — patterns (reference only when on sub-agent branch above)

The sub-agent branch in step 4 is authoritative. This list is a summary:

- Unique `name` in frontmatter; no clash with `ls sub-agents/`
- Explicit **Own** / **Do not own** (copy the style of a sibling)
- Stay inside the parent orchestrator's expected output contract
  (read how the parent parses sub-agent results)
- Fixed roster: parent `SKILL.md` edits in the same change set — never a
  standalone sub-agent file alone

### 5. Review the draft

**Artifact check (always):**

- [ ] Deliverable matches step 2b (sub-agent `.md` vs skill `SKILL.md` vs fork)
- [ ] No wrapper `SKILL.md` when user asked for sub-agent

**All artifacts:**

- [ ] **Discovered**, not memorized — harness/schema/skill/sub-agent reads
      are from current `agents` / `fullsend` checkouts
- [ ] **Unique name** — no collision with `ls skills` / `ls sub-agents`
- [ ] **Field or dimension ownership declared**
- [ ] **Human surface named** (from post-script / schema)
- [ ] **Hard language only**
- [ ] **Defaults acknowledged by discovered names**
- [ ] **Templates + fail checks** for format changes
- [ ] **No procedure overlap** with built-in skills
- [ ] **Fits agent definition + schema**
- [ ] **Sub-agent dispatch path verified** (parent will actually run it)

### 6. Recommend placement (then write only in the named target)

Ask which row applies **before** creating directories. Do not assume the
current project is the shipping repo.

| Setup | Where it lives | How it loads (verify in current docs) |
|-------|----------------|----------------------------------------|
| **Per-repo augmentation skill** | Target app `.agents/skills/<name>/SKILL.md` | Auto-discovered for agents on that repo |
| **Org-wide skill** | Org skills repo `skills/<name>/` | `.fullsend` harness `skills:` URL + pin |
| **Sub-agent / intra-skill file** | Path discovery recommends (upstream tree, file-level override target, or whole-skill fork) | Whatever `customizing-with-skills.md` + harness schema currently describe |

**Repo skill** — one-repo domain rules.

**Org harness skill** — org-wide unique skill name + harness pin + `#sha256=`.

**Sub-agent / fixed roster** — re-run the shipping-family discovery in step 1b;
prefer the lightest supported path. Do not assume whole-skill fork is required
forever.

Primary docs to re-read every run:

`fullsend` → `docs/guides/user/customizing-with-skills.md`

## Augment vs. override decision

1. After discovery, does a default skill, sub-agent, or agent def already
   cover this?
   - **No** → New augmentation / new capability. Low conflict risk.
   - **Yes** → Continue.
2. Can you constrain or reformat output without changing roster or procedure?
   - **Yes** → **Augment**: unique skill name + field ownership + hard limits.
     Append via harness `skills:` + pin.
   - **No** → Continue.
3. Must a sub-agent or orchestrator file change?
   - **Benefits everyone** → **Upstream** to `fullsend-ai/agents`.
   - **Org/repo-only** → Use the **lightest shipping family discovery found**
     (file-level override when available; whole-skill fork only if that is
     still the only supported path).
4. Never suggest overlay directories under `customized/` — use harness mechanisms
   documented in the current checkout.

## Common mistakes

| Mistake | Fix |
|---------|-----|
| Soft language ("prefer concise") | Hard limits ("N words maximum") |
| Hardcoding today's skill list into your notes as eternal truth | Re-run discovery against `agents` / `fullsend` |
| Looking in fullsend root `skills/` for agent skills | Use `fullsend-ai/agents` `harness/` + `skills/` |
| Shortening backlog/detail fields to make comments "feel" short | Change only the human-facing field from the post-script |
| Adding a sub-agent file the parent never dispatches | Read parent roster/selection; update parent or upstream it |
| User asked for sub-agent; you created `<name>/SKILL.md` | Use `sub-agents/<name>.md` + parent roster edits; no wrapper skill |
| Redefining default procedures | Constrain outputs; don't replace steps |
| Same directory name as a built-in | Rename to extend, or override through `base:` harness composition; Fullsend warns that the repo skill is shadowed |
| Suggesting `customized/` or overlay dirs for overrides | Use harness mechanisms from current docs (file-level when available) |
| Hardcoding "org fork" as the only sub-agent path | Re-discover shipping families each run |
| Writing into discovery or test cwd | Ask target repo; draft in chat until user names path |
| Ignoring agent definition + schema | Read both every time |
| Hand-waved integrity hashes on harness pins | Compute tree hash with platform tooling |
| Overriding when augmenting would work | Prefer augmentation to keep upstream improvements |
