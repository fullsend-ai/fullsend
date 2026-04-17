# Conversation: crash-on-save x socratic-refinement

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I can see how frustrating it must be to lose your work repeatedly.

To help us track this down, could you walk me through what you were doing right before the crash? Specifically, what kind of task or document were you working on, and what did you click or do to trigger the save? And when the crash happens, does the app close entirely, freeze, or show an error message?

---

### reporter

Sure — I have a task list with about 200 tasks in it. When I click the 'Save' button in the toolbar, the app just closes on me. Right before it closes I can see a dialog flash really quickly — it says something about 'encoding' but it disappears too fast for me to read the whole thing. The app shuts down completely, no freeze or anything, just gone. This started happening after I imported a bunch of tasks from a CSV file. The weird thing is auto-save seems to work fine, it's only when I manually hit Save.

---

### triage-agent

[RESOLVED] {"title":"App crashes on manual Save with encoding error after CSV task import","problem":"When a user manually clicks the Save button after importing tasks from a CSV file, the application displays a brief encoding-related error dialog and then terminates. Auto-save continues to work correctly. The user loses any unsaved work beyond the last auto-save point.","root_cause_hypothesis":"The CSV import introduced task data containing characters in a non-UTF-8 encoding (or invalid byte sequences). The manual Save code path attempts a stricter or different serialization/encoding process than auto-save, encounters characters it cannot encode, throws an unhandled exception, and the application crashes. Auto-save likely uses a different serialization method, writes in binary/raw mode, or has error handling that the manual save path lacks.","reproduction_steps":["Create or obtain a CSV file containing tasks with non-ASCII or mixed-encoding characters (e.g., curly quotes, accented characters, or data originating from a non-UTF-8 locale)","Import the CSV file into TaskFlow","Observe that auto-save functions normally","Click the 'Save' button in the toolbar","Observe the brief encoding error dialog followed by application termination"],"environment":"Not specified — reproduced with ~200 tasks imported from CSV. Platform and OS version not confirmed but likely relevant if the CSV originated from a system with a different default encoding (e.g., Windows CP-1252 vs UTF-8).","severity":"high","impact":"Any user who imports tasks from CSV files with non-UTF-8 characters is unable to manually save and risks data loss. The crash is deterministic and repeatable, blocking a core workflow.","recommended_fix":"1. Compare the manual Save and auto-save code paths to identify how they differ in encoding/serialization handling. 2. Add explicit encoding handling (e.g., UTF-8 with error replacement or transcoding) to the manual Save path. 3. Wrap the save operation in proper error handling so encoding failures surface as a user-facing error message rather than an unhandled crash. 4. Consider sanitizing or normalizing encoding at CSV import time so downstream saves are safe regardless of path.","proposed_test_case":"Import a CSV file containing tasks with mixed encodings (e.g., Latin-1 accented characters, UTF-8 multibyte characters, and invalid byte sequences). Verify that clicking the manual Save button either succeeds or displays a recoverable error dialog — the application must not terminate. Also verify the saved file is readable on reload.","information_gaps":["The exact encoding of the source CSV file (CP-1252, Latin-1, Shift-JIS, etc.)","The full text of the encoding error dialog","Operating system and TaskFlow version","Whether removing the CSV-imported tasks and re-saving resolves the crash (would confirm the imported data is the trigger)"]}

---
