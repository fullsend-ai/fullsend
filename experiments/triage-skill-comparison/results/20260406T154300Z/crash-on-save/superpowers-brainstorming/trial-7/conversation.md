# Conversation: crash-on-save x superpowers-brainstorming

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — losing work is definitely frustrating, and we want to get this sorted out.

To help us track this down, could you tell us where in the app the crash happens when you save?

1. Saving a new task
2. Editing and saving an existing task
3. Saving project/board settings
4. It happens on any save action throughout the app
5. Something else (please describe)

---

### reporter

It's option 5 — it happens when I click the 'Save' button in the toolbar to save my task list. I have about 200 tasks in there. It doesn't crash on every save though, it started after I imported a bunch of tasks from a CSV file.

---

### triage-agent

That's really helpful context — the CSV import connection is a strong clue.

One more thing that would help us pinpoint this: when the crash happens, do you see any error message or dialog before the app closes, or does it just disappear silently?

Also, if you've had a chance to notice — does the crash seem to happen more reliably right after importing, or does it also happen if you close and reopen the app and then try saving?

1. I see an error message (please paste it if you can!)
2. The app just disappears / freezes with no message
3. I get a spinning/loading state that never finishes
4. I'm not sure — it happens too fast to tell

---

### reporter

There is a brief flash of an error dialog — it goes by really fast but I caught the word 'encoding' in it before the app just closes. So I guess option 1, but I can't paste the full message because it disappears too quickly.

And to your second question, it happens every time I try to save, even after closing and reopening the app. As long as those imported tasks are in the list, hitting Save crashes it. I actually tested removing the imported tasks and it saves fine after that.

---

### triage-agent

[RESOLVED] {"title":"App crashes on save with encoding error when task list contains CSV-imported tasks","problem":"After importing tasks from a CSV file, clicking the toolbar Save button to save the task list causes the app to crash. A brief error dialog referencing 'encoding' flashes before the app closes. The crash is 100% reproducible whenever the imported tasks are present in the list, persists across app restarts, and is eliminated by removing the imported tasks.","root_cause_hypothesis":"The CSV import path accepts text data with an encoding (e.g., Latin-1, Windows-1252, or UTF-8 with BOM) or characters (e.g., special/non-ASCII characters, null bytes) that the save/serialization layer cannot handle. When the save routine iterates over task data to serialize it, it encounters these characters and throws an unhandled encoding exception, crashing the app.","reproduction_steps":["Create or obtain a CSV file with ~200 tasks (likely containing non-ASCII or special characters)","Import the CSV file into TaskFlow using the CSV import feature","Click the 'Save' button in the toolbar to save the task list","Observe the brief 'encoding' error dialog followed by the app crashing"],"environment":"Not platform-specific based on available information. The trigger is the CSV file content/encoding, not the OS or app version.","severity":"high","impact":"Any user who imports tasks from CSV risks persistent crashes on save, leading to data loss and inability to use the app until imported tasks are manually removed. This blocks a core workflow (CSV import → continued use).","recommended_fix":"1. Add encoding detection/normalization in the CSV import path (e.g., detect source encoding and transcode to UTF-8 on import). 2. Add defensive encoding handling in the save/serialization path so malformed characters are handled gracefully rather than crashing. 3. Ensure the error dialog is non-fatal and stays visible long enough for users to read and report it.","proposed_test_case":"Import a CSV file containing mixed encodings (UTF-8, Latin-1, Windows-1252) and special characters (em-dashes, accented characters, null bytes, BOM markers), then verify that (a) import succeeds or reports clear errors per-row, and (b) saving the task list after import completes without crashing.","information_gaps":["The exact encoding of the reporter's CSV file is unknown — reproducing with various encodings should cover this","The full error message was not captured — adding proper error logging or a persistent error dialog would help future debugging"]}

---

