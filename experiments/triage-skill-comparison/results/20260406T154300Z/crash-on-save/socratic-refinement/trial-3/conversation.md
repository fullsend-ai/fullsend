# Conversation: crash-on-save x socratic-refinement

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I can see how frustrating it must be to lose work repeatedly.

To help us track this down, could you walk me through what you were doing right before the crash? For example, what kind of content were you saving (a new task, edits to an existing one, something else), and did you notice anything unusual — like an error message, a freeze, or the app just disappearing? Any detail about what you see happen will help us narrow this down quickly.

---

### reporter

I was editing a big task list — around 200 tasks. When I click 'Save' in the toolbar, the app just dies. I do see a quick flash of some error dialog that mentions something about 'encoding' but it disappears so fast I can't read the whole thing. It started happening after I imported a bunch of tasks from a CSV file. The weird thing is auto-save seems to work fine, it's only when I manually hit Save. And smaller lists save without any issues.

---

### triage-agent

[RESOLVED] {"title":"App crashes on manual Save with large CSV-imported task lists — encoding error","problem":"When a user manually clicks the 'Save' button in the toolbar after editing a large task list (~200 tasks) that was imported from a CSV file, the application crashes. A brief error dialog referencing 'encoding' flashes before the app dies. Auto-save continues to work correctly, and manual save works fine for smaller lists.","root_cause_hypothesis":"The manual save code path likely uses a different serialization or encoding routine than auto-save. When processing large lists containing characters introduced by CSV import (e.g., non-UTF-8 characters, special delimiters, or BOM markers), this routine either throws an unhandled encoding exception or triggers a buffer/memory issue at scale. The size threshold suggests either a memory-related failure compounded by encoding conversion overhead, or a character that only appears in the larger imported dataset.","reproduction_steps":["Create or obtain a CSV file containing ~200 tasks (include varied characters — accented letters, special symbols, non-ASCII content)","Import the CSV file into TaskFlow","Open the imported task list for editing","Click the 'Save' button in the toolbar","Observe the app crash with a brief encoding-related error dialog"],"environment":"Not specified — likely reproducible across environments given it is tied to a specific code path and data condition","severity":"high","impact":"Users who import tasks from CSV files and manually save are at risk of data loss. The workaround (relying on auto-save) is fragile and undiscoverable. Any user working with large imported datasets is affected.","recommended_fix":"1. Compare the manual save handler's serialization/encoding logic against the auto-save path to identify where they diverge. 2. Check for unhandled encoding exceptions in the manual save routine — add proper error handling and user-facing error reporting instead of a crash. 3. Inspect CSV import for character encoding normalization (e.g., ensure UTF-8 conversion on ingest, strip BOM markers). 4. Test the manual save path with large payloads containing non-ASCII characters from CSV sources.","proposed_test_case":"Import a CSV file with 200+ tasks containing non-ASCII characters (accented characters, emoji, special symbols, and mixed encodings). Edit the list, then trigger manual save via the toolbar button. Verify the save completes without crashing, the data round-trips correctly, and any encoding issues surface as a recoverable error message rather than a crash.","information_gaps":["Exact text of the encoding error dialog","Operating system and app version","Character content of the original CSV file (encoding, special characters present)","Whether the issue reproduces with a freshly created 200-task list (not imported from CSV)"]}

---
