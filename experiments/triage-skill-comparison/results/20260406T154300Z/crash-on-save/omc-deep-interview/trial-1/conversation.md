# Conversation: crash-on-save x omc-deep-interview

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Sorry to hear you're dealing with this! To help us track this down, could you describe exactly what happens when the crash occurs? For example: does the app freeze and then close, does it show an error message or dialog before closing, or does it just instantly disappear? And which part of TaskFlow are you saving — a new task, an edit to an existing task, project settings, or something else?

---

### reporter

When I click Save in the toolbar, I see a quick flash of a dialog box — it's so fast I can barely read it, but I think it says something about 'encoding' — and then the whole app just closes. No freeze, it's pretty much instant. I'm editing a task list with about 200 tasks in it. This started happening after I imported a bunch of tasks from a CSV file.

---

### triage-agent

Thanks for those details — the encoding error after a CSV import is a really helpful clue. A couple of things that would help us reproduce this on our end: What kind of data was in the CSV? For instance, did any of the task names or descriptions contain non-English characters, emoji, or special symbols like curly quotes? Also, could you let us know what OS and version of TaskFlow you're running? If you still have the CSV file, would you be comfortable attaching it (or a trimmed-down version with just a few rows that still triggers the crash) to this issue?

---

### reporter

Yeah, the CSV had task names with special characters — things like curly quotes and em-dashes. It was exported from another tool so it had all that fancy formatting. I'm on macOS 14.2 running TaskFlow v2.3.1. I don't think I can share the actual CSV since it's work stuff, but I can tell you that if I manually delete the imported tasks, the crash stops. Also, smaller lists seem to save fine — I tested with a list of maybe 40-50 tasks that included some of the imported data and it didn't crash. It only seems to happen when the list is big, like my 200-task list.

---

### triage-agent

[RESOLVED] {"title":"Crash on save: encoding error when saving large task lists containing special characters from CSV import","problem":"TaskFlow crashes instantly when saving a task list containing ~200 tasks that were imported from a CSV file. A dialog briefly flashes mentioning 'encoding' before the app closes. The crash only occurs with large lists (~200 tasks); smaller subsets (~40-50 tasks) of the same imported data save without issue. Removing the imported tasks also eliminates the crash.","root_cause_hypothesis":"The CSV import preserves non-ASCII characters (curly quotes, em-dashes, etc.) in their original encoding, but the save/serialization path fails to handle them correctly — likely a buffer overflow or unhandled encoding conversion error that becomes fatal at scale. The size dependency suggests either a fixed-size buffer for encoding conversion, or an accumulating error (e.g., character-by-character re-encoding) that only overflows or triggers a crash past a certain data volume.","reproduction_steps":["Install TaskFlow v2.3.1 on macOS 14.2","Create a CSV file with ~200 task entries containing curly quotes (“ ”), em-dashes (—), and other non-ASCII Unicode characters in task names/descriptions","Import the CSV into TaskFlow","Click Save in the toolbar","Observe: brief dialog flash mentioning 'encoding', then app closes"],"environment":"macOS 14.2, TaskFlow v2.3.1","severity":"high","impact":"Users who import tasks from external tools (CSV) with non-ASCII characters lose all unsaved work when saving large task lists. Workaround exists (delete imported tasks or work with smaller lists), but this defeats the purpose of the import feature.","recommended_fix":"Investigate the save/serialization path for encoding handling. Likely candidates: (1) check if the save routine assumes ASCII or a fixed encoding when writing task data — it should use UTF-8 throughout, (2) look for fixed-size buffers in the serialization layer that could overflow with multi-byte characters at scale, (3) add proper error handling around the encoding/serialization step so a conversion failure surfaces as a user-visible error rather than a crash. Also audit the CSV import path to ensure it normalizes encoding on ingest.","proposed_test_case":"Create a test that generates a task list of 200+ entries with mixed Unicode characters (curly quotes, em-dashes, emoji, accented characters), saves it, reloads it, and verifies all characters round-trip correctly. Add a boundary test varying list size (50, 100, 200, 500 tasks) to confirm the size-dependent aspect is resolved.","information_gaps":["Exact error message in the flashing dialog (reporter couldn't read it fully)","Whether auto-save also triggers the crash or only manual toolbar save","Whether the crash occurs on other platforms (Windows, Linux) or is macOS-specific","Exact CSV encoding (UTF-8, Windows-1252, etc.) from the exporting tool"],"clarity_scores":{"symptom":0.85,"cause":0.75,"reproduction":0.80,"impact":0.75,"overall_ambiguity":0.20}}

---
