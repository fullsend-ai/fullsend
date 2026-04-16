# Conversation: crash-on-save x omo-prometheus

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I understand how frustrating it is to lose work.

To start narrowing this down, I need a few specifics:

**What exactly happens when you say "crashes"?** Does the app freeze, close entirely, show an error message, or something else? If there's an error message or dialog, please paste the exact text.

Also, **where in TaskFlow are you saving?** For example, are you creating a new task, editing an existing task, saving project settings, or something else?

---

### reporter

It just closes. The whole app disappears. I think there's some kind of error dialog that flashes for like a split second but I can't read it before everything shuts down. Something about encoding maybe? I don't know. I'm just clicking Save in the toolbar like I always do. It's a task list with a bunch of tasks in it. This has been happening for days now and I can't get any work done.

---

### triage-agent

Thanks for that detail — the flash of an encoding-related error is a really useful clue.

Since you said you're clicking Save in the toolbar "like I always do," does that mean this **used to work fine** and broke recently? If so, do you remember roughly when it started — for example, after an update, or after you added specific tasks?

Also, do any of your tasks contain **special characters, emoji, accented letters, or content pasted from another application** (like Word or a web page)? Encoding crashes often trace back to specific characters in the data being saved.

---

### reporter

Yeah it used to work fine until like a week ago. I imported a CSV file with a bunch of tasks into it, that's when things went sideways. I don't know about special characters, it's just normal task names. Can you guys just fix the save button? I shouldn't have to worry about what characters are in my tasks.

---

### triage-agent

That's very helpful — the CSV import timing lines up perfectly with the encoding error you glimpsed.

Could you try one quick test for me? **Create a brand-new task list with just one or two manually typed tasks and hit Save.** Does that work, or does it still crash?

This will tell us whether the problem is specifically in the imported data or something broader. And if you still have that CSV file, could you attach it to this issue (or let me know where it came from — what tool or system exported it)? That would let our developers reproduce the crash directly.

---

### reporter

I don't have time to be creating test lists, I have actual work to do. But yeah, small lists save fine — I've got another project with like 30 tasks and that one works. It's this big list that crashes. I don't have the CSV anymore, it was exported from some spreadsheet my manager sent me. Look, the save button is broken for my main project, that's what matters.

---

### triage-agent

Totally understand — you shouldn't have to worry about this, and we want to get it fixed for you.

One thing that would help our developers jump straight to the fix: **does TaskFlow have a log file on your machine?** Depending on your system, it's usually in one of these locations:

- **Windows:** `%APPDATA%\TaskFlow\logs\`
- **Mac:** `~/Library/Logs/TaskFlow/`
- **Linux:** `~/.config/TaskFlow/logs/`

If you can find a recent log file there and paste or attach the last ~20 lines, it almost certainly captured that encoding error you saw flash by — and that will tell our developers exactly what's breaking.

Also, quick: what OS are you on, and do you know which version of TaskFlow you're running (Help → About)?

---

### reporter

I'm on a Mac, macOS 14.2. TaskFlow is version 2.3.1 I think. I'm not going to go digging through log files, I don't even know how to get to that Library folder. Can you just look into the CSV import thing? That's clearly what broke it.

---

### triage-agent

[RESOLVED] {"title":"Crash on save when task list contains CSV-imported data with non-UTF-8 encoding","problem":"TaskFlow crashes (abrupt app closure) when the user clicks Save in the toolbar for a project whose tasks were populated via CSV import. A brief error dialog referencing 'encoding' flashes before the app terminates. Manually created task lists and other projects save without issue.","root_cause_hypothesis":"The CSV import path ingests task data without normalizing its character encoding to TaskFlow's internal format (likely UTF-8). When the save routine later serializes the project, it encounters byte sequences that are invalid in the expected encoding, triggering an unhandled exception that crashes the app. The source CSV was exported from a spreadsheet application (likely Excel on Windows) which commonly produces Windows-1252 or Latin-1 encoded files, or UTF-8 with BOM — any of which could cause this if the save path assumes clean UTF-8.","reproduction_steps":["Create a CSV file with task names containing characters outside ASCII (accented characters, curly quotes, em-dashes, or BOM marker) using a tool like Excel with Windows-1252 encoding","Open TaskFlow 2.3.1 on macOS","Import the CSV into a new or existing project","Click Save in the toolbar","Observe: app crashes with a brief encoding-related error dialog"],"environment":"macOS 14.2 (Sonoma), TaskFlow version 2.3.1","severity":"high","impact":"Any user who imports a CSV with non-UTF-8 encoding is unable to save their project, resulting in complete data loss for that session. The user reports being unable to work on their main project for days. This likely affects anyone importing CSVs from Excel or legacy spreadsheet tools.","recommended_fix":"1. Inspect the CSV import code path — verify whether it detects and normalizes source encoding (e.g., using charset detection like ICU or chardet) before storing task data internally. 2. Inspect the save/serialization code path — add encoding error handling (e.g., replace or escape invalid sequences) so that malformed strings produce a user-visible error rather than an unhandled crash. 3. Add a try/catch around the save routine to gracefully surface the error dialog instead of terminating the app. 4. Consider re-encoding all imported data to UTF-8 at import time as the primary fix.","proposed_test_case":"Import a CSV file saved with Windows-1252 encoding containing characters like curly quotes (\\u201c\\u201d), em-dashes (\\u2014), and accented characters (e.g., \\u00e9). Verify that (a) the import succeeds, (b) the project saves without crashing, and (c) the characters round-trip correctly. Additionally, test with a UTF-8 BOM file and a Latin-1 file.","information_gaps":["Exact error message text (flashes too quickly for reporter to read; recoverable from logs or code inspection)","Original CSV file is no longer available — reproduction requires constructing a test CSV","Specific spreadsheet application and OS that exported the CSV (reporter only knows 'a spreadsheet from manager')","Whether the issue is specific to certain characters in the CSV or any non-ASCII content"]}

---
