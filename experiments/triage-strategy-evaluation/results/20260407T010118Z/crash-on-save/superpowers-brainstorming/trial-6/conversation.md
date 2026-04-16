# Conversation: crash-on-save x superpowers-brainstorming

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — sorry you're losing work, that's definitely frustrating.

To help us narrow this down: what are you saving when the crash happens?

- A) Creating or editing a **task**
- B) Updating **project settings** or preferences
- C) Saving changes to a **board or view**
- D) Something else (please describe)

Also, does the crash happen *every* time you save, or only sometimes?

---

### reporter

It's A, I'm editing tasks. And yes it crashes every single time I hit Save. I've been dealing with this for days now and I just need it fixed.

---

### triage-agent

Got it — task editing, every time. That reproducibility will help us track it down quickly.

Does the crash happen when you edit **any** field on a task, or is it specific to certain fields?

- A) Any field at all (title, description, due date, etc.)
- B) Only when changing the **description** or adding long text
- C) Only when changing **dates**, assignees, or other structured fields
- D) I'm not sure — I've only tried editing one type of field

If D, which field have you been editing?

---

### reporter

I don't know, I'm not really changing specific fields most of the time. I just have my task list and when I click Save in the toolbar the whole thing crashes. It was working fine before I imported a bunch of tasks from a CSV file last week, if that matters.

---

### triage-agent

That CSV import detail is really helpful — it's likely the key to this.

Here's what I'm thinking: the import may have introduced task data that the save logic doesn't handle well (e.g., special characters, missing required fields, or unexpected formatting). When you hit Save, it tries to process that data and crashes.

To confirm: does the crash also happen if you create a **brand new** task from scratch (not one that was imported) and try to save it?

- A) Yes, even brand-new tasks crash on save
- B) No, new tasks save fine — it's only the imported ones
- C) I haven't tried that — I can test it now

This will tell us whether we're looking at a data problem from the import or a broader code issue.

---

### reporter

I mean, I haven't specifically tried creating one new task and saving just that. But I did try deleting all the imported tasks at one point and it saved fine after that. So yeah, it's something about those imported tasks. Can you just fix the import so it doesn't break everything?

---

### triage-agent

[RESOLVED] {"title":"App crashes on save when workspace contains CSV-imported tasks","problem":"Saving any changes in the task view causes the app to crash when CSV-imported tasks are present. The crash is 100% reproducible and occurs every time the user clicks Save in the toolbar. The issue began after the user imported tasks from a CSV file. Removing the imported tasks eliminates the crash, confirming the imported data is the trigger.","root_cause_hypothesis":"The CSV import accepts data that the save/serialization logic cannot handle. Likely candidates: special characters or encoding issues in text fields, missing required fields that the import doesn't validate but the save path assumes are present, or data type mismatches (e.g., a date field containing non-date text). The save operation processes all tasks in the view, so even if the user is editing an unrelated task, the crash occurs when the imported tasks are serialized.","reproduction_steps":["Start with a clean workspace with at least one manually-created task","Verify that saving works normally","Import tasks from a CSV file (try with various edge cases: special characters, empty required fields, long text, non-UTF8 encoding)","Click Save in the toolbar","Observe crash"],"environment":"Not specified — appears to be a data-handling issue likely independent of environment","severity":"high","impact":"Any user who imports tasks via CSV risks making their entire workspace unsaveable, causing repeated data loss. The user has been blocked for days.","recommended_fix":"1. Add input validation/sanitization to the CSV import pipeline — reject or clean data that doesn't conform to the task schema. 2. Make the save logic defensive against malformed task data (catch and isolate bad records rather than crashing the entire save). 3. Add a CSV import preview/validation step so users see problems before committing the import.","proposed_test_case":"Import a CSV containing edge-case data (empty required fields, special characters, extremely long strings, malformed dates) and verify that (a) the import either rejects or sanitizes the data, and (b) the save operation completes without crashing even if malformed data is present.","information_gaps":["Exact CSV file contents or structure that triggered the issue","Specific error message or stack trace from the crash","Which platform/version of the app the user is running"]}

---
