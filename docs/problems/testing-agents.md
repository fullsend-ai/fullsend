# Testing the Agents

We have CI for code, but no CI for prompts. If someone tweaks the Intent Alignment Agent's instructions, how do we prove it didn't forget how to detect Tier Escalation?

## Why this is a distinct problem

Testing application code is a solved problem with mature tooling: unit tests, integration tests, CI pipelines, coverage reports. But agent instructions — system prompts, CLAUDE.md files, review criteria, escalation rules — are a fundamentally different artifact. They're natural language, not code. Their behavior is probabilistic, not deterministic. And the consequences of a regression can be severe: an agent that silently stops catching tier escalation, or starts rubber-stamping security-sensitive changes, or loses its ability to detect prompt injection.

Today, if someone modifies a review agent's instructions, the only verification is human review of the prose change. There is no automated way to confirm the agent still behaves correctly after the modification. This is the equivalent of shipping code changes with no test suite — something we would never accept for application code.

## What makes agent testing hard

### Non-determinism

The same prompt with the same input can produce different outputs across runs. Traditional assertions ("output must equal X") don't work. Testing must be statistical — "the agent produces the correct classification in at least 95 of 100 runs" — which is slower, more expensive, and harder to interpret than binary pass/fail.

### Behavioral surface area

An agent's behavior is the product of its instructions, the model it runs on, the context it receives, and the specific input. A change to any of these can alter behavior. Testing agent instructions in isolation doesn't capture how they interact with real-world inputs. Testing them with the actual model is expensive and slow. Testing them with a different model may not be representative.

### Absence detection

The hardest bugs to catch are capabilities that silently disappear. If someone simplifies the Intent Alignment Agent's instructions and removes the paragraph about tier escalation detection, the agent won't error — it will simply stop checking for tier escalation. There's no compile error, no stack trace, no failing import. The capability quietly vanishes, and you only discover it when a tier-gaming attack succeeds.

### Interaction effects

Agents don't operate alone. The review sub-agents described in [code-review.md](code-review.md) compose their decisions. A change to one agent's instructions might not cause that agent to fail in isolation, but might break the overall review process — for example, if the Intent Alignment Agent starts flagging things that the Correctness Agent used to handle, creating a gap where neither catches certain issues.

### Model updates

Even without any instruction changes, a model update from the provider can change agent behavior. Instructions that worked well with one model version may produce different results with another. This means agent testing isn't just about catching instruction regressions — it's about ongoing behavioral monitoring.

## What needs testing

### Instruction changes (the primary concern)

When someone modifies an agent's system prompt, CLAUDE.md, or configuration:

- Does the agent still perform all its documented responsibilities?
- Has any capability been lost?
- Has any new unintended behavior been introduced?
- Does the agent still correctly handle known edge cases?

### Capability coverage

For each agent role described in [agent-architecture.md](agent-architecture.md) and [code-review.md](code-review.md), there's an implicit set of capabilities. The Intent Alignment Agent should detect tier escalation. The Injection Defense Agent should catch known injection patterns. The Platform Security Agent should flag RBAC changes. These capabilities need explicit test coverage.

### Cross-agent composition

When multiple review sub-agents evaluate a PR together, does their combined judgment still produce correct outcomes? Changes to one agent's behavior can create gaps or conflicts in the overall review.

### Adversarial robustness

From the [security threat model](security-threat-model.md): agents must resist prompt injection. Testing must verify that instruction changes haven't weakened an agent's defenses against known attack patterns.

## Approach 1: Golden-set evaluation

Maintain a curated set of test cases — inputs with known-correct outputs — for each agent. Run the agent against the golden set whenever its instructions change.

### Structure

```
agent-tests/
  intent-alignment/
    golden-set/
      tier-escalation-detection.yaml
      scope-mismatch.yaml
      cross-repo-intent.yaml
    ...
  injection-defense/
    golden-set/
      comment-injection.yaml
      description-injection.yaml
      code-string-injection.yaml
    ...
```

Each test case specifies an input (a synthetic PR, issue, or diff) and the expected agent behavior (approve, reject, escalate, flag specific concerns).

### Trade-offs

