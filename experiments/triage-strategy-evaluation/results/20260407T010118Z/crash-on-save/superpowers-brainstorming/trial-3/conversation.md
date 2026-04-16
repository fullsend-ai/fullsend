# Conversation: crash-on-save x superpowers-brainstorming

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — losing work to crashes is definitely frustrating, and we want to get this sorted out.

To help us narrow things down: where in TaskFlow are you when the crash happens?

- **Editing a task** (saving changes to an existing task)
- **Creating a new task** (saving a brand-new task)
- **Editing project settings** (saving project-level changes)
- **Something else** (please describe briefly)

Also, when you say "crashes," does the app close entirely, or do you see an error message / frozen screen?

---

### reporter

I'm editing my task list — I have a bunch of tasks in there. When I hit Save in the toolbar the whole app just closes on me. Sometimes I see some kind of error flash for a split second but it disappears too fast to read. This has been happening for days now and I've lost work multiple times. Can you guys just fix the save button?

---

### triage-agent

Thanks for those details — that helps a lot. The fact that it closes entirely (rather than freezing) and that you have a lot of tasks in the list makes me want to check one thing:

Roughly how many tasks are in the list you're saving?

- **Under 50**
- **50–200**
- **200+**
- **Not sure, but it's a lot**

This will help us figure out whether the crash is related to the size of the data being saved or something else about the save action itself.

---

### reporter

I'd say around 200 tasks. Look, does it really matter how many tasks I have? The save button should just work. It was working fine before.

---

### triage-agent

Understood — you're right that the save button should just work regardless of task count. That detail does help us though, so thank you.

Since it was working fine before: did the crashes start after a specific app update, or did they seem to begin on their own one day?

- **After an update** (if you remember roughly when, that's a bonus)
- **It just started happening** — no update I noticed
- **Not sure**

This is the last thing we need to narrow it down — it tells our developers exactly where to look in the code history.

---

### reporter

It wasn't an update. It just started happening out of nowhere maybe a week or so ago. I was adding a bunch of tasks to the project around that time — imported some from a spreadsheet — but I don't see why that would matter. The point is it was working and then it stopped.

---

### triage-agent

[RESOLVED] {"title":"App crashes on save with ~200 tasks after spreadsheet import","problem":"The app force-closes when the user clicks Save in the toolbar while editing a task list containing approximately 200 tasks. A brief error message flashes before the app closes. The issue began roughly one week ago, around the same time the user imported a large number of tasks from a spreadsheet. Save was working normally prior to the import.","root_cause_hypothesis":"The spreadsheet import likely introduced data that the save/serialization code path cannot handle — possible causes include special characters, unexpected field formats, excessively long text, or data type mismatches (e.g., dates or numbers stored as strings). The save operation hits an unhandled exception when serializing these records, crashing the process.","reproduction_steps":["Create a project in TaskFlow","Import a large set of tasks (~200) from a spreadsheet (CSV or similar)","Open the task list for editing","Click Save in the toolbar","Observe: app crashes / closes with a brief error flash"],"environment":"Not specified (reporter did not mention OS or app version). The issue is likely platform-independent since it appears to be a data-triggered crash.","severity":"high","impact":"The user cannot save any edits to their task list, causing repeated data loss. This blocks all productive use of the project. Any user who has imported a large number of tasks from a spreadsheet may be affected.","recommended_fix":"1. Add error handling/logging around the save serialization path so crashes produce a persistent error message instead of silently closing. 2. Inspect the imported task data for malformed fields (special characters, encoding issues, type mismatches, oversized values). 3. Check whether the save operation has a size or memory limit being exceeded at ~200 records. 4. Add input validation/sanitization to the spreadsheet import feature to prevent bad data from entering the system.","proposed_test_case":"Import a spreadsheet containing 200+ tasks with a variety of edge-case data (special characters, Unicode, very long strings, empty fields, numeric strings in text fields) and verify that saving the task list completes without error. Additionally, verify that if serialization does fail, a clear error message is displayed and the app does not crash.","information_gaps":["Exact error message (flashes too fast for reporter to read — crash logs would reveal this)","Specific spreadsheet format used for import (CSV, XLSX, etc.)","Whether the crash occurs with a smaller subset of the imported tasks (isolating the problematic records)","OS and app version"]}

---
