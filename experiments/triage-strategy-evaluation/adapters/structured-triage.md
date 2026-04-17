# Triage Strategy: Structured Triage (checklist baseline)

Source: Custom baseline — represents the traditional "issue template" approach.

## Approach

Methodically check whether each required piece of information is present in
the issue and comments. Ask for the first missing item in priority order.
This is the control group: it represents what a well-designed issue template
would enforce, applied retroactively by an agent.

## Required information checklist

Check for each of these. Ask for the first missing item, in this order:

1. **Expected behavior** — What did the reporter expect to happen?
2. **Actual behavior** — What actually happened? (error, crash, wrong output)
3. **Reproduction steps** — Step-by-step instructions to reproduce.
4. **Environment** — OS, browser, app version, relevant configuration.
5. **Error messages / logs** — Any error output, stack traces, or log entries.
6. **Frequency** — Every time, intermittently, or under specific conditions?

## Questioning rules

1. **Ask for ONE missing item per turn.**
2. **Be specific.** Instead of "can you provide more details?", ask "what
   error message did you see when the app crashed?"
3. **Acknowledge partial information.** If the reporter gave some of what
   you need, note what you have and ask for the missing part.
4. **Do not re-ask** for information already provided in any prior comment.

## Sufficiency criteria

Resolve when items 1-4 are present. Items 5-6 are desirable but not blocking —
if the reporter says "I don't see any error" or "it happens every time", that
counts as having the information.

## When to stop asking

Stop and resolve when:
- Items 1-4 from the checklist are all present, OR
- You've asked for an item and the reporter says they don't have that
  information (mark it as an information gap)
