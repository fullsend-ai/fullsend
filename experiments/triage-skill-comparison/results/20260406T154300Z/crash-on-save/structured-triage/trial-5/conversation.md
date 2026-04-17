# Conversation: crash-on-save x structured-triage

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I can see how frustrating losing your work would be.

To help us track this down, could you describe exactly what happens when the app crashes? For example:
- Does the app close entirely, or does it freeze/become unresponsive?
- Do you see any error message or dialog before it crashes?
- What kind of task are you saving when this happens (e.g., a new task, editing an existing one)?

Any of those details would really help us narrow things down.

---

### reporter

The app closes entirely — it just disappears. Right before it closes, I see a quick flash of some error dialog that mentions something about 'encoding' but it's gone so fast I can't read the whole thing. This happens when I'm editing an existing task list, not creating a new one. I have about 200 tasks in the list and I'm clicking the Save button in the toolbar.

---

### triage-agent

Thanks for those details — the encoding error flash and the large task list are really helpful clues.

Could you let us know what environment you're running TaskFlow in? Specifically:
- What operating system are you on (e.g., Windows 11, macOS Sonoma, Ubuntu 24.04)?
- Which version of TaskFlow are you using (you can find this under Help → About)?
- Are you using the desktop app or the web version, and if web, which browser?

This will help us reproduce the issue on our end.

---

### reporter

I'm on macOS 14.2 (Sonoma), using TaskFlow v2.3.1, and it's the desktop app.

---

### triage-agent

[RESOLVED] {"title":"Desktop app crashes with encoding error when saving large existing task list","problem":"TaskFlow desktop app crashes (closes entirely) when the user clicks the Save button in the toolbar while editing an existing task list containing approximately 200 tasks. A brief encoding-related error dialog flashes before the app closes, causing the user to lose unsaved work.","root_cause_hypothesis":"The save/serialization path likely encounters a character encoding issue when processing a large task list — possibly a task contains special or non-ASCII characters that trigger an unhandled encoding exception during serialization, and the error handler or crash reporter briefly shows a dialog before the process exits.","reproduction_steps":["Open TaskFlow v2.3.1 desktop app on macOS 14.2","Open an existing task list containing approximately 200 tasks","Make an edit to one or more tasks","Click the Save button in the toolbar","Observe: app crashes (closes entirely) with a brief flash of an encoding-related error dialog"],"environment":"macOS 14.2 (Sonoma), TaskFlow v2.3.1, desktop app","severity":"high","impact":"Users with large existing task lists lose all unsaved work when attempting to save. This blocks a core workflow (editing and saving tasks) and causes data loss.","recommended_fix":"Investigate the save/serialization code path for encoding issues — check how task content is encoded when written to disk or sent to the backend. Look for unhandled exceptions in the encoding/serialization layer, particularly with special characters, emoji, or non-ASCII text in task fields. Add proper error handling so encoding failures surface a readable error instead of crashing. Also investigate whether the large list size (200 tasks) contributes (e.g., buffer overflow or memory issue during serialization).","proposed_test_case":"Create a task list with 200+ tasks including tasks containing special characters (emoji, accented characters, CJK characters, etc.), edit a task, and verify that clicking Save completes successfully without crashing. Also test with a task list of similar size containing only ASCII to isolate whether size or content triggers the issue.","information_gaps":["Exact encoding error message (flashes too quickly for reporter to read — could be retrieved from crash logs)","Whether the issue reproduces with smaller task lists or only with ~200+ tasks","Whether any tasks contain special/non-ASCII characters","Frequency: whether it happens on every save attempt or intermittently"]}

---