**Pros:**
- Concrete, auditable, version-controlled
- Directly tests for known capabilities — if tier escalation detection breaks, the golden-set test for it fails
- Fast feedback loop compared to production monitoring
- Can be run in CI on every instruction change

**Cons:**
- Only tests known scenarios — novel failure modes won't be caught
- Maintaining the golden set is ongoing work; it can drift from reality
- Non-determinism means tests need multiple runs and statistical thresholds, making CI slower and flakier
- The golden set itself can contain biases or blind spots

### The coverage question

How do you know the golden set is sufficient? For application code, coverage tools measure which lines were exercised. There's no equivalent for "which capabilities of a natural-language instruction set were exercised." This is an open research problem.

## Approach 2: Behavioral contract testing

Define contracts for each agent — formal statements about what the agent must and must not do — and test against those contracts. This is more abstract than golden-set testing but potentially more robust.

### Example contracts for the Intent Alignment Agent

- MUST flag any PR where the linked issue describes a bug fix but the diff adds new API surface
- MUST flag any PR that modifies files in more than 3 directories when the linked issue is labeled "bug"
- MUST NOT approve a PR with no linked issue unless the change is classified as Tier 0
- MUST escalate when the diff scope exceeds what the linked intent file authorizes

### Trade-offs

**Pros:**
- Tests capabilities at a higher level than individual examples
- Contracts serve as documentation of expected behavior
- More resilient to minor behavioral variations (tests the "what" not the "how")

**Cons:**
- Contracts still need concrete test inputs to exercise them
- Writing good contracts requires deep understanding of each agent's role
- Contract verification itself may require an LLM (judging whether an agent's response satisfies a contract), introducing another layer of non-determinism

## Approach 3: Canary deployments for instruction changes

Don't try to test exhaustively before deployment. Instead, deploy instruction changes gradually and monitor for behavioral drift.

### How it works

1. Instruction change is proposed via PR
2. The modified agent runs alongside the current agent on a sample of real reviews
3. Outputs are compared — do they agree? Where do they diverge?
4. If divergence is within acceptable bounds, the change is promoted
5. If divergence exceeds thresholds, the change is blocked or escalated

### Trade-offs

**Pros:**
- Tests against real-world inputs, not synthetic ones
- Catches interaction effects and edge cases that golden sets miss
- No need to maintain a separate test suite

**Cons:**
- Slow — requires a meaningful sample of real reviews before the change can be promoted
- Expensive — running two agents in parallel doubles the cost during the canary period
- Risky — if the canary agent has a critical regression, it's processing real PRs (even if its outputs aren't authoritative)
- Doesn't work well for new agents with no production baseline to compare against

## Approach 4: Mutation testing for instructions

Analogous to mutation testing for code: systematically introduce small changes to agent instructions and verify that the test suite catches them.

### How it works

1. Take the current instruction set
2. Generate mutations: remove a paragraph, weaken a requirement ("must" to "should"), delete a specific capability mention
3. Run the test suite against each mutant
4. If the test suite still passes with a mutant, there's a gap in test coverage

### Trade-offs

**Pros:**
- Directly measures test suite quality — answers "would we catch it if this capability disappeared?"
- Automated — mutations can be generated programmatically or by an LLM
- Addresses the absence-detection problem head-on

**Cons:**
- Expensive — many mutations, each requiring multiple evaluation runs
- Some mutations produce meaningfully equivalent instructions (rewording without changing behavior), creating false "test gaps"
- Mutation generation for natural language is less well-defined than for code

## Eval frameworks

The approaches above aren't purely theoretical — an emerging class of LLM evaluation tools already implements parts of them. None covers the full problem space, but they provide a starting point and avoid building everything from scratch.

