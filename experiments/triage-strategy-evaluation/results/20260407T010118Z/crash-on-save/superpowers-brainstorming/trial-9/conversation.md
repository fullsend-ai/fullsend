# Conversation: crash-on-save x superpowers-brainstorming

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — sorry you're losing work, that's really frustrating.

To help us track this down, could you tell us what you're saving when the crash happens?

- **A.** Creating or editing a task
- **B.** Updating project settings or preferences
- **C.** Exporting or bulk-saving data
- **D.** Something else (please describe)

Also, does it happen *every* time you save, or only sometimes?

---

### reporter

It's A, I'm editing tasks. And yeah it happens every time I hit Save. I've been dealing with this for days now and it's getting old. I just need to save my work without the app blowing up on me.

---

### triage-agent

Thanks for confirming — that helps narrow it down.

Does the crash happen when you save **any** task, or only certain ones? For example:

- **A.** Every task, no matter what I change
- **B.** Only tasks where I edit a specific field (like description, due date, assignee, etc.)
- **C.** Only tasks that already have a lot of content or attachments
- **D.** Not sure — I haven't tried comparing different tasks

This will help us figure out whether the issue is in the general save flow or triggered by specific data.

---

### reporter

I don't know, I haven't really tested individual tasks. I just know that when I hit Save it crashes. I have like 200 tasks in there so I'm not going to go through them one by one. It was working fine before I imported a bunch of stuff from a CSV file, if that matters.

---

### triage-agent

That CSV import detail is really helpful — I think that's probably the key.

Here's my working theory: the imported data likely contains something that the save flow doesn't handle well (e.g., special characters, very long field values, or a format mismatch). Can you help me confirm by trying one quick test?

- **Create a brand-new task from scratch** (don't edit an imported one) with a simple title like "test task" and hit Save.

Does that also crash, or does it only crash when saving tasks that came from the CSV import?

This one test will tell us whether the problem is in the save logic itself or in the imported data, and it'll save you from having to check all 200 tasks.

---

### reporter

Look, when I hit Save it saves everything, not just one task. So yeah it still crashes because all the imported stuff is still in there. I actually already tried removing the imported tasks at one point and it saved fine after that. So yeah it's definitely something about that CSV data. Can you just fix the import or the save so it stops crashing? I need those tasks.

---

### triage-agent

[RESOLVED] {"title":"Bulk save crashes when workspace contains CSV-imported tasks","problem":"The application crashes every time the user saves after importing ~200 tasks from a CSV file. The save operation is a bulk save (all tasks at once), and the crash is 100% reproducible. Removing the imported tasks restores normal save functionality. The user's work is blocked because they need the imported tasks.","root_cause_hypothesis":"The CSV import accepts data that the bulk save/serialization path cannot handle. Likely candidates: special characters (unescaped quotes, newlines, unicode), field values exceeding length limits, null or missing required fields, or data type mismatches (e.g., a string in a date field) that pass import validation but fail during save serialization.","reproduction_steps":["Start with a clean TaskFlow workspace where saving works normally","Import a CSV file containing a non-trivial number of tasks (~200)","Edit any task (or make no edits at all)","Click Save — the app crashes","Remove all imported tasks and try saving again — save succeeds"],"environment":"Not specified; issue appears data-dependent rather than environment-dependent","severity":"high","impact":"User is completely blocked from saving any work as long as imported tasks exist in the workspace. Data loss occurs on each crash. Any user who imports tasks via CSV may hit this.","recommended_fix":"1. Examine the bulk save serialization path for unhandled data types, missing null checks, or field length/format assumptions. 2. Compare the schema expectations of the save path against what the CSV importer actually produces — there is likely a validation gap where import accepts data that save cannot serialize. 3. Add defensive handling in the save path so malformed records fail gracefully (skip or flag) rather than crashing the entire operation. 4. Consider adding import-time validation that matches save-time constraints.","proposed_test_case":"Import a CSV with edge-case values (special characters, very long strings, empty required fields, type mismatches) and verify that both import and subsequent bulk save complete without crashing. Additionally, test that a single malformed record does not prevent the rest from saving.","information_gaps":["Exact error message or stack trace from the crash","Contents/schema of the CSV file used for import","Application version and platform (web, desktop, mobile)"]}

---
