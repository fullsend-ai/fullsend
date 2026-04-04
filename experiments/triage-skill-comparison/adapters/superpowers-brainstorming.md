# Triage Strategy: Superpowers Brainstorming (adapted)

Source: [obra/superpowers](https://github.com/obra/superpowers) `skills/brainstorming/SKILL.md`

## Approach

Adapts the core principles from superpowers brainstorming skill for issue triage.
The skill's natural questioning loop — one question at a time, multiple choice
preferred, judgment-based sufficiency — maps cleanly to async issue dialogue.

## Questioning rules

1. **One question per turn.** Never batch multiple questions.
2. **Prefer multiple choice** when the answer space is bounded. Offer 2-4 options
   plus "something else." Use open-ended questions only when the answer space
   is genuinely unbounded.
3. **Focus on understanding purpose first.** Before asking about reproduction steps
   or environment, understand what the reporter was trying to accomplish.
4. **YAGNI.** Do not ask for information that would not change how the issue is
   resolved. If the answer doesn't affect the fix, don't ask.
5. **Propose hypotheses.** After 2+ exchanges, propose your best understanding and
   ask if it's correct, rather than continuing to probe blindly.
6. **Scale to complexity.** A clear-cut bug needs fewer questions than an ambiguous
   behavior change. Stop early when the issue is straightforward.

## Sufficiency criteria

You have enough information to resolve when you can:
- Describe the problem in one clear paragraph
- Hypothesize a root cause with reasonable confidence
- Outline what a developer would need to do (or investigate) to fix it
- A developer reading your triage summary would not need to go back to the reporter

## When to stop asking

Stop asking and resolve when:
- You can meet all the sufficiency criteria above, OR
- Further questions would have diminishing returns (the reporter has shared all
  they reasonably know), OR
- You've asked 3+ questions and the picture is clear enough to act on
