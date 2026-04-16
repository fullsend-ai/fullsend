# Conversation: crash-on-save x structured-triage

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — losing work to crashes is definitely frustrating.

To help us investigate, could you walk us through exactly what happens when the crash occurs? For example:
- What are you saving (a new task, an edit to an existing task, etc.)?
- What does the crash look like — does the app close entirely, freeze, or show an error message?

Any specific error text or screenshot would be really helpful if you see one.

---

### reporter

I'm editing a task list that has about 200 tasks in it. When I click the Save button in the toolbar, the app just closes on me. Right before it closes I can see a dialog flash very briefly — too fast to read the whole thing, but I think it says something about 'encoding'. The app shuts down completely, no freeze or anything, just gone. This started happening after I imported a bunch of tasks from a CSV file. Auto-save seems to work fine weirdly enough, it's only when I manually hit Save.

---

### triage-agent

Thanks for those details — that's really helpful, especially the bit about the 'encoding' dialog and the CSV import connection.

Could you let us know what environment you're running TaskFlow in? Specifically:
- What operating system are you on (e.g., Windows 11, macOS Sonoma, Ubuntu 24.04)?
- Which version of TaskFlow you're using (you can usually find this under Help → About)?
- If it's a browser-based setup, which browser and version?

The encoding clue combined with the CSV import is a strong lead, and knowing the environment will help us narrow things down.

---

### reporter

I'm on macOS 14.2 (Sonoma) and TaskFlow version 2.3.1. It's the desktop app, not browser-based.

---

### triage-agent

[RESOLVED] {"title":"Desktop app crashes on manual Save with large CSV-imported task list — possible encoding error","problem":"TaskFlow 2.3.1 desktop app crashes (closes entirely) when the user clicks the Save button on a task list containing approximately 200 tasks that were imported from a CSV file. A dialog mentioning 'encoding' flashes briefly before the app terminates. Auto-save does not trigger the crash — only manual Save does.","root_cause_hypothesis":"The CSV import likely introduced characters with non-standard or mixed encoding (e.g., UTF-8 BOM, Latin-1 characters, or null bytes). The manual Save code path appears to handle serialization or encoding differently from auto-save — it likely performs a full re-encode or validation step that chokes on these characters, triggering an unhandled exception that terminates the app.","reproduction_steps":["Import a set of tasks from a CSV file (approximately 200 tasks) into TaskFlow","Open the imported task list for editing","Click the Save button in the toolbar","Observe the app crash — a dialog briefly mentioning 'encoding' may flash before the app closes"],"environment":"macOS 14.2 (Sonoma), TaskFlow 2.3.1 desktop app","severity":"high","impact":"Users who import tasks from CSV files lose unsaved work when attempting to manually save. This affects any user relying on CSV import for bulk task entry. The crash is deterministic and blocks a core workflow.","recommended_fix":"Investigate the difference between the manual Save and auto-save code paths, focusing on character encoding and serialization. Check how CSV-imported data is stored internally versus how the manual Save serializes it. Add proper encoding handling (likely UTF-8 normalization) to the manual Save path, and wrap the save operation in error handling that surfaces the encoding error to the user rather than crashing.","proposed_test_case":"Create a CSV file containing tasks with mixed encoding characters (e.g., accented characters, special symbols, BOM markers, non-ASCII punctuation). Import the CSV into TaskFlow, then perform a manual Save. Verify the save completes without crashing and that the saved data preserves the original characters correctly.","information_gaps":["Exact text of the encoding error dialog","Contents or encoding of the original CSV file that was imported","Whether the crash occurs with smaller CSV imports or only at scale","Application crash logs from macOS Console or TaskFlow log directory"]}

---
