# Conversation: crash-on-save x omc-deep-interview

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — sorry you're losing work, that's definitely not acceptable. To help us track this down quickly: when the app crashes, what exactly do you see? For example, does the window freeze, close suddenly, or do you get an error message or dialog? And which save action triggers it — saving a new task, editing an existing one, or any save operation in the app?

---

### reporter

When I hit Save in the toolbar, I see a quick flash of some error dialog — it says something about 'encoding' I think, but it disappears so fast I can barely read it — and then the whole app just closes. No freeze or anything, it just shuts down instantly. It happens when I'm saving my main task list, which has around 200 tasks in it. I think it started after I imported a bunch of tasks from a CSV file.

---

### triage-agent

That's really helpful — the encoding error combined with the CSV import is a strong clue. Can you tell me a bit about the data in that CSV file? Specifically, did it contain any non-English characters (accented letters, CJK characters, emoji, etc.), or any unusual symbols? And do you happen to know how the CSV was created — was it exported from another app, or hand-edited in something like Excel? If you still have the CSV, could you also check whether the issue goes away if you create a brand-new task list and try saving that (just to confirm it's tied to the imported data)?

---

### reporter

The CSV had some fancy punctuation in it — like curly quotes and em-dashes in the task names. It was exported from a spreadsheet app, I don't remember if it was Excel or Google Sheets. No emoji or foreign language stuff though, just those typographic characters. And yeah, I actually tested this — if I create a small list with just a few tasks, it saves fine. Even if I import some of the CSV data into a small list (under 50 tasks or so), it seems to work. It only crashes with my main list that has around 200 tasks. And if I delete the imported tasks from my big list, the crash goes away too. So it's definitely related to the imported data, but it also seems like the size of the list matters.

---

### triage-agent

[RESOLVED] {"title":"App crashes on save with encoding error when saving large lists containing CSV-imported typographic characters","problem":"The application crashes immediately (window closes) when saving a task list that contains ~200+ tasks imported from a CSV file with typographic characters (curly quotes, em-dashes). A brief error dialog mentioning 'encoding' flashes before the crash. Smaller lists (<50 tasks) with the same imported data save successfully, and removing the imported tasks from the large list eliminates the crash.","root_cause_hypothesis":"The save/serialization path likely fails when encoding typographic characters (smart quotes U+2018/U+2019/U+201C/U+201D, em-dash U+2014) that are outside the ASCII range. The size dependency suggests either: (1) a buffer overflow where the multi-byte UTF-8 encoding of these characters causes a miscalculated buffer size to overflow only at scale, or (2) an error-handling path that accumulates encoding errors and only crashes past a threshold. The most likely root cause is that the save routine assumes ASCII or single-byte encoding for buffer allocation, and the multi-byte typographic characters cause a write-past-end that becomes fatal at ~200 tasks.","reproduction_steps":["Create or obtain a CSV file with typographic characters in task names (curly/smart quotes, em-dashes) — export from a spreadsheet app to get these naturally","Import the CSV into TaskFlow","Ensure the task list contains approximately 200 tasks (include the imported ones with typographic characters)","Click Save in the toolbar","Observe: brief encoding error dialog flash, then app crashes (window closes)"],"environment":"Desktop app (OS not specified). CSV was exported from a spreadsheet application (Excel or Google Sheets). No specific version reported.","severity":"high","impact":"Users who import CSV data with typographic characters into large task lists lose all unsaved work on every save attempt. Workaround exists: remove imported tasks or keep lists under ~50 tasks, but this is not practical for the reporter's 200-task workflow.","recommended_fix":"Investigate the save/serialization code path for encoding handling. Likely candidates: (1) Check buffer size calculation in the save routine — ensure it accounts for multi-byte UTF-8 characters, not just character count. (2) Check if the serializer (JSON, XML, or custom format) properly handles Unicode code points above U+007F. (3) Add proper error handling around the encoding step so failures surface a readable error instead of crashing. (4) Consider normalizing typographic characters on CSV import (e.g., converting smart quotes to straight quotes) as a secondary defense.","proposed_test_case":"Create a task list with 200+ tasks where at least 20 tasks contain typographic characters (curly quotes, em-dashes, ellipsis characters). Verify that save completes without error and that the data round-trips correctly (reload and confirm typographic characters are preserved). Also test boundary: 50, 100, 150, 200 tasks with the same special characters to identify the exact failure threshold.","information_gaps":["Exact OS and app version not provided","Whether the CSV was UTF-8 or another encoding (e.g., Windows-1252) is unknown","Exact error message in the dialog (it disappears too quickly to read fully)","Whether the crash produces a crash log or stack trace on disk"],"clarity_scores":{"symptom":0.85,"cause":0.75,"reproduction":0.80,"impact":0.80,"overall_ambiguity":0.20}}

---

