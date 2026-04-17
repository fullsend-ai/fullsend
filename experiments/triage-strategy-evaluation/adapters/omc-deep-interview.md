# Triage Strategy: OMC Deep Interview

Source: [Yeachan-Heo/oh-my-claudecode](https://github.com/Yeachan-Heo/oh-my-claudecode) `/deep-interview` skill

## Approach

Score the clarity of your understanding across four dimensions after each
exchange. Target the dimension with the lowest score with your next question.
Continue until overall clarity reaches 80% or all dimensions are individually
adequate. This prevents premature closure by making sufficiency measurable
rather than intuitive.

## Clarity dimensions

Score each 0.0-1.0 after every exchange:

| Dimension | Weight | What it measures |
|-----------|--------|------------------|
| Symptom | 35% | Do we know exactly what goes wrong? |
| Cause | 30% | Do we have a plausible hypothesis for why? |
| Reproduction | 20% | Could a developer reproduce this? |
| Impact | 15% | How severe? Who's affected? Workarounds? |

## Questioning rules

1. **One question per turn.** Target the dimension with the lowest clarity.
2. **Score clarity after each answer.** Track how your understanding evolves
   and target the weakest area.
3. **Challenge your assumptions at round 3+.** Consider the opposite of what
   seems obvious. ("You say it crashes on save — does it also crash on
   auto-save, or only manual save?")
4. **Simplify at round 4+.** Ask for the minimal reproduction case. ("What's
   the simplest scenario where this still breaks?")
5. **Ask questions naturally.** Don't mention dimensions or scores to the
   reporter — they are your internal reasoning tool.

## Sufficiency criteria

Resolve when:
- Overall clarity >= 0.80 (weighted sum of dimensions), OR
- All four dimensions are individually >= 0.70, OR
- You've reached round 5 and overall clarity >= 0.70

## When to stop asking

Stop and resolve when sufficiency criteria are met. If you reach the turn
limit, resolve with whatever clarity you have, noting the weakest dimensions
as information gaps.
