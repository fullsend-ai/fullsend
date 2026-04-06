# Conversation: crash-on-save x superpowers-brainstorming

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — losing work to crashes is definitely frustrating and we want to get this sorted out.

To help us narrow down what's happening, could you tell us where in TaskFlow the crash occurs when you save?

1. Saving a new task
2. Editing/updating an existing task
3. Saving project or workspace settings
4. It happens on any save action throughout the app
5. Something else (please describe)

Also, do you see any error message or does the app just close/freeze without warning?

---

### reporter

It's option 2 — I'm editing an existing task list and when I click 'Save' in the toolbar, the app just dies. I do see a brief flash of some error dialog that mentions something about 'encoding' but it disappears so fast I can't read the whole thing before the app closes. Auto-save seems to work fine weirdly enough, it's only when I manually hit Save.

---

### triage-agent

That's really helpful — the encoding error and the manual-Save-only behavior are great clues.

One more question that will help us pin this down: does the crash happen with **any** task list you edit, or only specific ones?

If it's specific ones, do those tasks happen to contain any of the following?

1. Emoji (e.g. ✅, 🔥)
2. Accented or non-English characters (e.g. café, naïve, 日本語)
3. Copy-pasted content from another app (Word, Google Docs, etc.)
4. It crashes on every task list, even simple ones with plain English text
5. Not sure — I haven't tested other task lists

This will tell us whether the encoding issue is triggered by specific content or is a broader problem with the manual save path.

---

### reporter

It's only specific task lists — the ones where I imported data from a CSV file. The imported tasks have things like em-dashes and curly quotes in the task names, so option 3 is closest. I copy-pasted the data into a CSV from a Word document originally. If I remove those imported tasks, saving works fine. Also worth mentioning, it only seems to crash when the list is big — my smaller lists with under 50 tasks save fine even with the imported data.

---

### triage-agent

[RESOLVED] {"title":"Manual save crashes with encoding error on large task lists containing Word smart-quotes/em-dashes imported via CSV","problem":"When a user edits a task list that contains special typographic characters (curly/smart quotes, em-dashes) originally pasted from Microsoft Word into a CSV and then imported, clicking the manual 'Save' button in the toolbar causes the application to crash. A brief error dialog referencing 'encoding' flashes before the app closes. Auto-save does not trigger the crash. The issue only manifests when the task list contains roughly 50 or more tasks; smaller lists with the same characters save successfully.","root_cause_hypothesis":"The manual save code path likely uses a different serialization or encoding routine than auto-save — probably one that does not handle Windows-1252/CP1252 characters (U+2018, U+2019 curly quotes; U+2013/U+2014 em-dashes) correctly, or attempts a strict ASCII/Latin-1 encoding. The size threshold suggests the encoder may process data in chunks/buffers, and the encoding failure is triggered when a multi-byte or extended character falls at or near a chunk boundary, which only occurs in larger payloads.","reproduction_steps":["Create a CSV file with task names containing Word-style curly quotes (‘ ’ “ ”) and em-dashes (—) — paste from a Word document to reproduce the exact byte sequences.","Import the CSV into TaskFlow to create a task list with 50+ tasks.","Open the imported task list and edit any task (e.g., change a due date).","Click the 'Save' button in the toolbar.","Observe the app crash with a brief 'encoding' error dialog."],"environment":"Not yet confirmed — reporter did not specify OS, app version, or platform (desktop vs. web). Likely desktop app given the 'app just dies' description.","severity":"high","impact":"Any user who imports CSV data originating from Word (or similar rich-text sources) into large task lists will lose unsaved work on every manual save attempt. Auto-save mitigates data loss but the crash disrupts workflow and erodes trust. CSV import from productivity tools is a common onboarding path.","recommended_fix":"1. Compare the manual save serialization path against the auto-save path — identify where encoding is handled differently. 2. Ensure the manual save path uses UTF-8 encoding throughout (reading task data from the model, serializing, and writing to storage). 3. Investigate chunked/buffered writes in the manual save path — if data is split into fixed-size byte buffers, ensure splits do not occur mid-character. 4. Add a targeted try/catch around the save serialization to surface the full encoding error to the user instead of crashing. 5. Consider normalizing imported CSV data to UTF-8 NFC at import time to prevent problematic characters from persisting.","proposed_test_case":"Create a task list with 100+ tasks whose names contain Windows-1252 typographic characters (curly single/double quotes, em-dashes, ellipses). Perform a manual save and verify it completes without error. Additionally, test with task names containing emoji, CJK characters, and mixed scripts to confirm the fix generalizes.","information_gaps":["Exact OS and app version (desktop vs. web) — does not change the fix approach but useful for reproduction.","Whether the CSV import step itself should normalize encoding, or whether that's a separate improvement.","Exact error message text — only 'encoding' was partially visible; full stack trace from logs would confirm the root cause."]}

---

