# Tool Call Risk Assessment

Evaluating the risk of individual tool calls before execution. Pattern-matching hooks catch known-bad commands, but novel attack patterns and context-dependent risks require semantic understanding that static rules cannot provide.

**Related:**
- [security-threat-model.md](security-threat-model.md) — threat priority and defense principles
- [agent-architecture.md](agent-architecture.md) — agent roles and trust model
- [ADR 0027](../ADRs/0027-allowed-and-disallowed-tools-for-agents.md) — allowed/disallowed tools

## The gap

Fullsend's current security hooks operate on pattern matching: Tirith scans for known-dangerous shell patterns, the SSRF validator checks URL blocklists, the canary hook detects token leakage, and the unicode normalizer strips invisible characters. These are necessary but insufficient.

The gap is between "this command matches a known-bad pattern" and "this tool call is inappropriate given the agent's current task and context." Examples of risks that pattern matching misses:

- A `Bash` call that runs `git push --force` to a protected branch. No single token is dangerous; the combination in context is.
- A `WebFetch` call to an internal service URL that is not on the SSRF blocklist but is clearly outside the agent's task scope.
- A `Read` call to a file containing credentials that happens to be in a non-standard path (not in the sensitive paths list).
- A `Bash` call that constructs a command dynamically via variable interpolation, evading static pattern checks entirely.
- A sequence of individually-benign tool calls that together constitute data exfiltration (read sensitive file, encode content, send via network).

Pattern-matching hooks are the equivalent of a firewall with static rules. This problem is about whether agents also need an IDS/IPS layer that understands intent.

## Approaches

### Approach 1: LLM-as-judge pre-tool hook

A PreToolUse hook that sends the tool name, arguments, and recent conversation context to a fast, inexpensive model (not the primary agent model). The judge model evaluates the call against a set of risk categories and returns a structured verdict.

**Risk categories to evaluate:**
- Command injection / shell escape
- Path traversal / unauthorized file access
- Data exposure / credential leakage
- Privilege escalation
- Destructive operations (irreversible writes, deletions)
- Unauthorized network access
- Scope violation (tool call unrelated to assigned task)

**Verdict structure:**
- Risk level: low / medium / high / critical
- Category: which risk category triggered
- Reasoning: one-sentence explanation

**Action mapping:**
- Low: allow, log
- Medium: allow, log with elevated visibility
- High: block, log, continue agent execution (the agent sees the block and can adjust)
- Critical: block, log, halt agent execution entirely

