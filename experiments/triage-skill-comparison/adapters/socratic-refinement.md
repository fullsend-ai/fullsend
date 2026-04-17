# Triage Strategy: Socratic Refinement

Source: Custom — inspired by Socratic method combined with elements from
superpowers brainstorming and general bug investigation techniques.

## Approach

Open-ended Socratic probing that starts from the reporter's intent and works
inward. Rather than checking boxes, this strategy follows the thread of the
conversation, asking deeper questions based on each answer.

The goal is to discover information the reporter doesn't realize is relevant,
by exploring the context around the bug rather than just its symptoms.

## Questioning flow

1. **Start with intent.** "What were you trying to accomplish when this happened?"
   Understanding the task gives context that a symptom description alone misses.
2. **Follow the thread.** Each answer suggests the next question. If the reporter
   says "I was trying to save a large document," follow up on "large" — how large?
   What kind of document? Did it work with smaller ones?
3. **Probe assumptions.** The reporter may assume things ("it was working fine
   before") — ask when "before" was. What changed? Did they update, reconfigure,
   or change their usage pattern?
4. **Explore the periphery.** Ask about what happened just before and just after
   the problem. "What were you doing right before the crash? Did anything else
   seem different?"
5. **Synthesize and confirm.** After 2-3 questions, state your understanding back
   to the reporter and ask if it's accurate. This catches misunderstandings early.

## Questioning rules

1. **One question per turn**, but it can be multi-part if the parts are closely
   related (e.g., "Did it work before? If so, when did it stop working?").
2. **Never ask a question the reporter has already answered.** Read all prior
   comments carefully.
3. **Prefer open-ended questions** that invite narrative ("tell me more about
   what happened when...") over closed questions ("yes/no").
4. **Be curious, not interrogating.** Frame questions as collaborative exploration,
   not as demanding information.

## Sufficiency criteria

You have enough when you can tell a coherent story:
- What the reporter was doing, and why
- What went wrong, from their perspective
- What's likely happening under the hood
- What a developer should investigate or fix

## When to stop asking

Stop when:
- You can tell the coherent story above, OR
- The reporter's answers are becoming circular (they're sharing the same
  information rephrased), OR
- You've explored the key threads and additional questions would be tangential
