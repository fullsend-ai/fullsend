# Adopting fullsend incrementally

How to introduce fullsend to your team incrementally, building trust at each stage before taking on more automation.

This guide describes a suggested adoption path based on the fullsend team's own experience dogfooding the platform. Each team's path will differ depending on their workflow, tooling, and needs. What you read here is not a manual — it's a map.

It's written for team leads evaluating how to roll out fullsend, and for developers who want to understand what to expect as usage evolves.

Each stage below describes what to enable, what to observe, and soft signals that you might benefit from the next stage. For installation mechanics, see the [getting started guide](../getting-started/).

## Before You Start

### Install fullsend

Before enabling any agents, you need to get fullsend running in your environment:

1. [Enroll](../getting-started/) your org or repo in a token mint
2. [Provision inference access](../getting-started/getting-inference.md)
3. [Configure GitHub](../getting-started/configuring-github.md) — Apps, permissions, webhooks
4. Optionally set up [org mode](../getting-started/org-mode.md) for multi-repo management

These guides walk through each step in detail. Once installed, you're ready to prepare your repo.

### Prepare Your Repo

The following items benefit any team regardless of how far you go with fullsend. Think of these as good engineering hygiene that happens to make agents more effective — not fullsend-specific requirements.

**AGENTS.md and contributing guides** — Agents read these the same way human contributors do. If your contributing guide is outdated or missing, agents will make the same mistakes a new team member would. Update it to reflect how your team actually works. See [Customizing with AGENTS.md](customizing-with-agents-md.md) for guidance.

**Test coverage** — Agents rely on tests to validate their work. Review agents check test results, code agents run tests to confirm fixes. Low coverage means agents can't tell if they broke something. You don't need 100%, but critical paths should be covered.

**CI pipeline** — Agents use CI signals to assess their own output. Flaky or slow CI means flaky or slow agent feedback loops.

**Branch protection and CODEOWNERS** — These are your guardrails. Agents respect them. Make sure they reflect what actually needs human approval.

**Understand where fullsend intersects your SDLC** — The stages below assume a GitHub-native workflow as the default example. If your team manages issues in Jira, or uses a different review process, map each stage to your own tooling. Some agents might not apply, or might need customization before they're useful.

**Resource note for public repos** — Fullsend requires write access to trigger agents, so external contributors can't trigger agent runs. However, PRs from unknown authors still trigger CI workflows that consume runner minutes. If your repo accepts external contributions, consider a gating workflow that closes PRs from non-approved contributors before other workflows fire. See the fullsend repo's vouch system for an example.

## Crawl — Observe and Evaluate

**What you enable:**

- **Triage agent** — labels and comments on issues, checks for duplicates, identifies missing information
- **Prioritize agent** — suggests priority based on issue content and repo context
- **Review agent** — comments on human-opened PRs with findings

**What you do:**

Watch agent output for a few weeks. Are triage labels accurate? Are review findings useful? Are priorities reasonable?

Identify where agents lack context — are they misunderstanding your architecture, coding conventions, or domain? Note what's missing for the next stage.

Talk to your team — do people find the feedback helpful or noisy?

**Optional: use fix agent on-demand.** The fix agent responds to `/fs-fix` on any PR. When the review agent flags issues on a PR you authored, you can invoke `/fs-fix` to let the agent address the findings instead of fixing manually. This is a low-risk way to experience agent-driven code changes while staying fully in control — the agent only touches the existing PR, and only when you ask. See the [fix agent documentation](../../agents/fix.md) for details.

**Note:** Not all of this may apply to your team. If you manage issues outside GitHub, skip triage and prioritize — start with review. The point is to start with agents that observe and advise without changing your workflow.

**Soft signals you might benefit from Walk:**

- You find yourself wishing agents understood more about your repo
- Review findings are relevant but miss project-specific patterns
- You've identified repo-specific context that would help agents perform better

## Walk — Fine-Tune and Provide Context

**What you do:**

Based on what you observed in Crawl, give agents more context about your repo and workflow. Default agents are designed to work well out of the box — this stage is about helping them work even better for your specific codebase.

The most common and lightweight mechanisms:

**AGENTS.md** — add architecture context, coding conventions, and domain knowledge. This is usually the highest-leverage change — agents read it the same way a new team member would.

**Environment variables** — quick behavioral tuning. For example, `REVIEW_FINDING_SEVERITY_THRESHOLD=medium` to adjust review sensitivity.

For teams with specific needs, fullsend offers deeper customization:

- **Skills** — extend agents with repo-specific capabilities, or replace built-in skills with your own. See [customizing with skills](customizing-with-skills.md).
- **Policies** — adjust network access and security posture for your environment.
- **Sandbox image** — add tooling your agents need (linters, schema validators, domain-specific CLIs).
- **Harness config** — tune the execution environment, timeouts, validation loops.

Not every team will need all of these. Many teams find that a good AGENTS.md and a few environment variable tweaks are enough. See [customizing agents](customizing-agents.md) for a full overview, or [running agents locally](running-agents-locally.md) to test changes in your own environment.

