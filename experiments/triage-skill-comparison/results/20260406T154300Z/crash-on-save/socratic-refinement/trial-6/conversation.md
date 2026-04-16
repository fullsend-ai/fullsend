# Conversation: crash-on-save x socratic-refinement

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I can see how frustrating it must be to lose your work repeatedly.

To help us track this down, could you walk me through what you were doing right before the crash? Specifically, what kind of task or content were you working on, and how were you saving (e.g., clicking a button, using a keyboard shortcut, auto-save)? And did you notice any error message or unusual behavior right before or after the crash?

---

### reporter

I was editing a big task list — around 200 tasks. I click the 'Save' button in the toolbar and the app just crashes. It actually started happening after I imported a bunch of tasks from a CSV file. Before that import, saving worked fine. I do see a quick flash of some error dialog that mentions something about 'encoding' but it disappears so fast I can't read the whole thing before the app closes. The weird thing is auto-save doesn't seem to cause the crash, only when I manually click Save.

---

### triage-agent

[RESOLVED] {"title":"App crashes on manual Save after importing tasks from CSV — encoding error","problem":"After importing approximately 200 tasks from a CSV file, clicking the Save button in the toolbar causes the application to crash. A brief error dialog referencing 'encoding' flashes before the app closes. Auto-save continues to function normally; only the manual Save button triggers the crash.","root_cause_hypothesis":"The CSV import likely introduced task data containing characters in a non-UTF-8 encoding (or malformed multi-byte sequences). The manual Save button and auto-save use different serialization code paths — the manual save path likely attempts a full serialization or encoding conversion that chokes on the malformed data, while auto-save may do incremental or partial writes that bypass the problematic encoding step.","reproduction_steps":["Create or open a project in TaskFlow","Import a large set of tasks (~200) from a CSV file","Edit or view the imported task list","Click the 'Save' button in the toolbar","Observe the app crash with a brief encoding-related error dialog"],"environment":"Not specified — appears to be the desktop/Electron app based on toolbar Save button and crash behavior","severity":"high","impact":"Users who import tasks from CSV files are unable to manually save their work, leading to data loss. The workaround (relying on auto-save) is fragile and not obvious to users.","recommended_fix":"1. Compare the manual Save and auto-save code paths to identify where encoding handling differs. 2. Add proper encoding detection/conversion during CSV import (normalize to UTF-8 at ingest time). 3. Add error handling in the manual save path so encoding failures surface a readable error instead of crashing. 4. Consider validating encoding on import and warning the user about problematic characters.","proposed_test_case":"Import a CSV file containing non-UTF-8 characters (e.g., Latin-1 accented characters, Windows-1252 smart quotes, or raw byte sequences) with 200+ tasks, then click the manual Save button. Verify the save completes without crashing and the data is preserved correctly.","information_gaps":["Exact encoding of the source CSV file and whether it contains non-ASCII characters","Application platform/version and OS","Whether the crash produces a crash log or stack trace beyond the flashed dialog","Whether deleting the imported tasks restores normal save behavior"]}

---
