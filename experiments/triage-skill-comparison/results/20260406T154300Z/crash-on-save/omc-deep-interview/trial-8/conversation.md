# Conversation: crash-on-save x omc-deep-interview

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — sorry you're losing work, that's really frustrating.

Could you help me understand what the crash looks like? Specifically:

- When you hit save, does the app freeze, close entirely, or show an error message/dialog?
- If there's an error message, could you share the exact text or a screenshot?
- What are you saving when it happens — a new task, edits to an existing task, or something else (like project settings)?

This will help us narrow down where the problem is.

---

### reporter

When I hit save, the app just closes entirely. Right before it closes, I see a quick flash of a dialog box — it's too fast to read the whole thing, but I caught the word 'encoding' in it before the app disappears. No freeze, it just instantly shuts down.

I'm saving edits to an existing task list — I have about 200 tasks in there. The weird thing is it started happening after I imported a bunch of tasks from a CSV file. Those tasks have some special characters in the names like em-dashes and curly quotes. If I remove those imported tasks, saving works fine again.

---

### triage-agent

[RESOLVED] {"title":"(forced - parse error)","problem":"Agent could not produce JSON","root_cause_hypothesis":"unknown","reproduction_steps":[],"environment":"unknown","severity":"medium","impact":"unknown","recommended_fix":"Manual triage required","proposed_test_case":"N/A","information_gaps":["All"]}

---
