# Conversation: crash-on-save x structured-triage

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — sorry you're losing work, that's definitely painful.

To help us track this down, could you walk us through the exact steps you take before the crash? For example:

1. What screen or view are you on when you hit Save? (e.g., creating a new task, editing an existing one, updating project settings)
2. What did you do right before saving? (e.g., changed the title, added an attachment, modified a due date)
3. Does the app close entirely, freeze, or do you see an error message before it goes down?

Even a rough sequence like "I open task X, change the description, click Save, and it crashes" would help a lot.

---

### reporter

Sure, here's what happens: I'm on the main task list view where I can see all my tasks. I'm not even making changes most of the time — I just click the 'Save' button in the toolbar and it crashes. The app closes entirely, though right before it does I see a quick flash of some error dialog that mentions something about 'encoding' but it disappears too fast to read the whole thing. This started happening after I imported a bunch of tasks from a CSV file — maybe 200 tasks total now. Before the import, saving worked fine.

---

### triage-agent

Thanks for that detail — the CSV import connection is a really useful clue.

Could you let us know what environment you're running TaskFlow in? Specifically:

- **Operating system** (e.g., Windows 11, macOS Sonoma, Ubuntu 24.04)
- **Browser** (if it's a web app — e.g., Chrome 125, Firefox 128) or **desktop app version**
- **TaskFlow version** (you can usually find this under Help → About or in Settings)

This will help us narrow down whether the crash is platform-specific or tied to the app itself.

---

### reporter

I'm on macOS 14.2 (Sonoma). It's the desktop app, not a web app — version 2.3.1. Let me know if you need anything else.

---

### triage-agent

[RESOLVED] {"title":"Desktop app crashes on save with encoding error after CSV import (~200 tasks)","problem":"TaskFlow desktop app v2.3.1 crashes immediately when the user clicks the Save button in the toolbar on the main task list view. The crash began after importing approximately 200 tasks from a CSV file. A brief error dialog referencing 'encoding' flashes before the app closes entirely. The crash occurs even without making any changes — simply clicking Save triggers it.","root_cause_hypothesis":"The CSV import likely introduced task data containing characters in an encoding the save/serialization routine cannot handle (e.g., non-UTF-8 characters, malformed Unicode sequences, or a BOM). When the app attempts to serialize the full task list to disk, it hits an unhandled encoding exception and crashes.","reproduction_steps":["Install TaskFlow desktop app v2.3.1 on macOS 14.2","Import a CSV file containing approximately 200 tasks","Navigate to the main task list view","Click the 'Save' button in the toolbar","Observe the app crash (brief encoding-related error dialog flashes before the app closes)"],"environment":"macOS 14.2 (Sonoma), TaskFlow desktop app v2.3.1","severity":"high","impact":"User cannot save any work at all, leading to repeated data loss. The app is effectively unusable after the CSV import. Any user who imports CSV data with similar encoding characteristics would hit the same crash.","recommended_fix":"Investigate the save/serialization path for unhandled encoding exceptions. Check how imported CSV data is stored in memory and written to disk — likely the CSV reader accepted data in a non-UTF-8 encoding that the save routine does not handle. Add encoding validation or normalization during CSV import, and add graceful error handling (with a readable error message) in the save path so it never crashes silently.","proposed_test_case":"Create a CSV file containing tasks with mixed encodings (e.g., Latin-1 characters, curly quotes, BOM markers, null bytes) and import it into TaskFlow. Verify that (a) the import either normalizes encoding or rejects invalid characters with a clear message, and (b) saving after import succeeds or produces a user-readable error rather than crashing.","information_gaps":["Exact content/encoding of the original CSV file used for import","Full text of the encoding error dialog (disappears too quickly to read)","Whether deleting the imported tasks restores the ability to save","Application crash logs (e.g., from macOS Console or ~/Library/Logs)"]}

---

