# Customizing Agents with AGENTS.md

Fullsend agents operate on your repository using Claude Code inside a sandboxed
environment. Because agents run with your repo checked out, they automatically
read its `AGENTS.md` file — the same file human contributors use. No fullsend
configuration changes needed.

For agent-specific customization using skills, see
[Customizing with Skills](customizing-with-skills.md).

## What to put in AGENTS.md

`AGENTS.md` is the [open standard](https://agentskills.io/) that any agent
tool can discover. The recommended approach is to keep your `CLAUDE.md`
lightweight and have it point at `AGENTS.md`:

```markdown
# CLAUDE.md

See AGENTS.md for contributor conventions (human and agent alike).
```

Add instructions that apply to anyone (human or agent) working in your repo:

```markdown
# AGENTS.md

## Testing
- Always run `make test` before committing.
- Integration tests require `docker compose up -d` first.

## Code style
- Use structured logging via `slog`. Do not use `log.Printf`.
- All public functions must have doc comments.

## Architecture
- The `internal/api/` package is the HTTP layer. Business logic belongs in `internal/service/`.
- Never import `internal/service/` from `internal/api/` — use interfaces.
```

These instructions influence every agent:

- **Triage** reads them to understand your project's architecture and conventions
  when assessing whether an issue has enough context.
- **Code** follows them when implementing features — it will run `make test`,
  use `slog`, and put code in the right packages.
- **Review** checks PRs against them — if a PR uses `log.Printf`, the review
  agent flags it.
- **Fix** reads them when addressing review feedback to avoid introducing new
  violations while fixing old ones.

## Examples

### Enforcing a migration review checklist

You want the review agent to check every PR that touches database migrations
against a specific checklist:

```markdown
## Database migrations
When reviewing PRs that add or modify files in `db/migrations/`:
- Verify the migration is reversible (has both up and down).
- Check that no migration drops a column that is still referenced.
- Confirm the migration number does not conflict with existing ones.
- Flag any `ALTER TABLE` on large tables that could lock production.
```

### Guiding the code agent's test strategy

```markdown
## Test conventions
- Use table-driven tests with `t.Run` subtests.
- Name test cases descriptively: `"returns error when input is empty"`, not `"test1"`.
- Place test helpers in `_test.go` files, not in a `testutil` package.
- Mock external services using interfaces, not monkey-patching.
```

### Steering triage with domain context

Your repo has a complex domain model and triage often miscategorizes issues:

```markdown
## Domain context
- "Reconciler" always refers to the Kubernetes controller in `internal/controller/`.
- "Pipeline" means the CI/CD pipeline, not the data pipeline in `internal/etl/`.
- Issues mentioning "flaky" are almost always about `internal/e2e/` tests.
- The `api/` directory is auto-generated from protobuf — never modify it directly.
```

## How AGENTS.md interacts with agent definitions

Fullsend agents have two layers of instructions, loaded through different
mechanisms:

1. **Agent definition** — the system prompt loaded via `--agent <name>`.
   Fullsend controls this. It defines the agent's role, task, allowed tools,
   model, and which built-in skills to load. Repos cannot modify it.

2. **Project instructions** — `CLAUDE.md` and `AGENTS.md` auto-loaded from
   the working directory. Your repo controls these. They provide conventions,
   architecture context, and domain knowledge.

These layers **compose** — they don't compete. The agent definition sets
*what* the agent does (review code, implement a feature). Your AGENTS.md
sets *how* it should work in your repo (test commands, code style, domain
context). If AGENTS.md contradicts the agent definition, the agent definition
takes precedence.

### What AGENTS.md can do

- Guide agent behavior within its defined role (coding conventions, test
  strategy, architecture rules)
- Reference repo skills by name — the agent will invoke them if they exist
  in `.claude/skills/`
- Provide domain context that helps the agent make better decisions

### What AGENTS.md cannot do

- Override the agent definition's tool restrictions (e.g., the review agent
  cannot write files regardless of what AGENTS.md says)
- Remove or replace built-in skills — use
  [`customized/skills/`](customizing-with-skills.md#overriding-built-in-skills)
  for that
- Change the agent's model or execution parameters

### Injection handling

When the target repo has no AGENTS.md, fullsend injects an org-level default
from the config repo. When the repo has AGENTS.md but no CLAUDE.md, fullsend
injects a bridge CLAUDE.md that points to AGENTS.md. Both injected files are
hidden from git so agents don't accidentally commit them.

All repo context files (AGENTS.md, CLAUDE.md, SKILL.md) are scanned for
prompt injection before the agent starts.

## What not to do

- **Don't write agent-specific instructions.** All agents read the same
  `AGENTS.md`, so write instructions as if they're for any contributor.
  This is a feature — the same conventions apply to humans and agents alike.
- **Don't put label glossaries or skill-specific knowledge here.** That
  bloats context for every agent. Use
  [skills](customizing-with-skills.md) instead.
- **Don't make AGENTS.md a monolith.** Use progressive disclosure — put
  detailed context in the package directory where it's relevant rather than
  loading every agent with everything. For example, database migration
  review checklists belong in `db/migrations/AGENTS.md`, not the root file.