**Enable the retro agent** — it reviews agent runs and surfaces systematic problems, so you're not manually auditing agent behavior anymore. Its post-script files issues from retro findings automatically, following your team's conventions — right labels, context, and acceptance criteria.

**Custom agents for specific SDLC gaps:** If your workflow uses tooling outside GitHub that default agents don't cover, this is where you might start exploring derived or custom agents — for example, a triage agent that bridges to Jira, or a prioritization agent that reads from your planning tool. Most teams won't need this. See [building custom agents](building-custom-agents.md) and [default vs custom agents](../../agents/topics/default-vs-custom.md) for guidance.

**What you're learning:**

- Whether adding repo context improves agent output quality
- Which tuning mechanisms give you the most leverage for your repo

**Soft signals you might benefit from Run:**

- You trust triage and review output enough that you rarely override them
- Your team is comfortable with agents participating in the workflow
- You have good test coverage and CI, so you'd feel confident reviewing agent-generated code

## Run — Agent-Driven Development

**What you enable:**

- **Code agent** — automatically picks up issues labeled `ready-to-code` (applied by triage for bugs, docs, and performance issues) and implements fixes, opening PRs. For features and other issue types, someone explicitly invokes `/fs-code` on the issue. This means enabling the code agent doesn't mean agents code on everything — it's scoped to bugs, docs, and performance issues by default, with human control over the rest.
- **Fix agent on bot-authored PRs** — the fix agent auto-triggers on bot-authored PRs when the review agent requests changes. Since the code agent now produces bot-authored PRs, the review-fix loop runs automatically on agent work. For human-authored PRs, auto-fix requires the `fullsend-fix` label — or you can continue using `/fs-fix` on demand.

**Preparation that pays off here:**

**Retro agent loop** — the retro agent (enabled in Walk) has been surfacing problems and filing issues. With the code agent active, those issues can now feed directly into the code agent, closing the loop.

**PR conventions** — specify your merge strategy (squash / rebase / merge), commit message format, and PR description expectations in AGENTS.md or a skill. Agent PRs stay consistent with how your team works.

**CI checks knowledge** — document your CI pipeline in skills so the code agent knows what checks run, what linters are enforced, what test suites must pass. The more the code agent knows upfront, the fewer round-trips through review and fix.

**What changes for the team:**

- Agent-authored PRs start appearing alongside human-authored ones. Bugs, docs, and performance issues are handled by agents; features and other work remain human-driven (unless explicitly triggered with `/fs-code`).
- The review and fix loop runs automatically on agent PRs — review agent flags issues, fix agent addresses them, review runs again.
- The team now reviews both human and agent PRs.

**What you do:**

Review agent-opened PRs like you'd review a new contributor's work. Trust builds over time.

Watch for patterns — if the code agent consistently struggles with a certain area of the codebase, add more context or skills.

Use retro agent findings (now filed as proper issues) to drive systematic improvements.

See [bugfix workflow](bugfix-workflow.md) for the full agent-driven flow from issue to merge.

**Soft signals you might benefit from Fly:**

- You approve most agent PRs with minor or no changes
- You find yourself wanting to auto-merge certain low-risk PRs rather than manually approving them

## Fly — Mastery and Autonomy

**Two dimensions of Fly:**

### Progressive Auto-Merge

Auto-merge can be achieved today using GitHub's native features and CODEOWNERS. The approach:

1. Add the fullsend review bot as a CODEOWNER for specific low-risk paths (e.g., `docs/**`).
2. Enable GitHub auto-merge on the repo.
3. Configure branch protection to require CODEOWNERS approval and CI passing.

When a PR only touches paths where the bot is a CODEOWNER, its approval satisfies the required review. CI passes, and GitHub auto-merges — no human approval needed for that scope.

Start small — docs-only paths, or dependency update paths already covered by Renovate / Dependabot. Expand gradually by adding more paths to the bot's CODEOWNERS entries as trust builds.

The team always retains control — CODEOWNERS and branch protection are the safety net, and auto-merge scope can be dialed back at any time by editing CODEOWNERS.

For more granular control, PR-level risk assessment ([#4698](https://github.com/fullsend-ai/fullsend/issues/4698)) will add a composite risk score that can further gate auto-merge eligibility based on change size, path sensitivity, and git history signals — not just file paths.

### Bring Your Own Agents

Fullsend is also an agent runtime infrastructure: teams can build and deploy custom or derived agents alongside the defaults.

- Custom agents for domain-specific tasks (schema migration validation, API compatibility checks, release note generation)
- Derived agents that extend defaults with team-specific behavior

**What this stage emphasizes:**

Fly is built on the trust earned through previous stages. Even at Fly, the team controls the blast radius.

---

Each stage describes a level of engagement with the platform, not a fixed set of features to enable. What Crawl, Walk, Run, or Fly looks like depends on your team's existing workflow, tooling, and needs. Teams can stay at any stage, move between them, or skip stages that don't apply.