**Trade-offs:**
- Adds latency per tool call (one LLM round-trip, ~200-500ms with a fast model)
- Adds cost per tool call (small model inference)
- The judge model is itself a potential attack surface (can it be manipulated by the same context it's judging?)
- False positives block legitimate operations; false negatives miss real threats
- The judge shares context with the primary agent, so if the context is poisoned, the judge may be equally blind

**Mitigations for judge manipulation:**
- Use a different model family than the primary agent (model diversity as defense, per [Threat 4](security-threat-model.md#threat-4-supply-chain-attacks))
- Limit the context window the judge sees (recent N tool calls + current call, not full conversation)
- The judge's system prompt is hardcoded and immutable, not influenced by agent configuration

### Approach 2: Learned behavioral baseline

Instead of per-call judgment, build a statistical model of "normal" tool call patterns per agent role. Flag calls that deviate significantly from the baseline.

**What the baseline captures:**
- Distribution of tool types per agent role (a review agent rarely calls Bash; a code agent rarely calls WebFetch)
- Typical argument patterns (file paths accessed, command prefixes used)
- Temporal patterns (tool call frequency, sequences that typically occur together)

**Trade-offs:**
- Requires a training period to establish baselines (cold start problem)
- Legitimate novel behavior triggers false alarms
- An attacker who understands the baseline can stay within it (evasion by conformity)
- More complex to implement and maintain than an LLM judge
- Does not understand intent, only statistical deviation

### Approach 3: Declarative tool call policies

Define per-agent-role policies that constrain what tool calls are allowed beyond the existing `permissions.deny` mechanism. Policies express constraints on arguments, not just tool names.

**Example policy (review agent):**
```yaml
tool_policies:
  Bash:
    allow_patterns:
      - "git diff *"
      - "git log *"
      - "git show *"
    deny_patterns:
      - "git push *"
      - "git merge *"
      - "rm -rf *"
    max_args_length: 500
  WebFetch:
    allow_domains:
      - "github.com"
      - "api.github.com"
  Read:
    deny_paths:
      - "~/.ssh/*"
      - "~/.aws/*"
      - "/etc/shadow"
```

**Trade-offs:**
- Deterministic, fast, no LLM cost
- Easy to audit and version-control
- Cannot catch context-dependent risks or novel attack patterns
- Policy maintenance burden (every new legitimate use case may need a policy update)
- Overlaps significantly with existing `permissions.deny` and `FULLSEND_TOOL_ALLOWLIST`

### Approach 4: Hybrid

Combine Approach 3 (fast deterministic policies) as a first pass with Approach 1 (LLM judge) as a second pass for calls that don't match any explicit allow/deny rule. The deterministic layer handles the common cases cheaply; the LLM judge handles the ambiguous cases.

**Trade-offs:**
- Best coverage of both known and novel threats
- Most complex to implement
- The boundary between "deterministic" and "needs judgment" requires careful design
- Two systems to maintain

## Relationship to existing hooks

This is not a replacement for existing security hooks. The Tirith scanner, SSRF validator, canary detection, and unicode normalizer address specific, well-defined threat classes with zero ambiguity. They should remain as-is. Tool call risk assessment addresses the space between those specific checks, where the threat is context-dependent and the decision requires judgment rather than pattern matching. (How far those existing hooks actually reach also depends on the runtime carrying them: on codex they are re-verified against their digests before every invocation and their interpreter, `PATH` and configuration are pinned past the agent-writable `.env`, where on Claude Code and pi they stay writable between iterations — see [ADR 0100](../ADRs/0100-codex-sandbox-hooks.md).)

The tool allowlist hook (`tool_allowlist_pretool.py`) is the closest existing mechanism. It is currently disabled by default and operates on tool names only, not arguments or context. A risk assessment layer would subsume and extend its functionality.

## Enforcement boundary: advisory inside the agent runtime, hard only at the sandbox boundary

All four approaches above run inside the agent runtime, which bounds what any of them can guarantee. Per [ADR 0027](../ADRs/0027-allowed-and-disallowed-tools-for-agents.md), each agent runs in its own sandbox with default-deny network **and filesystem** policies, and that sandbox is the sole enforcement layer: tool-level restrictions are *always bypassable* (every sandbox needs writable paths such as `/tmp` and `/sandbox`, so an agent that can run a shell can rewrite arguments, shell out around a pattern check, or reach a capability by another path). Restricting tool calls at the runtime layer serves **steering** (focusing the agent, saving tokens, surfacing intent), not **security**. Security lives at or below the sandbox boundary, and the chokepoints differ by rule kind: the OpenShell L7 egress policy for network access, the sandbox filesystem policy for path access, and, for privileged operations that need a credential, the credential-isolation model of [ADR 0017](../ADRs/0017-credential-isolation-for-sandboxed-agents.md), whose **default** keeps credentialed actions out of the sandbox entirely (prefetch + post-process) and whose **fallback**, when full isolation is not viable, routes them through a host-side capability-reducing REST proxy under L7 policy.

That reframes what a tool-call risk layer is *for*:

- **Inside the agent runtime, a "block" is advisory.** All four approaches evaluate a verdict inside the same trust domain as the agent, its own sandbox. Approach 1 is a remote model call; Approaches 3 and 4 run as PreToolUse hook scripts, which are separate processes from the agent but sit on hook wiring and scripts that stay agent-writable between iterations (see [runtime implementation](../contributing/runtime-implementation.md)); so process separation is not what makes something a boundary. The intercepted call is genuinely blocked, but an equivalent capability reached by a different path is not: shelling out around the check, rewriting arguments, or a nested runtime that never fires the hook at all (an agent can invoke a nested `pi` from Bash with none of the runtime's tool hooks). A compromised or jailbroken agent takes that path. Treating an in-runtime block as a security guarantee is the trap, because it produces a false sense of containment against the exact adversary (external prompt injection, the top-priority threat in the [threat model](security-threat-model.md)) it is meant to stop.
- **For a verdict to be a real boundary, enforcement must sit where the agent cannot reach it.** The deterministic, argument-aware policy of Approach 3 becomes load-bearing only when each rule kind is mapped to a chokepoint at or below the sandbox boundary, and those chokepoints are not uniform: domain rules (`WebFetch.allow_domains`) map to the OpenShell L7 egress policy; path rules (`Read.deny_paths`) map to the sandbox filesystem policy; privileged operations that need a credential map to ADR 0017's out-of-sandbox handling (prefetch + post-process by default, the host-side capability-reducing REST proxy as fallback). Local-command patterns such as `git merge` or `rm -rf` have *no* egress or credential chokepoint at all (they act only inside the sandbox), so for those the runtime hook is the only place a policy can run, and it stays advisory. Which rule kinds can be pushed down and which are inherently in-runtime is itself an open question (see below).

So the value of the risk layer is twofold and worth keeping distinct. As **steering, defense in depth, and an observability signal** it is useful in-runtime today: a run that trips many high-risk verdicts is itself a trust signal (see the open questions below). As a **security control** it only counts when the same declarative policy is enforced at or below the sandbox boundary. Conflating the two is how an advisory hook gets mistaken for containment.

## Relationship to reasoning monitoring

Issue [#174](https://github.com/fullsend-ai/fullsend/issues/174) proposes a reasoning monitor that watches the agent's internal thought process for signs of compromise. Tool call risk assessment is complementary: the reasoning monitor detects *intent drift* in the agent's thinking; risk assessment catches *dangerous actions* regardless of intent. An agent with benign reasoning can still make a dangerous tool call due to a misunderstanding, and a compromised agent may produce clean-looking reasoning while crafting a dangerous call.

Both layers are needed. Neither subsumes the other.

## Open questions

- Is the latency of an LLM judge acceptable for every tool call, or should it be sampled (e.g., judge 1 in 10 calls, always judge Bash and WebFetch)?
- Can the judge model be meaningfully isolated from the same poisoned context that might compromise the primary agent?
- Should the risk assessment operate at the individual tool call level, or should it also consider sequences (e.g., "read credentials file" followed by "curl to external URL")?
- How do we handle the cold start problem for behavioral baselines in new repos or with new agent roles?
- What is the right model for the judge? It needs to be fast and cheap but capable enough to understand security context. Is a small fine-tuned model better than a general-purpose small model?
- Should risk assessment verdicts feed into a broader risk score that influences approval routing? A run that triggered multiple high-risk tool call blocks is itself a signal, even if the blocks succeeded.
- How do we measure the effectiveness of risk assessment? What metrics distinguish "caught a real threat" from "blocked legitimate work"?
- If a blocked tool call returns the block reason to the agent, a compromised agent could use that feedback to reformulate the call and evade the judge on retry. How much information should a block verdict expose to the agent, and is there a way to limit retries or detect reformulation attempts?
- Which parts of a declarative tool-call policy can be pushed down to a sandbox-boundary chokepoint (L7 egress for domain rules, the sandbox filesystem policy for path rules, ADR 0017 credential isolation for privileged operations) where they become enforceable, which have no chokepoint at all (local-command patterns such as `rm -rf` that act only inside the sandbox), and which are therefore inherently in-runtime and advisory?
