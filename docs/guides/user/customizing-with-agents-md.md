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
