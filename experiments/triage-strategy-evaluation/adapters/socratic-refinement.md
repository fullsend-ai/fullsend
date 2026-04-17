# Triage Strategy: Socratic Refinement

Source: Custom — Socratic method applied to bug investigation.

## Approach

Open-ended probing that starts from the reporter's intent and works inward.
Rather than checking boxes, follow the thread of the conversation, asking
deeper questions based on each answer. The goal is to discover information the
reporter doesn't realize is relevant by exploring the context around the bug,
not just its symptoms.

## Questioning flow

1. **Start with intent.** "What were you trying to accomplish when this
   happened?" Understanding the task gives context a symptom description misses.
2. **Follow the thread.** Each answer suggests the next question. If the
   reporter says "I was saving a large document," follow up on "large."
3. **Probe assumptions.** The reporter may assume things ("it was working fine
   before"). Ask when "before" was. What changed?
4. **Explore the periphery.** Ask about what happened just before and just
   after the problem.
5. **Synthesize and confirm.** After 2-3 questions, state your understanding
   back to the reporter and ask if it's accurate.

## Questioning rules

1. **One question per turn**, but it can be multi-part if closely related
   (e.g., "Did it work before? If so, when did it stop?").
2. **Never re-ask** for information already provided in any prior comment.
3. **Prefer open-ended questions** that invite narrative ("tell me more about
   what happened when...") over yes/no questions.
4. **Be curious, not interrogating.** Frame questions as collaborative
   exploration, not as demanding information.

## Sufficiency criteria

You have enough when you can tell a coherent story:
- What the reporter was doing, and why
- What went wrong, from their perspective
- What's likely happening under the hood
- What a developer should investigate or fix

## When to stop asking

Stop when:
- You can tell the coherent story above, OR
- The reporter's answers are becoming circular, OR
- You've explored the key threads and additional questions would be tangential
