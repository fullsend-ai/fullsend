# Conversation: crash-on-save x socratic-refinement

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — losing work to crashes is definitely frustrating, and we want to get this sorted out for you.

To help us dig in: can you walk us through what you were working on when the crash happened? For example, what kind of task or document were you saving, roughly how much content was in it, and had you been working in that session for a while or was it a fresh start? Any detail about what you were doing right before hitting save would be really helpful.

---

### reporter

Sure thing. I have a task list with about 200 tasks in it. The crash happens when I click the Save button in the toolbar. I recently imported a bunch of tasks from a CSV file — that's when the problems started. Before the import, saving worked fine. I've noticed that if I create a smaller list with like 30-40 tasks, it saves okay even with some of the imported data in there. But with the full 200-task list it just dies every time I hit Save.

---

### triage-agent

[RESOLVED] {"title":"App crashes on save with large task lists created via CSV import","problem":"Clicking the Save button in the toolbar causes the app to crash when the task list contains approximately 200 tasks that were imported from a CSV file. Saving works correctly with smaller lists (30-40 tasks) and worked correctly before the CSV import feature was used.","root_cause_hypothesis":"The save operation likely has a performance or memory issue that manifests at scale. The CSV import may introduce data volume or payload size that exceeds a buffer, timeout, or memory limit in the save path. A secondary hypothesis is that the CSV import produces malformed or oversized field data (e.g., unclosed strings, unexpected characters) that corrupts serialization at scale but not in small batches.","reproduction_steps":["Create or open a task list in TaskFlow","Import approximately 200 tasks from a CSV file","Click the Save button in the toolbar","Observe the app crash"],"environment":"Not specified — reporter did not mention OS, browser, or app version. Reproduction should be attempted on standard environments first.","severity":"high","impact":"Users who import large datasets via CSV lose all unsaved work when the app crashes on save. This is a data-loss scenario that directly blocks a core workflow (CSV import + save).","recommended_fix":"Investigate the save handler for issues at scale: check for synchronous serialization of the full task list that could blow a stack or memory limit, missing pagination or chunking in the save payload, or timeout issues on the backend. Also inspect the CSV import path to verify imported records are well-formed — check for excessively long fields, special characters, or encoding issues that could break serialization. Add a size-based stress test for the save path.","proposed_test_case":"Import a CSV with 200+ tasks of representative data and verify that clicking Save completes without crashing. Additionally, test with intentionally malformed CSV data (special characters, very long fields, empty fields) at the 200-task scale to ensure the save path is resilient.","information_gaps":["Exact error message or crash log not provided","Unknown whether the crash is client-side (UI freeze/JS error) or server-side (HTTP 500, timeout)","App version, OS, and browser not specified","CSV file contents and structure not examined — unclear if specific rows contain problematic data","Exact threshold between 30-40 (works) and 200 (crashes) not pinpointed"]}

---
