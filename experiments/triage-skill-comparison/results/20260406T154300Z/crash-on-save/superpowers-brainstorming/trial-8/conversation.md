# Conversation: crash-on-save x superpowers-brainstorming

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — losing work to crashes is definitely frustrating and we want to get this sorted out.

To help us narrow this down: what are you doing in TaskFlow when the crash happens?

1. Saving a new task
2. Editing and saving an existing task
3. Saving changes to a project or board
4. Something else (please describe)

Also, if you see any error message or popup before the crash, could you paste it here?

---

### reporter

I'm saving a task list that has about 200 tasks in it. I click the 'Save' button in the toolbar and the app just crashes. Right before it closes I can see a dialog flash briefly — it says something about 'encoding' but it disappears too fast for me to read the whole thing. This started after I imported a bunch of tasks from a CSV file. If I remove those imported tasks, saving works fine again.

---

### triage-agent

[RESOLVED] {"title":"App crashes on save when task list contains CSV-imported tasks (encoding error)","problem":"When a user imports tasks from a CSV file and then attempts to save a task list containing those imported tasks, the application crashes. A dialog referencing 'encoding' flashes briefly before the crash. Removing the CSV-imported tasks from the list allows saving to work normally.","root_cause_hypothesis":"The CSV import path is ingesting task data without normalizing character encoding. The imported data likely contains non-UTF-8 characters (e.g., Latin-1, Windows-1252 smart quotes, BOM markers, or other special characters) that the save/serialization code does not handle, causing an unhandled encoding exception.","reproduction_steps":["Prepare a CSV file with ~200 tasks (likely containing non-ASCII characters such as smart quotes, accented characters, or a BOM)","Import the CSV file into TaskFlow","Create or open a task list containing the imported tasks","Click the 'Save' button in the toolbar","Observe the encoding error dialog flash and the application crash"],"environment":"Not specified — likely desktop application. The issue is data-dependent rather than environment-dependent.","severity":"high","impact":"Users who import tasks from CSV files lose unsaved work when the app crashes on save. This blocks a core workflow (CSV import + save) and causes data loss.","recommended_fix":"1. Add a try/catch around the save serialization path to surface the full encoding error instead of crashing. 2. Audit the CSV import code to ensure it detects and normalizes input encoding to UTF-8 (or the application's internal encoding) at import time. 3. Add defensive encoding handling in the save path so malformed characters are replaced or escaped rather than causing an unhandled exception.","proposed_test_case":"Import a CSV file containing non-ASCII characters (smart quotes, accented characters, BOM marker, mixed encodings) into a task list with 200+ tasks, then save. Verify the save completes without crashing and the data round-trips correctly.","information_gaps":["Exact encoding of the reporter's CSV file (UTF-8 with BOM, Latin-1, Windows-1252, etc.)","Full text of the encoding error dialog","Operating system and application version","Whether this affects all CSV files or only specific ones"]}

---
