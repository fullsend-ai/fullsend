# Triage Strategy: OMC Deep Interview (adapted from oh-my-claudecode)

Source: [Yeachan-Heo/oh-my-claudecode](https://github.com/Yeachan-Heo/oh-my-claudecode) `/deep-interview` skill

## Approach

Adapts oh-my-claudecode's deep-interview skill for issue triage. The original
skill uses mathematical ambiguity gating with weighted clarity dimensions and
Socratic questioning. It scores clarity after each answer and targets the
weakest dimension with each question. It also deploys "challenge agent modes"
at specific round thresholds to break out of local optima.

For issue triage, we adapt the clarity dimensions from feature design to bug
investigation.

## Clarity dimensions (adapted for bug triage)

Score each dimension 0.0-1.0 after every exchange:

| Dimension | Weight | What it measures |
|-----------|--------|------------------|
| Symptom Clarity | 35% | Do we know exactly what goes wrong? (error, crash, wrong output, performance) |
| Cause Clarity | 30% | Do we have a plausible hypothesis for why? (trigger, conditions, root cause) |
| Reproduction Clarity | 20% | Could a developer reproduce this from the information given? |
| Impact Clarity | 15% | How severe is this? Who/what is affected? Is there a workaround? |

## Ambiguity calculation

```
ambiguity = 1 - (symptom * 0.35 + cause * 0.30 + reproduction * 0.20 + impact * 0.15)
```

**Target: ambiguity <= 0.20** (i.e., 80% clarity)

## Questioning rules

1. **ONE question per turn.** Target the dimension with the lowest clarity score.
2. **State which dimension you're targeting and why** in your internal reasoning,
   but ask the question naturally (don't tell the reporter about dimensions).
3. **Score clarity dimensions after each answer.** Track how clarity evolves.
4. **Challenge modes** fire at specific rounds (see below).

## Challenge agent modes

Borrow oh-my-claudecode's challenge modes to break out of surface-level questioning:

- **Round 3+ (Contrarian):** Consider the opposite of what the reporter claims.
  "You say it crashes on save — does it also happen on auto-save, or only
  manual save? What about 'save as'?"
- **Round 4+ (Simplifier):** "What's the simplest scenario where this still
  breaks? Can you reproduce it with a blank document?"
- **Round 5+ (Ontologist):** Only if ambiguity > 0.3. Question fundamental
  assumptions. "When you say 'save,' what exactly happens in the app? Does it
  save to disk, to cloud, or both?"

Each mode fires at most once.

## Sufficiency criteria

Resolve when:
- Ambiguity <= 0.20, OR
- All four dimensions are >= 0.7 individually, OR
- You've reached round 5 and ambiguity <= 0.30 (good enough to act on)

## When to stop asking

Stop asking and resolve when sufficiency criteria are met. Include your final
clarity scores in the triage summary.

## Output format

When resolving, include a "clarity_scores" field in your triage summary:
```json
{
  "clarity_scores": {
    "symptom": 0.9,
    "cause": 0.8,
    "reproduction": 0.85,
    "impact": 0.75,
    "overall_ambiguity": 0.15
  }
}
```
