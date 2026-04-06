# Conversation: crash-on-save x structured-triage

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I can see how frustrating it must be to lose your work.

To help us track this down, could you walk us through the exact steps you're taking when the crash happens? For example:

- What are you saving? (a new task, an edit to an existing task, etc.)
- How are you saving? (clicking a button, using a keyboard shortcut, etc.)
- What happens when it crashes? (does the app freeze, close entirely, show an error message?)

The more specific you can be, the faster we can get this fixed for you.

---

### reporter

Sure, so I'm editing a task list that has about 200 tasks in it. When I click the 'Save' button in the toolbar, the app just closes on me. I think I see a dialog flash for a split second before it dies — something about 'encoding' maybe? — but it's too fast to read properly. The app just shuts down completely, no freeze or anything, just gone. I've been saving by clicking the toolbar button. This started after I imported a bunch of tasks from a CSV file, if that helps.

---

### triage-agent

Thanks for those details — the CSV import angle is really helpful context.

Could you let us know what environment you're running the app in? Specifically:

- **Operating system** (e.g., Windows 11, macOS Sonoma, Ubuntu 24.04)
- **App version** (you can usually find this under Help → About)
- If you're using the web version, which **browser and version** you're on

This will help us narrow down whether the crash is platform-specific.

---

### reporter

I'm on macOS 14.2 (Sonoma) and the app version is 2.3.1. It's the desktop app, not the web version.

---

### triage-agent

[RESOLVED] {"title":"Desktop app crashes on save when task list contains CSV-imported data (encoding error)","problem":"The desktop app (v2.3.1, macOS) crashes immediately — full process termination — when the user clicks the Save button on a task list containing approximately 200 tasks that were imported from a CSV file. A dialog mentioning 'encoding' briefly flashes before the app exits.","root_cause_hypothesis":"The CSV import likely introduced text with characters in an encoding (e.g., Latin-1, Windows-1252) that the save/serialization path cannot handle. When the app attempts to encode or serialize the task data on save, it hits an unhandled encoding error that crashes the process instead of being caught gracefully.","reproduction_steps":["Import a set of tasks from a CSV file (reporter's file contained ~200 tasks)","Open the imported task list in the editor","Click the 'Save' button in the toolbar","Observe: app terminates immediately with a brief 'encoding' dialog flash"],"environment":"macOS 14.2 (Sonoma), TaskFlow desktop app v2.3.1","severity":"high","impact":"Users who import tasks from CSV files risk total data loss on save. The crash is not recoverable and the user loses any unsaved work. This blocks a core workflow (import → edit → save).","recommended_fix":"Investigate the save/serialization code path for encoding handling. Check what encoding the CSV importer assigns to imported text and whether the save routine expects a specific encoding (likely UTF-8). Add proper encoding normalization on CSV import and graceful error handling on save so encoding issues surface as a user-facing error rather than a crash.","proposed_test_case":"Import a CSV file containing non-UTF-8 characters (e.g., Latin-1 accented characters, Windows-1252 smart quotes) into a task list, then save. Verify the save completes without crashing and the characters are either preserved or the user is warned about lossy conversion.","information_gaps":["Exact error message from the flashing dialog (reporter could not read it in time)","Whether the crash reproduces with a smaller number of CSV-imported tasks or only at scale","Contents/encoding of the original CSV file","Whether the crash occurs every time or intermittently"]}

---

