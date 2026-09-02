# Roadmap

Where fullsend is going, as **Now / Next / Later**. Those are confidence
horizons, not deadlines: **Now** is what this planning cycle is advancing,
**Next** is likely once Now has room, **Later** is on the horizon.

This edition is the **September 2026** planning snapshot. Themes are how to
find work; horizons are how to read commitment. Tracking spans
[fullsend-ai/fullsend](https://github.com/fullsend-ai/fullsend) and
[fullsend-ai/agents](https://github.com/fullsend-ai/agents) (best-effort).
The team also does maintenance that is not listed here.

Earlier published editions live in [archived-roadmaps](archived-roadmaps/).

## At a glance

| Theme | Now |
| ----- | --- |
| [Auto-merge](#auto-merge) | A dedicated opt-in auto-merge agent; multi-runtime lets the multi-model review squad ([fullsend#6322](https://github.com/fullsend-ai/fullsend/issues/6322)) run, which raises trust in that path |
| [Review quality and cost](#review-quality-and-cost) | Compare review approaches using quality and cost together |
| [GitLab](#gitlab) | A usable GitLab path, including traces from real runs |
| [Jira](#jira) | Jira can drive the default agent loop for teams that already live there |
| [Community](#community) | Contributor review keeps moving; identify maintainer candidates through the documented process |
| [Platform](#platform) | Host APIs, public mint, per-org leftovers, artifacts, behaviour tests |
| [Partners](#partners) | Tekton and Lightwell stay in motion; not a ship promise |

## Now

What we are actively advancing. Themes are listed in planning-discussion
order, not priority rank. Issue links are the work that best matches the
September planning discussion, not the full backlog under each theme.

### Auto-merge

The deliverable is a **fullsend auto-merge agent**: dedicated, opt-in, disabled
by default, for a narrow class of low-risk work. Repository rules and CI stay
the enforcement boundary. CI-aware fixing raises the success rate; it is not a
gate on the first foothold.

That agent depends on a trust path the team laid out in planning:
**multi-runtime** so the **multi-model review squad** ([fullsend#6322](https://github.com/fullsend-ai/fullsend/issues/6322)) can run as the review agent,
which should improve trust in auto-merge. Debounce/restart and a tighter
triage → code → review → fix loop are the other enablers. Eval measurements
and the revert/defect view (next theme) are how we know the path is actually
safer.

**Auto-merge agent**

- [agents#1132 — Add a dedicated auto-merge agent](https://github.com/fullsend-ai/agents/issues/1132)
- [fullsend#3016 — Consider auto-merge for low-risk Renovate patch/minor devDependency updates where CI passes](https://github.com/fullsend-ai/fullsend/issues/3016)

**Depends on: multi-runtime → review squad**

- [fullsend#587 — Investigate ACP proxy chains for multi-runtime support with consistent security hooks](https://github.com/fullsend-ai/fullsend/issues/587)
- [fullsend#6322 — Enhance Fullsend review agent with multi-model collaborative review strategy](https://github.com/fullsend-ai/fullsend/issues/6322)

**Depends on: loop hygiene (debounce, review/fix)**

- [fullsend#1014 — Debounce review dispatch on rapid synchronize events](https://github.com/fullsend-ai/fullsend/issues/1014)
- [fullsend#5666 — Route fix-agent synchronize events to review stage for automated re-review](https://github.com/fullsend-ai/fullsend/issues/5666)
- [agents#343 — Scope re-review to finding verification when push only addresses prior findings](https://github.com/fullsend-ai/agents/issues/343)
- [agents#447 — Review agent should incorporate outstanding human reviews when re-reviewing after PR update](https://github.com/fullsend-ai/agents/issues/447)
- [agents#478 — Post-fix script should reply to review inline threads it addressed](https://github.com/fullsend-ai/agents/issues/478)

### Review quality and cost

Measure the review path that auto-merge will rely on. Run the collaborative
review approach beside the current agent (not a day-one replacement). Compare
cost over time. Group traces across triage → code → review → fix. Make revert
and defect rates visible.

- [fullsend#6433 — telemetry: hierarchical work-graph correlation IDs for cost rollup (leaf / parent / feature)](https://github.com/fullsend-ai/fullsend/issues/6433)
- [fullsend#5361 — telemetry: MLflow trace columns (Name/Session/User/Source) blank; token & cost omit cache tokens](https://github.com/fullsend-ai/fullsend/issues/5361)
- [fullsend#6458 — Portable OTLP export for eval measurement scores](https://github.com/fullsend-ai/fullsend/issues/6458)
- [fullsend#6892 — Revert and defect rates are not visible for review and auto-merge outcomes](https://github.com/fullsend-ai/fullsend/issues/6892)
- [agents#209 — Review eval suite has zero coverage — reintroduce review eval cases after post-review 422 fix lands](https://github.com/fullsend-ai/agents/issues/209)

### GitLab

Move from the current foundation through a usable release path. Prove default
stages and MLflow export on the same GitLab run, not as separate pieces.

- [fullsend#6684 — feat(cli): add `--gitlab-url` flag to `repos install` for GitLab bootstrapping](https://github.com/fullsend-ai/fullsend/issues/6684)
- [fullsend#6816 — GitLab MR-event dispatch (fullsend-dispatch.yml) does not pass REPO_FULL_NAME for review stage](https://github.com/fullsend-ai/fullsend/issues/6816)
- [fullsend#6893 — GitLab default agent stages and MLflow trace export are not validated together](https://github.com/fullsend-ai/fullsend/issues/6893)

### Jira

Finish the path from Jira events into default triage and code so teams (for
example Konflux) do not copy bugs into GitHub first. The August-edition Jira
poller issues ([fullsend#3812](https://github.com/fullsend-ai/fullsend/issues/3812),
[fullsend#4885](https://github.com/fullsend-ai/fullsend/issues/4885),
[fullsend#3428](https://github.com/fullsend-ai/fullsend/issues/3428)) remain
open; the work below is what the September plan focuses on.

- [fullsend#6672 — jira-poll: built-in harness files lack CEL triggers, causing 0 dispatches](https://github.com/fullsend-ai/fullsend/issues/6672)
- [fullsend#2264 — Add JIRA support to the triage agent](https://github.com/fullsend-ai/fullsend/issues/2264)
- [fullsend#2265 — Add JIRA support to the code agent](https://github.com/fullsend-ai/fullsend/issues/2265)

### Community

Gain additional contributors through the [maintainer process](../MAINTAINERS.md).
Do not promise specific number of new maintainers. Keep contributor pull
requests moving. Progress is tracked through PR review velocity and the
maintainer process rather than dedicated issues.

### Platform

Host-side APIs for sandboxed subagents, finish public mint, complete the
per-org removal leftovers, let teams choose artifact storage, and shift
tests toward behaviour coverage in the agents repo.

- [fullsend#879 — Host-side API servers for sandboxed agents](https://github.com/fullsend-ai/fullsend/issues/879)
- [fullsend#881 — fullsend run: implement host-side API server lifecycle](https://github.com/fullsend-ai/fullsend/issues/881)
- [fullsend#5116 — Deploy PROD public CF mint at mint.fullsend.sh](https://github.com/fullsend-ai/fullsend/issues/5116)
- [fullsend#2887 — Remove install_mode input from per-repo shim template](https://github.com/fullsend-ai/fullsend/issues/2887)
- [fullsend#6734 — Configurable artifact storage backend (replace forge-native artifacts)](https://github.com/fullsend-ai/fullsend/issues/6734)
- [fullsend#3786 — feat(behaviour): integrate behaviour test runner in fullsend-ai/agents](https://github.com/fullsend-ai/fullsend/issues/3786)
- [fullsend#3236 — Evaluate overlap between functional tests and behaviour tests + prompt evals](https://github.com/fullsend-ai/fullsend/issues/3236)

### Partners

Keep the Tekton compatibility conversation going. Explore a Lightwell
custom-agent workflow after their GA, as a mock/PoC — not a September
delivery promise. No public tracking issue yet for either thread.

## Next

Parked or dependency-bound in this planning session. Not dated.

- Isolated sandboxes per subagent (host APIs come first): [fullsend#3978 — harness: subagents share the parent sandbox instead of running in isolated sandboxes](https://github.com/fullsend-ai/fullsend/issues/3978)
- Red Hat AI inference routing — waiting on an inference proxy from another team: [fullsend#6464](https://github.com/fullsend-ai/fullsend/issues/6464)
- Growing eval-measurements past the first review/cost slice: more stages, model experiments, and outcome evals — [fullsend#3413 — Design large-scale evaluation experiment for new inference models](https://github.com/fullsend-ai/fullsend/issues/3413) · [fullsend#6384 — Track agents@v0 release cut for eval measurement manifests](https://github.com/fullsend-ai/fullsend/issues/6384)
- Tool proxies after host APIs land: [fullsend#5242 — Tool proxies: transparent CLI interception for sandboxed agents](https://github.com/fullsend-ai/fullsend/issues/5242)

## Later

Direction, not a plan. No dates.

- Kubernetes and OpenShift execution — forge-decoupled agent runtime
- Security hardening, human factors, and production feedback loops — [human-factors](problems/human-factors.md) · [governance](problems/governance.md) · [production-feedback](problems/production-feedback.md) · tracking: [fullsend#172](https://github.com/fullsend-ai/fullsend/issues/172) · [fullsend#877](https://github.com/fullsend-ai/fullsend/issues/877) · [fullsend#2826](https://github.com/fullsend-ai/fullsend/issues/2826)
- Agent attestations — cryptographic provenance for agent output: [fullsend#267](https://github.com/fullsend-ai/fullsend/issues/267)
- Cross-forge orchestration — GitHub + GitLab / multi-org
- Alternative sandbox providers — considered; not worth the refactor now

## August 2026 (shipped since last edition)

August planning lived as a flat Now / Next / Later table. The published
edition is in [archived-roadmaps/2026-08.md](archived-roadmaps/2026-08.md).
Work that closed in that window includes GitLab polling/dispatch foundations
([fullsend#1964](https://github.com/fullsend-ai/fullsend/issues/1964),
[fullsend#5556](https://github.com/fullsend-ai/fullsend/issues/5556)),
removing per-org install mode
([fullsend#2302](https://github.com/fullsend-ai/fullsend/issues/2302)),
versioned docs
([fullsend#5717](https://github.com/fullsend-ai/fullsend/issues/5717),
[fullsend#5718](https://github.com/fullsend-ai/fullsend/issues/5718)),
public-mint `--public` support
([fullsend#5634](https://github.com/fullsend-ai/fullsend/issues/5634)),
GitLab default-stage environment variables
([fullsend#6865](https://github.com/fullsend-ai/fullsend/issues/6865)),
configurable agent status comments
([fullsend#3697](https://github.com/fullsend-ai/fullsend/issues/3697)),
multi-endpoint telemetry export
([fullsend#5545](https://github.com/fullsend-ai/fullsend/issues/5545)),
and doc-practices guidance
([fullsend#5372](https://github.com/fullsend-ai/fullsend/issues/5372)).

Items from the August edition not listed above were folded into September
themes, moved to Next / Later, or deferred — see the
[archived August edition](archived-roadmaps/2026-08.md) for the
complete table.
