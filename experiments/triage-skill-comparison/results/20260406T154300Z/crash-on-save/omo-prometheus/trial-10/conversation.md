# Conversation: crash-on-save x omo-prometheus

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I understand how frustrating it is to lose work. To help us track this down quickly, I need a few specifics:

1. **What are you saving?** (e.g., a new task, editing an existing task, project settings, etc.)
2. **What exactly happens when it crashes?** Does the app close entirely, do you see an error message or dialog, or does it freeze/hang?
3. **Is this new behavior?** Was saving working for you before, and if so, do you remember roughly when it started failing?

If you can answer these three, we'll be in much better shape to investigate.

---

### reporter

I'm editing a task list — I have about 200 tasks in it. When I click Save in the toolbar, the app just closes completely. There's a brief flash of some kind of error dialog that mentions something about 'encoding' but it disappears so fast I can't read the whole thing. And yeah, saving was working fine before. It started happening after I imported a bunch of tasks from a CSV file. If I remove those imported tasks, saving works again.

---

### triage-agent

[RESOLVED] {"title":"App crashes on save when task list contains CSV-imported tasks (encoding error)","problem":"When a user imports tasks from a CSV file and then attempts to save the task list via the toolbar Save button, the application crashes (closes entirely). A briefly-visible error dialog references an 'encoding' issue. The crash does not occur if the imported tasks are removed first, confirming the imported data is the trigger.","root_cause_hypothesis":"The CSV import path accepts data with characters or encoding (e.g., non-UTF-8, BOM markers, or special/multi-byte characters) that the save/serialization code path cannot handle. The save routine likely assumes a consistent encoding and fails — probably an unhandled exception in the serialization layer that propagates to an uncaught crash.","reproduction_steps":["Create or open a task list in TaskFlow","Import tasks from a CSV file (reporter had ~200 tasks after import)","Click Save in the toolbar","Observe: app crashes with a brief encoding-related error dialog"],"environment":"Not specified (reporter did not mention OS/version/platform). The issue is data-dependent rather than environment-dependent — triggered by CSV import content.","severity":"high","impact":"Any user who imports tasks from CSV risks a crash on every subsequent save, leading to repeated data loss. The workaround (removing imported tasks) negates the value of the import feature entirely.","recommended_fix":"1. Inspect the save/serialization code path for encoding assumptions — likely it expects UTF-8 but the CSV import does not normalize encoding on ingest. 2. Add encoding normalization (e.g., detect and convert to UTF-8) in the CSV import pipeline. 3. Add a try/catch around the save serialization that surfaces the full error to the user instead of crashing. 4. Consider validating imported data at import time and warning the user about encoding issues before they hit save.","proposed_test_case":"Import a CSV file containing non-ASCII characters (e.g., accented characters, CJK characters, emoji, BOM markers, and mixed encodings like Latin-1 mixed with UTF-8). Save the task list and verify it completes without error. Verify the saved data round-trips correctly when reopened.","information_gaps":["Exact CSV file encoding and content that triggers the crash (developer can reproduce with various non-UTF-8 CSVs)","Full error message from the crash dialog","User's OS and TaskFlow version","Whether this affects all CSV imports or only CSVs from specific sources/tools"],"confidence":{"root_cause":"medium","reproduction":"high","severity_assessment":"high"}}

---

