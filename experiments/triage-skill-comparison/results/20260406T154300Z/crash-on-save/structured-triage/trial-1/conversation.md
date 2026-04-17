# Conversation: crash-on-save x structured-triage

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — sorry you're losing work, that's definitely frustrating.

To help us track this down, could you describe what exactly happens when the app crashes? For example:
- Does the app close completely, freeze, or show an error dialog?
- Do you see any error message or warning before or after the crash?
- Is there a specific type of task or project you're saving when this happens, or does it occur on any save?

---

### reporter

When I hit Save, I see a dialog flash really quickly — too fast to read fully, but I caught the word 'encoding' in it — and then the app just closes completely. No freeze, it just shuts down. I have to reopen everything and my changes are gone.

It seems to happen on my main task list which has about 200 tasks in it. I recently imported a bunch of tasks from a CSV file and that's around when the problem started. Smaller lists seem to save fine.

---

### triage-agent

Thanks for those details — the CSV import connection and the 'encoding' flash are really helpful clues.

Could you let us know what environment you're running the app in? Specifically:
- What operating system are you on (e.g., Windows 11, macOS Sonoma, Ubuntu 24.04)?
- Which version of TaskFlow are you using (you can find this under **Help → About**)?
- Do you recall what encoding or format your CSV file was in (e.g., UTF-8, Excel default), or where the data in the CSV originally came from?

---

### reporter

I'm on macOS 14.2 (Sonoma) and TaskFlow version 2.3.1. As for the CSV, I honestly don't know what encoding it was — I just exported it from a spreadsheet. I do know the data had some fancy characters in it like em-dashes and curly quotes in the task names, if that matters.

---

### triage-agent

[RESOLVED] {"title":"App crashes on save when task list contains special characters imported from CSV","problem":"TaskFlow crashes immediately upon saving a task list that contains approximately 200 tasks imported from a CSV file. A dialog briefly flashes mentioning 'encoding' before the app closes completely, resulting in data loss. Smaller task lists without the imported data save successfully.","root_cause_hypothesis":"The CSV import likely brought in text with non-UTF-8 encoding (e.g., Windows-1252 or MacRoman) containing characters like em-dashes and curly quotes. The save routine appears to hit an encoding error when serializing these characters, and the unhandled exception causes the app to crash instead of gracefully recovering.","reproduction_steps":["Create or obtain a CSV file containing task names with special characters (em-dashes, curly quotes) exported from a spreadsheet application","Import the CSV into TaskFlow to create a task list with ~200 tasks","Attempt to save the task list","Observe the brief 'encoding' dialog flash followed by the app closing"],"environment":"macOS 14.2 (Sonoma), TaskFlow v2.3.1","severity":"high","impact":"Users who import task data from spreadsheets via CSV lose all unsaved work on every save attempt. This blocks normal use of the app for affected task lists and risks significant data loss.","recommended_fix":"Investigate the save/serialization path for encoding handling. Ensure imported CSV data is normalized to UTF-8 at import time, or that the save routine can handle mixed encodings gracefully. The crash dialog should be caught and surfaced as a recoverable error rather than terminating the app.","proposed_test_case":"Import a CSV file containing task names with non-ASCII characters (em-dashes, curly quotes, accented characters) in various encodings (Windows-1252, MacRoman, UTF-8 with BOM). Verify that saving the resulting task list succeeds without errors and that the special characters are preserved correctly.","information_gaps":["Exact encoding of the original CSV file is unknown","Exact error message from the flashing dialog was not fully captured","Whether the issue is triggered by list size, specific characters, or a combination of both is not fully isolated"]}

---
