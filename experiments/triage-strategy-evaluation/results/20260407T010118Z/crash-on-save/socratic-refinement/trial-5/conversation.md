# Conversation: crash-on-save x socratic-refinement

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I can understand how frustrating it is to lose work to crashes.

To help us dig into what's going on, could you tell me a bit more about what you were working on when the crash happened? For example, what kind of task or document were you saving, and roughly how much content was in it? And when you say the app "crashes," what exactly do you see — does the whole app close, do you get an error message, or does it freeze up?

---

### reporter

Look, I have like 200 tasks in there and it just closes. The whole app just shuts down when I hit Save. I think there's some kind of error that flashes for a split second but I can't read it before the app is gone. I just need to be able to save my work without the app dying on me.

---

### triage-agent

That's really helpful, thank you. A fleeting error message right before the crash is a great clue — if you're up for it, could you try again and see if you can grab a screenshot or screen recording of that moment? (On most systems, a quick video capture would catch what the eye can't.)

Also, I'm curious about the history here — has saving always been a problem with this particular project, or was there a time when it worked fine? If it used to work, do you recall roughly when it started crashing — for instance, after an app update, or after your task list grew past a certain size?

---

### reporter

I'm not going to sit here recording my screen every time the app crashes, sorry. It used to save fine. It started crashing after I imported a bunch of tasks from a CSV file. Before that it was working. Can you just look into the save function or whatever? I don't have time to keep troubleshooting this myself.

---

### triage-agent

Totally fair — no need to record anything. The CSV import is a really useful clue, so let me follow that thread.

Could you tell me a bit about that CSV file? For instance, where did it come from (exported from another app, hand-built in a spreadsheet, etc.), and was there anything unusual about the tasks in it — like very long descriptions, special characters, or fields left blank? Basically, anything that might have been "messy" compared to the tasks you'd created directly in the app.

Also, one low-effort thing that might save us both time: if you check your system's app/crash logs, there may be a record of that fleeting error message. On Windows that'd be Event Viewer, on Mac it's Console.app, and on Linux it'd be `journalctl` or `~/.local/share/TaskFlow/logs/`. If you happen to spot something there, it'd go a long way — but no pressure if not.

---

### reporter

The CSV came from some spreadsheet my coworker sent me. I don't know what was in it exactly, I just imported it. Maybe there were some weird characters in there, I don't know. I'm not going to go digging through system logs, I'm not a developer. All I know is it worked before the import and now it doesn't. If I delete the imported tasks it works again but I need those tasks.

---

### triage-agent

[RESOLVED] {"title":"App crashes on save when project contains CSV-imported tasks","problem":"The application force-closes immediately when the user saves a project that contains tasks imported from a CSV file. A brief error message flashes on screen before the app exits, but it disappears too quickly to read. The user has approximately 200 tasks, a portion of which were imported. Saving works normally if the imported tasks are deleted first.","root_cause_hypothesis":"The CSV import likely introduces data that the save/serialization code path cannot handle — most probably special characters, encoding issues, unexpectedly long fields, or empty/null values in required fields. The save function appears to throw an unhandled exception when it encounters this data, causing the application to crash rather than fail gracefully.","reproduction_steps":["Create a project with some manually-created tasks and confirm saving works","Prepare a CSV file with potential edge cases: special characters, very long text fields, empty fields, non-UTF-8 encoding","Import the CSV file into the project","Attempt to save the project","Observe crash — app should force-close with a brief error flash"],"environment":"Not specified by reporter. The project contains approximately 200 tasks including a batch imported from a CSV that was originally built in a spreadsheet by a coworker.","severity":"high","impact":"User loses unsaved work on every save attempt and cannot use the application productively. The only workaround (deleting imported tasks) means losing data the user needs. Any user who imports CSV data with similar characteristics would hit the same crash.","recommended_fix":"1. Investigate the save/serialization code path for unhandled exceptions — add proper error handling so a single bad record doesn't crash the app. 2. Audit the CSV import function to sanitize or validate incoming data at import time (encoding normalization, field length limits, null handling). 3. Check for the specific fleeting error by running the app from a terminal or examining crash logs to identify the exact exception. 4. Consider adding a save-to-temp-then-rename pattern so that a crash during save doesn't destroy existing data.","proposed_test_case":"Import a CSV file containing tasks with edge-case data (special/unicode characters, empty required fields, extremely long descriptions, mixed encodings) into a project, then call save. Verify the save completes without crashing. If any records are invalid, verify the app surfaces a clear error and still saves the valid data.","information_gaps":["Exact OS and app version (not critical — the bug is data-dependent, not platform-dependent)","Contents of the specific CSV file that triggered the issue (would speed up reproduction but developer can test with synthetic edge-case CSVs)","The exact error message that flashes before the crash (developer can capture this from logs or terminal output)"]}

---
