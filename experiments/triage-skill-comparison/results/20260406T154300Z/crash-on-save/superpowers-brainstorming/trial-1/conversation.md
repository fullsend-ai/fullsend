# Conversation: crash-on-save x superpowers-brainstorming

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — losing work to crashes is definitely frustrating, and we want to get this sorted out.

To start narrowing this down: what are you saving when the crash happens?

1. **A new task** (creating a task for the first time)
2. **Edits to an existing task** (updating title, description, status, etc.)
3. **Project or workspace settings**
4. **Something else** — please describe

Also, when you say "crashes," does the app close entirely, or do you see an error message / blank screen?

---

### reporter

I'm saving edits to a task list — I have about 200 tasks in it. When I click 'Save' in the toolbar, the app closes entirely. I see a brief flash of some dialog box that mentions something about 'encoding' but it disappears too fast to read the whole thing before the app just shuts down.

---

### triage-agent

Thanks — that 'encoding' flash is a really useful clue.

Based on what you've described, my best guess is that one or more of those 200 tasks contains special characters (emoji, accented letters, or text pasted from another app like Word or Excel) that the save routine can't encode properly, causing the crash.

Does that sound plausible? Specifically:

1. **Yes** — some tasks have emoji, non-English characters, or text pasted from another application
2. **Not that I know of** — it's all plain English text, nothing unusual
3. **Not sure** — I'd have to check

Also — does the crash happen every time you click Save, or only sometimes?

---

### reporter

Yes, that sounds right actually! I imported a bunch of tasks from a CSV file a little while back, and I think some of them have fancy characters — like curly quotes and em-dashes — that came from the original spreadsheet. The crash happens every single time I click Save. It never works. But I just noticed that auto-save doesn't seem to cause the same crash, which is weird. And smaller lists I have with under 50 tasks save fine, even ones that have some of the imported data in them.

---

### triage-agent

[RESOLVED] {"title":"App crashes on toolbar Save for large task lists containing non-ASCII characters (curly quotes, em-dashes) imported from CSV","problem":"When a user clicks the 'Save' button in the toolbar on a task list with approximately 200 tasks — some containing non-ASCII characters (curly quotes, em-dashes) imported from a CSV file — the app crashes immediately. A dialog briefly flashes mentioning 'encoding' before the app closes. Auto-save does not trigger the crash, and smaller lists (under 50 tasks) with similar characters save successfully via the toolbar.","root_cause_hypothesis":"The toolbar Save button uses a different serialization/write path than auto-save — likely a bulk write that re-encodes the entire list at once. This bulk path does not handle non-ASCII characters (e.g., Windows-1252 curly quotes U+2018/U+2019, em-dashes U+2014) correctly, throwing an unhandled encoding exception. The size threshold suggests the bulk path may only be used above a certain task count (an optimization branch), which is why small lists with the same characters save fine — they may still use the simpler per-task path that auto-save also uses.","reproduction_steps":["Create or import a CSV file containing tasks with curly quotes (‘ ’ “ ”) and em-dashes (—) — at least 50+ tasks","Import the CSV into TaskFlow as a new task list","Edit any task in the list (to dirty the save state)","Click the 'Save' button in the toolbar","Observe the app crash with a brief 'encoding' dialog flash"],"environment":"Not OS-specific based on report. Triggered by CSV-imported data containing Windows-1252/Unicode punctuation characters. List size threshold appears to be somewhere between 50 and 200 tasks.","severity":"high","impact":"Any user who imports tasks from external sources (CSV/spreadsheets) and accumulates a list above the size threshold is completely unable to manually save. They lose any work done since the last auto-save each time they hit Save. This is a data-loss scenario for users who rely on manual save.","recommended_fix":"1. Investigate the toolbar Save code path vs the auto-save code path — identify where they diverge in serialization logic, especially for lists above the size threshold. 2. Check for a bulk/batch write optimization that kicks in for larger lists and inspect its character encoding handling. 3. Ensure the bulk path uses UTF-8 encoding (or matches whatever encoding auto-save uses successfully). 4. Add proper error handling around the save serialization so encoding failures surface as a recoverable error dialog rather than an app crash.","proposed_test_case":"Create a task list with 200+ tasks where at least 10 tasks contain non-ASCII characters (curly quotes, em-dashes, accented characters, emoji). Trigger a toolbar Save and verify it completes without error. Additionally, add a unit test for the bulk serialization path that explicitly includes Windows-1252 punctuation characters (U+2018, U+2019, U+201C, U+201D, U+2014) and asserts successful round-trip encoding.","information_gaps":["Exact task count threshold where the save path switches from per-task to bulk write","Whether the crash also affects the File > Save menu item or only the toolbar button","Specific OS and app version (not critical since the root cause is in the save code path, not platform-specific)","Whether the crash leaves a corrupted save file on disk or fails before writing"]}

---
