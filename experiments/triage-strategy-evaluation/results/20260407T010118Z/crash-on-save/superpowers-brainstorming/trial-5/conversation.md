# Conversation: crash-on-save x superpowers-brainstorming

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — sorry you're losing work, that's definitely frustrating.

To help us track this down, could you tell us what you were saving when the crash happens?

- **A)** Creating or editing a task
- **B)** Updating project settings or preferences
- **C)** Exporting or saving a file/report
- **D)** Something else (please describe briefly)

Also, does it happen *every* time you save, or only sometimes?

---

### reporter

It's A, I'm editing tasks. And yeah it happens every single time I hit Save. I've tried multiple times and it just crashes. The app is basically unusable right now.

---

### triage-agent

Thanks for confirming — every-time-on-save is actually helpful for narrowing this down.

Here's my working theory: something about the specific data in the task might be triggering the crash (e.g., a special character, a long description, or a particular field value). To test that:

- **A)** It crashes when I edit *any* task, even a brand-new one with a simple title like "test"
- **B)** It only crashes on certain tasks — others save fine
- **C)** I'm not sure, I've only tried it on one task

This will help us figure out whether the problem is in the save logic itself or in how specific data is being handled.

---

### reporter

I guess B? I mean, I have a lot of tasks and the whole thing crashes when I save. I haven't tried making a brand new task and saving just that. All I know is it worked fine before and now it doesn't.

---

### triage-agent

That detail — 'it worked fine before and now it doesn't' — is a really useful clue. It sounds like something changed recently that broke saving.

Do you recall what changed around the time it stopped working?

- **A)** The app was updated (or updated itself) recently
- **B)** I added or imported a bunch of new tasks around that time
- **C)** I changed something in my account or project settings
- **D)** Nothing I can think of — it just started happening one day

This will help us figure out whether we're looking at a bug introduced in a release or something data-related in your account.

---

### reporter

B, I imported a CSV file with a bunch of tasks. But I don't see why that would matter, the import worked fine. The tasks are all there, I can see them. It just crashes when I try to save.

---

### triage-agent

[RESOLVED] {"title":"App crashes on every task save after CSV import","problem":"After importing tasks via CSV, the application crashes every time the user attempts to save an edited task. The import itself completes successfully and tasks are visible, but the app becomes effectively unusable because no task edits can be saved.","root_cause_hypothesis":"The CSV import likely introduces task data that bypasses input validation (e.g., special characters, unexpected encoding, overly long fields, or null/empty values in required fields). The save logic then encounters this malformed data during serialization or validation and crashes instead of handling the error gracefully.","reproduction_steps":["Import a CSV file containing multiple tasks","Verify the imported tasks appear in the task list","Open any existing task for editing","Make a change and click Save","Observe the app crash"],"environment":"Not specified — reproduce on any supported platform. Reporter did not mention platform or version.","severity":"critical","impact":"Any user who imports tasks via CSV may be completely unable to save edits afterward, resulting in data loss and an unusable application.","recommended_fix":"1. Compare the data model of CSV-imported tasks vs natively-created tasks — look for fields with unexpected types, encoding, or null values. 2. Add a try-catch or error boundary around the save path to prevent hard crashes. 3. Add validation/sanitization to the CSV import pipeline so imported data conforms to the same constraints as manually-created tasks. 4. Test whether saving a brand-new (non-imported) task also crashes — if so, the import may have corrupted shared state (e.g., a project-level index).","proposed_test_case":"Import a CSV with varied data (special characters, unicode, empty fields, very long strings) and verify that each imported task can be opened, edited, and saved without crashing.","information_gaps":["Exact error message or crash log","Whether brand-new (non-imported) tasks also crash on save, or only imported ones","App version and platform (web, desktop, mobile)","Contents/structure of the imported CSV file"]}

---