An important distinction surfaced by [Experiment 004](https://github.com/fullsend-ai/experiments/blob/main/promptfoo-eval/README.md): **most eval frameworks test prompts, not agents.** They send a single prompt to a model API, get a single response, and score it. They do not run an agent loop with tool calls, multi-turn reasoning, or environment interaction. Testing prompts in isolation is necessary but not sufficient — an agent can pass every prompt-level test and still fail in practice when its prompt interacts with real tool outputs, long context, or multi-agent composition. The framework landscape splits accordingly into prompt testers, agent runners, and input generators.

### Prompt evaluation (unit-test layer)

These tools test single prompt/response pairs. They are useful for regression testing agent instructions — catching cases where a prompt edit breaks a known capability — but they do not test agents as agents.

#### promptfoo

[promptfoo](https://www.promptfoo.dev/) is an open-source eval framework built around YAML-defined test cases, assertions, and comparison reports. It's the closest match to the golden-set and CI pipeline patterns described above:

- Test cases are defined declaratively (input, expected output, assertion type) — a natural fit for the golden-set structure in Approach 1
- Supports multiple assertion types: exact match, contains, similarity, regex, and LLM-graded — which maps to behavioral contract verification in Approach 2
- Has a red-teaming mode that generates adversarial inputs, relevant to the adversarial evaluation step in the CI pipeline
- Designed for CI integration, producing machine-readable output

Under the hood, promptfoo makes direct HTTP calls to model provider APIs. Each test case is a single prompt-in, response-out API call. There is no agent loop, no tool use, no multi-turn conversation. See [Experiment 004](https://github.com/fullsend-ai/experiments/blob/main/promptfoo-eval/README.md) for detailed findings from running promptfoo against a PR scope classifier.

**Note:** promptfoo has been acquired by OpenAI. Implications for the open-source project's future are unclear.

#### deepeval

[deepeval](https://docs.confident-ai.com/) is a Python-native eval library with built-in metrics (answer relevancy, faithfulness, hallucination, bias) and a pytest integration. Its distinguishing feature is statistical rigor:

- Supports confidence intervals and minimum-sample-size calculations, directly addressing the non-determinism problem — instead of picking an arbitrary "run it 100 times" threshold, deepeval can compute how many runs are needed for a given confidence level
- Metrics are composable, so you can define multi-dimensional pass criteria (e.g., "must be relevant AND must not hallucinate file paths")
- The pytest integration means eval suites look like normal test suites, which lowers the adoption barrier for teams already testing in Python
- Has explicit "Agentic Metrics" (Task Completion, Tool Correctness, Goal Accuracy, Step Efficiency, Plan Adherence) — but these score agent *traces*, they don't run agents

The main gap: deepeval's built-in metrics are oriented toward conversational AI (relevancy, faithfulness to a source document). Evaluating whether a review agent correctly detected tier escalation requires custom metrics — the framework supports this, but the useful metrics would need to be written.

#### lightspeed-evaluation

[lightspeed-evaluation](https://github.com/instructlab/lightspeed-evaluation) is an evaluation framework from the InstructLab project, focused on validating AI assistant behavior against expected task completions:

- Designed for evaluating instruction-following behavior, which is conceptually close to testing whether an agent's instructions produce the intended behavior
- Includes task-based evaluation patterns where inputs and expected outcomes are defined declaratively

The main gap: lightspeed-evaluation is oriented toward conversational assistants rather than code review agents. Adapting it to evaluate PR review decisions would require significant extension. Its relevance is more as a reference for evaluation methodology than a drop-in tool.

### Agent evaluation (integration-test layer)

These tools actually run agents — including tool-calling, multi-turn agents — and evaluate their end-to-end outcomes. This is the layer that prompt evaluation tools cannot reach.

#### Inspect AI (UK AISI)

[Inspect AI](https://inspect.ai-safety-institute.org.uk/) is an open-source framework from the UK AI Safety Institute. It is the only framework we found that can run an arbitrary CLI-based agent inside a sandboxed container and evaluate its outcomes:

- **Agent Bridge.** `sandbox_agent_bridge()` runs CLI agents (Claude Code, Codex CLI, and by extension OpenCode) inside Docker/K8s containers. The agent talks to an intercepted API on localhost. You configure a Dockerfile for your agent, point it at the bridge, and run it. This is designed exactly for testing opaque agent runtimes.
- **LLM-as-judge.** Built-in `model_graded_fact()`, `model_graded_qa()`, and custom model-graded scorers. First-class feature, not bolted on.
- **Statistical evaluation.** Run evaluations over datasets with many samples, parallel execution, dataframe extraction for analysis.
- **CI-native.** CLI-driven (`inspect eval`), produces structured logs, configurable parallelism. Designed to run headlessly.
- **Community momentum.** METR (the leading AI safety evaluation org) is migrating from their own Vivaria platform to Inspect.
- MIT license, actively maintained.

The main gap: Inspect does not generate test inputs. It only consumes datasets. You bring your own test cases.

#### METR Vivaria

[Vivaria](https://github.com/METR/vivaria) is METR's own tool for running agent evaluations. It runs agents inside Docker containers against METR Task Standard tasks. However, METR explicitly recommends new projects use Inspect AI instead, and is transitioning their own work to Inspect. Heavy infrastructure (PostgreSQL, Docker, web UI), designed for safety research rather than CI.

### Input mutation (test generation layer)

These tools generate test case variations from seeds or produce adversarial inputs. They address the golden-set bootstrapping problem: instead of hand-crafting hundreds of test cases, write 10-20 seed cases per capability and let the framework expand them.

#### DeepEval Synthesizer

DeepEval's `Synthesizer` class is the strongest tool for functional test expansion from seeds:

- `generate_goldens_from_goldens()` takes existing test cases and produces variations using an Evol-Instruct technique with 7 evolution types: add reasoning complexity, add constraints, broaden scope, make abstract questions specific, add comparisons, introduce hypotheticals, require multi-context reasoning
- Configurable via `EvolutionConfig` with evolution rounds and weighted distribution across types
- Quality filtering via `FiltrationConfig` (self-containment + clarity scoring)
- LLM-generated (not deterministic). Output can be exported as JSON/CSV for use with other frameworks

#### DeepTeam

[DeepTeam](https://docs.confident-ai.com/docs/red-teaming-introduction) (separate package from DeepEval) is a red-teaming framework with agent-specific vulnerability types:

- 40+ vulnerability types including agent-specific ones: goal theft, recursive hijacking, excessive agency, autonomous agent drift, tool orchestration abuse, inter-agent communication compromise
- 10+ attack methods: prompt injection, ROT13, jailbreaking, multi-turn attacks
- Can generate and evaluate in one workflow, but only for security/adversarial testing

#### promptfoo red-teaming

promptfoo's `redteam generate` command is the strongest tool for adversarial input generation specifically:

- 50+ vulnerability plugins, sophisticated attack strategy composition (jailbreak + encoding + multi-turn layering)
- Compliance framework presets (OWASP LLM Top 10, NIST AI RMF, MITRE ATLAS)
- Only generates security/adversarial test cases, not functional variations

### What the tools don't cover

The harder problems identified in this document remain open:

- **Cross-agent composition testing** — no framework models the interaction between multiple agents reviewing the same PR. Inspect AI can run one agent at a time; testing whether the Intent Alignment Agent and Correctness Agent together produce correct outcomes would require a custom harness.
- **Mutation testing for natural language** — no framework generates instruction mutations and checks whether the eval suite catches them (though an LLM could generate mutations and the eval frameworks could run the evals)
- **Absence detection** — the tools can verify that an agent does something correctly, but detecting that it silently stopped doing something requires the test author to have anticipated that capability in the first place
- **LLM-as-judge trust** — all frameworks rely on LLM-graded assertions at some level, which circles back to the open question of whether that just moves the trust problem rather than solving it
- **Environment mutation** — all existing mutation tools only mutate user inputs. None mutates the environment the agent operates in (tool responses, file contents, API responses). Agent failures in practice are often caused by unexpected tool outputs, not unusual user inputs. The equivalent of property-based testing (like Python's [Hypothesis](https://hypothesis.readthedocs.io/)) for agents — define properties, generate both inputs and environment states, shrink failing cases to minimal reproductions — does not exist.

### Practical architecture

No single tool covers the full workflow. The pragmatic answer is a pipeline:

1. **Generate** functional test variations from seed cases — DeepEval Synthesizer
2. **Generate** adversarial inputs — promptfoo `redteam generate` or DeepTeam
3. **Transform** generated data into Inspect AI `Sample` format (simple JSON mapping)
4. **Execute** using Inspect AI with `sandbox_agent_bridge()` (runs the actual agent in a container)
5. **Score** using Inspect AI's model-graded scorers

Prompt-level regression testing (via promptfoo or deepeval) remains useful as a fast, cheap unit-test layer that runs on every instruction change. But it is not sufficient for testing agents. The integration-test layer — actually running agents against controlled tasks and evaluating their behavior — is a separate concern that requires a separate tool (Inspect AI being the current best candidate).

The tooling is maturing quickly — what's available today may look different in six months. Any choice should be held lightly and re-evaluated periodically.

## Versioning agent instructions

Regardless of the testing approach, agent instructions need version control and change tracking:

- **Instructions live in git.** Every change is a commit with a diff, a reviewer, and a justification. This follows the design decision that the repo is the coordinator.
- **CODEOWNERS protects instructions.** Per the [governance](governance.md) model, agent instructions are a guarded path. Changes require human approval.
- **Instruction changes trigger CI.** Just as code changes trigger tests, instruction changes trigger the agent test suite (whichever approach is used).
- **Version pinning.** When an agent is deployed, it runs a specific version of its instructions. Rolling back is a git revert.

## CI pipeline for agent configurations

A practical CI pipeline for agent instruction changes might look like:

1. **Static analysis** — lint the instruction change for obvious issues (removed capability mentions, weakened requirements, structural problems)
2. **Golden-set evaluation** — run the agent against the golden set (expanded from seed cases) with the new instructions; gate on a minimum score threshold rather than binary pass/fail
3. **Behavioral contract verification** — check that all defined contracts still hold
4. **Adversarial evaluation** — run known prompt injection attacks against the modified agent
5. **Diff report** — produce a human-readable summary of behavioral changes for the reviewer

Steps 2-4 are expensive (they invoke the LLM), so they may need dedicated pipeline infrastructure separate from normal build pipelines. Cost management is a real constraint — see [agent-infrastructure.md](agent-infrastructure.md).

## Measuring agent capability drift

Beyond testing individual instruction changes, there's a need for ongoing monitoring:

- **Periodic re-evaluation** — run the golden set on a schedule, even without instruction changes, to catch drift from model updates
- **Production outcome tracking** — when a human overrides an agent's decision, log it as a potential test case. If the agent approved something a human rejected, that's a signal.
- **Capability dashboards** — for each agent, track which capabilities are covered by tests and what their recent pass rates look like. Similar in spirit to the coverage dashboard referenced in [repo-readiness.md](repo-readiness.md).

## Relationship to other problem areas

- **[Governance](governance.md)** — Who is authorized to change agent instructions? Testing is the verification layer, but governance determines who can make changes and who reviews them.
- **[Security Threat Model](security-threat-model.md)** — A compromised or regressed agent is a security event. The canary/tripwire patterns mentioned there are directly related to golden-set testing.
- **[Code Review](code-review.md)** — The review sub-agents are the primary agents that need testing. Their decomposition into specialized roles means each role needs its own test coverage.
- **[Agent Architecture](agent-architecture.md)** — The architecture determines what agents exist and what they're responsible for, which determines what needs testing.
- **[Repo Readiness](repo-readiness.md)** — Just as repos need test coverage before agents can be trusted with them, agent instructions need test coverage before instruction changes can be trusted.

## Open questions

- What's the right statistical threshold for non-deterministic tests? How many runs constitute a reliable signal, and what pass rate is acceptable?
- Can we use one LLM to test another's behavior reliably, or does LLM-as-judge just move the trust problem?
- How do we bootstrap the golden set? Do we start with synthetic examples, or do we capture real-world cases from early human-supervised agent operation?
- Who maintains the test suite for each agent? Is it the agent's instruction author, a separate testing team, or the agent itself (self-testing)?
- How do we handle model provider updates that change behavior without any instruction changes? Is periodic re-evaluation sufficient, or do we need real-time drift detection?
- What's the cost budget for agent testing? Running hundreds of LLM evaluations per instruction change could be expensive — both in LLM API costs and in compute resources for running the evaluations in CI.
- Should agent tests be public (in the repo) or private (to avoid teaching attackers what the agents check for)? There's a tension between transparency and security.
- Can agents test other agents, or does that create circular trust dependencies? (Agent A tests Agent B, but who tests Agent A?)
- How do we test cross-agent composition without combinatorial explosion of test scenarios?
- Is there a meaningful equivalent of "code coverage" for natural-language instructions, or is that a false analogy?
