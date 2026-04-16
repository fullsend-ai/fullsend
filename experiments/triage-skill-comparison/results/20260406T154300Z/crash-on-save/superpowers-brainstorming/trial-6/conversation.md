# Conversation: crash-on-save x superpowers-brainstorming

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — losing work to crashes is definitely frustrating and we want to get this sorted out.

To help us track this down, could you tell us which save operation is triggering the crash?

1. Saving a new task (creating one for the first time)
2. Editing and saving an existing task
3. Saving project settings or preferences
4. Something else (please describe)

Also, do you see any error message or dialog before/during the crash, or does the app just close unexpectedly?

---

### reporter

It's option 2 — I'm editing and saving an existing task list. Specifically, I click the 'Save' button in the toolbar and the whole app just dies. I do see a quick flash of some error dialog that mentions something about 'encoding' but it disappears so fast I can't read the whole thing before the app closes. This started happening after I imported a bunch of tasks from a CSV file. I have about 200 tasks in the list now.

---

### triage-agent

[RESOLVED] {"title":"App crashes on save with encoding error after CSV task import","problem":"When a user edits a task list that was populated via CSV import (~200 tasks) and clicks the toolbar 'Save' button, the application crashes. A brief error dialog referencing 'encoding' flashes before the app closes. The crash is deterministic and repeatable, causing the user to lose unsaved work each time.","root_cause_hypothesis":"The CSV import path does not normalize text encoding on ingest, allowing non-UTF-8 or mixed-encoding characters (e.g., Latin-1 smart quotes, BOM markers, or other non-ASCII byte sequences) into the task data store. The save/serialization path then attempts to encode this data (likely as UTF-8 or JSON) and throws an unhandled encoding exception, which tears down the application.","reproduction_steps":["Prepare a CSV file containing task data with non-ASCII characters (e.g., curly quotes, accented characters, or a non-UTF-8 encoding like Latin-1 or Windows-1252)","Import the CSV file into TaskFlow, creating ~200 tasks","Open the imported task list and edit any task","Click the 'Save' button in the toolbar","Observe the encoding error flash and application crash"],"environment":"Not yet specified — likely desktop app (given toolbar and crash-to-close behavior). OS and version not confirmed but not needed to begin investigation.","severity":"high","impact":"Any user who imports tasks from a CSV file with non-standard encoding will be unable to save edits, losing work on every attempt. This blocks a core workflow (edit + save) for affected task lists.","recommended_fix":"1. Investigate the save/serialization code path for unhandled encoding exceptions — add proper error handling so encoding failures surface a user-visible error instead of crashing. 2. Audit the CSV import path to normalize all text to UTF-8 on ingest, handling common source encodings (Latin-1, Windows-1252, UTF-16). 3. Consider a migration or repair utility for already-imported data containing bad encoding sequences.","proposed_test_case":"Import a CSV file saved with Windows-1252 encoding containing characters like smart quotes (\\u201c\\u201d), em dashes (\\u2014), and accented characters (é, ñ). Edit a task in the imported list and click Save. Verify the save completes without error and the characters are preserved correctly.","information_gaps":["Exact OS and app version (does not change the fix approach)","The specific CSV file or its source encoding (reproducible with any non-UTF-8 CSV)","Whether the crash also occurs when saving without editing (save of unmodified imported data)"]}

---
