# Conversation: crash-on-save x omo-prometheus

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I understand how frustrating it is to lose work. To help us track this down efficiently, I need a few specifics:

1. **What are you saving?** (e.g., a new task, editing an existing task, project settings, etc.)
2. **What exactly happens when it crashes?** Does the app close entirely, do you see an error message or dialog, or does it freeze/hang?
3. **When did this start?** Was saving working for you before, and if so, did anything change (app update, OS update, new browser, etc.)?

Any one of these would help, but all three would let us move fast.

---

### reporter

I'm saving my task list — I have about 200 tasks in it. When I hit the Save button in the toolbar, the app just closes completely. There's a brief flash of some kind of error dialog that mentions something about 'encoding' but it disappears too fast for me to read the whole thing.

This started after I imported a bunch of tasks from a CSV file. Saving was working totally fine before that import. I haven't updated the app or my OS recently — it's definitely related to that import somehow.

---

### triage-agent

Thanks, that's very helpful — the CSV import is almost certainly the trigger here. The 'encoding' error strongly suggests some character data in that file is tripping up the save process.

Could you tell me a bit about that CSV file?

- **Where did it come from?** (e.g., exported from another app like Excel, Notion, Jira, or hand-edited in a text editor?)
- **Does the task data contain any non-English text, special characters, or emoji?** (e.g., accented characters like é or ñ, symbols like ™ or ©, or any CJK characters)

Also — if you're comfortable sharing the CSV file (or even just a few sample rows), that would help us reproduce this quickly. If not, the answers above will still get us moving.

---

### reporter

The CSV was exported from Excel. And yeah, now that you mention it, the task names do have some special characters — things like em-dashes and curly quotes (the fancy ones that Word and Excel like to use). No emoji or foreign language text though, just those typographic characters.

I don't have the original CSV anymore unfortunately, but I can tell you that if I go in and manually delete the imported tasks, saving works fine again. Also, I tried with a smaller list — like under 50 tasks — and it saves okay even with the imported ones still in there. It only seems to crash when the list is bigger.

---

### triage-agent

[RESOLVED] {"title":"App crashes on save when task list contains Excel-imported tasks with typographic characters (encoding error, size-dependent)","problem":"Saving a task list crashes the application (immediate close with a brief 'encoding' error dialog) when the list contains tasks imported from an Excel-generated CSV file. The imported tasks contain Windows-1252 typographic characters (curly/smart quotes, em-dashes). The crash only occurs when the imported task count exceeds roughly 50; smaller sets save successfully.","root_cause_hypothesis":"The save/serialization path likely assumes UTF-8 (or ASCII) encoding but the Excel CSV import preserves Windows-1252 encoded characters (CP1252 curly quotes U+201C/U+201D, em-dashes U+2014). When the save routine processes these characters at scale, it hits an unhandled encoding exception — possibly in a batched or buffered write operation, which explains the size-dependent threshold. The error is not caught gracefully, causing the app to crash instead of surfacing a user-facing error.","reproduction_steps":["Create a CSV file in Excel with ~200 task entries, ensuring some task names contain curly/smart quotes and em-dashes (type in Word or Excel and let auto-correct produce them)","Import the CSV into TaskFlow using the CSV import feature","Verify the tasks appear in the task list","Click the Save button in the toolbar","Observe: app crashes with a brief encoding-related error dialog"],"environment":"No specific OS/version provided, but the issue is data-dependent rather than environment-dependent. Excel on Windows is the source of the CSV, producing Windows-1252 encoded characters.","severity":"high","impact":"Any user who imports tasks from Excel-generated CSVs containing typographic characters risks data loss — the app crashes on save with no recovery. The crash is deterministic once the threshold is crossed, and users lose any unsaved work. This likely affects a significant portion of users migrating from spreadsheet-based workflows.","recommended_fix":"1. Investigate the save serialization path for encoding handling — check whether it enforces or assumes a specific encoding. 2. Ensure the CSV import normalizes characters to UTF-8 at import time, or that the save path handles multi-byte/non-ASCII characters correctly. 3. Add a try/catch around the save operation so encoding errors surface as user-facing error messages rather than crashes. 4. Investigate the size-dependent behavior — check if the save uses batched writes or a fixed buffer that interacts with multi-byte character expansion. 5. Consider adding an import-time sanitization option that replaces typographic characters with ASCII equivalents.","proposed_test_case":"Create a task list with 200+ tasks where task names contain Windows-1252 typographic characters (curly quotes, em-dashes, ellipsis). Save the list and verify: (a) no crash occurs, (b) the characters are preserved correctly in the saved output, and (c) the file can be reloaded without data corruption. Also test the boundary condition around 50 tasks to ensure the fix isn't size-dependent.","information_gaps":["Exact error message (the dialog flashes too fast to read fully — application logs may capture it)","Whether the app uses a specific serialization format (JSON, SQLite, custom binary) for the save file","Exact size threshold where the crash begins (reporter estimates ~50 but hasn't tested precisely)","Whether auto-save (if it exists) is also affected or only the manual toolbar save"],"confidence":{"root_cause":"medium","reproduction":"high","severity_assessment":"high"}}

---
