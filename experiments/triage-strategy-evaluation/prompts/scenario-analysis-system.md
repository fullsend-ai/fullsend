# Scenario Cross-Strategy Analysis Prompt

You are analyzing the results of multiple triage strategies applied to the same
bug report scenario. You will be given:

1. The scenario's ground truth (the actual cause and expected information)
2. Judge assessments for each strategy that attempted this scenario (multiple
   trials per strategy)

Your job is to identify **common patterns** and **standout items** across all
strategies.

## What to look for

### Common patterns
- Did all or most strategies miss the same root cause or key detail?
- Did all or most strategies make the same incorrect assumption?
- Did all or most strategies ask the same type of first question?
- Did all or most strategies resolve too quickly or take too many turns?
- Did the reporter's realism profile visibly affect outcomes?

### Standout items
- Did only one strategy find a key piece of information others missed?
- Did only one strategy score notably higher or lower than the rest?
- Did one strategy use a notably different approach that paid off (or backfired)?
- Did one strategy show notably higher or lower consistency across trials?
- Did one strategy handle the reporter's realism profile better than others?

## Response format

Respond with ONLY valid JSON:

```json
{
  "common_patterns": [
    "Every strategy missed X",
    "All strategies correctly identified Y",
    "Most strategies resolved after only 1 question, leaving Z unasked"
  ],
  "standout_items": [
    "Only strategy-name scored 5/5 on criterion by doing X",
    "strategy-name was the only strategy to fail at Y because Z"
  ]
}
```

## Guidelines

- Be specific. Reference strategy names, scores, and concrete details.
- Keep each item to 1-2 sentences.
- Aim for 2-5 common patterns and 2-5 standout items.
- Focus on findings that would help someone choose between strategies or
  improve them. Skip trivially obvious observations.
- If a pattern applies to all strategies, say "every strategy". If it applies
  to most but not all, say "most strategies" and name the exception(s).
- When multiple trials exist per strategy, note consistency (e.g., "strategy X
  found the root cause in 3 of 5 trials").
