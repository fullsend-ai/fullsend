# Conversation: crash-on-save x superpowers-brainstorming

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — sorry you're losing work, that's definitely not acceptable.

To help us track this down, could you tell us what you were saving when the crash happened?

- **A)** Saving a new task
- **B)** Editing and saving an existing task
- **C)** Saving project or workspace settings
- **D)** Something else (please describe)

Also, does the crash happen *every* time you save, or only sometimes?

---

### reporter

It's not saving a single task, it's saving my whole task list. I have a big list with like 200 tasks and every time I hit Save it just crashes. Every. Single. Time. I need this fixed, I have a deadline coming up and I can't save my work.

---

### triage-agent

That's really helpful, thank you — and I understand the urgency with your deadline.

One more question: did saving this task list used to work fine, and it started crashing recently? Or has it always crashed with a list this large?

- **A)** It used to work, but started crashing recently (maybe after an update)
- **B)** It's always crashed — this is the first time I've had a list this large
- **C)** Not sure / I'm a new user

This will help us figure out whether it's a regression or a scaling issue.

---

### reporter

It used to work fine. It started crashing after I imported a bunch of tasks from a CSV file. I don't know if there was an update or whatever, but it was working before that import.

---

### triage-agent

Thanks — the CSV import is a strong lead. I have a hypothesis and want to check it:

I suspect the imported data may contain something that's tripping up the save process (for example, special characters, very long task names, or unusual formatting from the CSV). To help us narrow it down:

Could you try **deleting just a few of the imported tasks** and then saving again?

- **A)** It still crashes even after removing some imported tasks
- **B)** It saves successfully once I remove some
- **C)** I can't try this right now (that's okay, we can work with what we have)

This will tell us whether it's a data issue or a volume issue, which points the fix in very different directions.

---

### reporter

I already tried removing all the imported tasks and yeah it saves fine after that. But I NEED those tasks, that's the whole point. I imported them for a reason. Why can't the app handle a simple CSV import? The data looks fine to me.

---

### triage-agent

[RESOLVED] {"title":"Save crashes when task list contains CSV-imported tasks","problem":"The application crashes every time the user saves their task list after importing ~200 tasks from a CSV file. Removing all imported tasks restores normal save functionality. The user needs the imported tasks and cannot work around the issue.","root_cause_hypothesis":"The CSV import likely produces task records with data that the save/serialization process cannot handle — for example, special characters, encoding mismatches, fields exceeding expected lengths, or missing/malformed fields that manually-created tasks always have. The import may succeed in loading the data into memory but produce objects that fail validation or serialization on save.","reproduction_steps":["Create or use a task list with a small number of manually-created tasks","Verify that saving works normally","Import a CSV file containing ~200 tasks","Attempt to save the task list","Observe the crash"],"environment":"Not specified — issue appears to be data-dependent rather than environment-dependent","severity":"high","impact":"Users who import tasks via CSV are unable to save their work at all, leading to data loss. The reporter has an upcoming deadline and is completely blocked.","recommended_fix":"Investigate the difference between CSV-imported task objects and manually-created ones at save time. Check the serialization/persistence layer for: (1) unescaped special characters, (2) encoding issues (e.g., non-UTF8 from CSV), (3) missing required fields that the CSV importer doesn't populate, (4) field length or type mismatches. Add defensive validation in the CSV importer to normalize data to match the schema expected by the save path. Consider adding a try/catch in the save path that identifies the offending record rather than crashing.","proposed_test_case":"Import a CSV containing tasks with varied edge-case data (special characters, long names, empty fields, unicode) and verify that save completes successfully. Also test with 200+ imported tasks to rule out a pure volume/memory issue.","information_gaps":["No error message or stack trace from the crash","Exact contents/format of the CSV file used","Whether removing only *some* imported tasks (rather than all) would allow saving — could help distinguish a single bad record from a volume issue","Platform and application version"]}

---
