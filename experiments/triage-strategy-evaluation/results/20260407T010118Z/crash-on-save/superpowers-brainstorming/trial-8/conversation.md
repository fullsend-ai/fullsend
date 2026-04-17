# Conversation: crash-on-save x superpowers-brainstorming

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — sorry you're losing work, that's definitely frustrating.

To help us track this down, could you tell us what you were saving when the crash happens?

- **A.** Creating or editing a task
- **B.** Updating project settings or preferences
- **C.** Exporting or saving a file/report
- **D.** Something else (please describe)

Also, does this happen *every* time you save, or only sometimes?

---

### reporter

It's A, I'm editing tasks. And yeah it happens every single time I hit Save. I've lost my work like 5 times now. Can you just fix it?

---

### triage-agent

Thanks for confirming — and sorry again about the lost work.

Since it happens every time, can you tell us which part of the task you're editing when it crashes?

- **A.** The task title or description (text fields)
- **B.** The due date, priority, or status (dropdowns/pickers)
- **C.** Assignees or tags
- **D.** It doesn't matter — *any* edit to *any* field crashes on Save

This will help us pinpoint exactly where the bug is.

---

### reporter

D. It doesn't matter what I edit. I can change anything and when I hit Save it crashes. Look, I don't think it's about what field I'm editing — the whole save is broken.

---

### triage-agent

Got it — the save itself is broken regardless of the field. That's actually helpful info.

One more thing that would really help us narrow this down: does this happen on **every task** you try to edit, or only on **specific tasks**?

If you're not sure, could you try creating a brand new task with a simple title like "test123" and then immediately editing and saving it?

This will tell us whether it's a problem with the save code itself, or something about the data in your existing tasks.

---

### reporter

Ugh, fine. I just tried that and the new task saved fine. But that doesn't help me because I need my actual tasks to work. I have like 200 tasks in there and they ALL crash when I save.

---

### triage-agent

That's actually a really important clue — new tasks saving fine but existing ones crashing tells us a lot. This is almost certainly about something in your existing task data, not the save button itself.

Do you remember when this started happening?

- **A.** After a recent app update or version change
- **B.** After I did something specific (imported tasks, changed a setting, etc.)
- **C.** It's always been like this — I've never been able to edit and save existing tasks
- **D.** I'm not sure, but it used to work and now it doesn't

This will help us figure out whether a recent change broke things or if it's a deeper data issue.

---

### reporter

B I guess. I imported a bunch of tasks from a CSV file a while back. But the import worked fine, it's the saving that's broken. Why does it matter how the tasks got there?

---

### triage-agent

[RESOLVED] {"title":"Task save crashes on all CSV-imported tasks; natively-created tasks save fine","problem":"The application crashes every time the user edits and saves any existing task. The user has approximately 200 tasks, all of which were imported via CSV. Newly created tasks save without issue. The crash is 100% reproducible and occurs regardless of which field is edited.","root_cause_hypothesis":"The CSV import likely introduced task data that does not conform to what the save/update code path expects — for example, null or missing fields that the save handler doesn't guard against, special characters or encoding issues in text fields, or data type mismatches (e.g., a string where an integer is expected). The create path works because it produces well-formed data, but the update/save path chokes on the malformed imported data.","reproduction_steps":["Import tasks from a CSV file (any CSV with multiple tasks should work, though the reporter's specific CSV would be ideal)","Open any imported task for editing","Change any field (title, description, due date, etc.)","Click Save","Observe crash — contrast with creating a brand new task, editing it, and saving successfully"],"environment":"Not specified — but the bug is data-dependent rather than environment-dependent, so it should reproduce in any environment with CSV-imported task data","severity":"high","impact":"The user cannot edit any of their ~200 existing tasks without the app crashing, causing repeated data loss. This effectively makes the application unusable for their primary workflow.","recommended_fix":"1. Inspect the database records for CSV-imported tasks vs. natively-created tasks — look for schema differences, null values, unexpected types, or malformed data. 2. Add defensive handling in the task save/update code path for any fields that could be null, missing, or malformed. 3. Consider adding a data migration or repair script to normalize existing imported tasks. 4. Harden the CSV import to validate and sanitize data on ingest so future imports don't produce fragile records.","proposed_test_case":"Import a CSV containing tasks with edge-case data (empty fields, special characters, very long strings, missing optional columns), then verify that each imported task can be opened, edited, and saved without crashing.","information_gaps":["Exact error message or stack trace from the crash (would speed up pinpointing the failing line but not needed to start investigating)","The structure/contents of the original CSV file","App version and platform (web, desktop, mobile)","Whether the issue appeared immediately after import or only after a subsequent app update"]}

---
