# Conversation: crash-on-save x omc-deep-interview

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — sorry you're dealing with it. To help us track this down: when the app crashes, what exactly do you see? Does it freeze, close entirely, or show an error message? And could you let us know what you're saving at the time — for example, a specific type of task or project, roughly how large it is, and which version of TaskFlow you're running?

---

### reporter

When I hit Save, there's a brief flash of some kind of dialog — I think it mentions something about 'encoding' — and then the app just closes completely. No freeze, it just shuts down. I'm on TaskFlow v2.3.1. I have about 200 tasks in my list. The thing is, it started happening after I imported a bunch of tasks from a CSV file. Before that import, saving worked fine.

---

### triage-agent

That's really helpful — the fact that it started right after the CSV import is a strong clue, especially with that 'encoding' flash. Could you tell us a bit about that CSV file? Specifically: where did it come from (another app, a spreadsheet, hand-edited), and did any of the tasks in it contain special characters — things like accented letters, emoji, or non-English text? Also, if you still have the CSV, would you be able to try importing just a small subset of it (say 10–20 rows) into a fresh TaskFlow instance to see if the crash still happens?

---

### reporter

The CSV came from a spreadsheet a coworker shared with me — I think they exported it from Excel. Looking at it now, yeah, there are some fancy characters in there like curly quotes and em-dashes in the task names. No emoji or foreign language stuff, just those typographic characters. I just tried importing about 15 rows from the CSV into a new empty task list and it saved fine. So it seems like it only crashes when the list is big — my main list has around 200 tasks.

---

### triage-agent

Good find on narrowing it down! One thing I want to double-check before we jump to a conclusion about list size: when you tested those 15 rows, did any of them happen to contain the curly quotes or em-dashes? It's possible the crash isn't about how many tasks you have, but about a specific character in a specific row. If you're able to try again with a small batch that definitely includes some of those typographic characters, that would help us pin down whether it's a size issue or a character issue — and if it's a character issue, it might even point us to the exact row causing the problem.

---

### reporter

Good point — yes, the 15 rows I tested did include some of the curly quotes and em-dashes, and it saved just fine with those. So I don't think it's one specific bad character or row. It really does seem like it's related to the size of the list. My full list with ~200 tasks crashes every time I hit Save, but smaller lists save fine even with the same special characters in them.

---

### triage-agent

[RESOLVED] {"title":"App crashes on save with encoding error when task list exceeds ~200 items containing typographic characters from CSV import","problem":"TaskFlow v2.3.1 crashes immediately (app closes) when the user saves a task list containing approximately 200 tasks that were imported from an Excel-exported CSV file. A brief dialog flash mentioning 'encoding' appears before the crash. The issue does not occur with smaller subsets (~15 tasks) of the same data, including rows containing the same typographic characters.","root_cause_hypothesis":"The save/serialization path likely has an encoding conversion issue (e.g., converting Windows-1252 typographic characters like curly quotes and em-dashes to UTF-8) that manifests only when the data volume exceeds a threshold — possibly a fixed-size buffer overflow in the encoding conversion routine, or an O(n²) operation in character escaping that causes a timeout or memory issue at scale.","reproduction_steps":["Install TaskFlow v2.3.1","Create or obtain a CSV file exported from Excel containing ~200 task rows, with some task names containing typographic characters (curly quotes, em-dashes)","Import the CSV into TaskFlow","Click Save","Observe: brief encoding-related dialog flash, then app closes entirely"],"environment":"TaskFlow v2.3.1. CSV source: Excel export. OS not confirmed but likely desktop (Windows or macOS given Excel workflow).","severity":"high","impact":"Users with large imported task lists cannot save at all, resulting in complete data loss of any changes. Any user who imports a sufficiently large CSV from Excel with typographic characters will hit this. No known workaround reported.","recommended_fix":"Investigate the save/serialization code path for encoding handling. Check for: (1) fixed-size buffers in character encoding conversion, (2) improper handling of Windows-1252 characters (curly quotes U+2018/U+2019, em-dashes U+2014) during serialization, (3) any size-dependent behavior in the encoding layer such as chunked processing or buffer allocation. Add a try-catch around the encoding step to surface the actual error instead of crashing. Consider normalizing all imported text to UTF-8 at import time rather than at save time.","proposed_test_case":"Create a test that programmatically generates a task list of 250+ items where ~20% contain curly quotes (U+2018, U+2019) and em-dashes (U+2014), then invokes the save routine and asserts it completes without error and the saved file round-trips correctly with characters preserved.","information_gaps":["Exact size threshold where the crash begins (somewhere between 15 and 200 tasks)","Operating system of the reporter","Whether the crash also occurs on auto-save or only manual save","Full text of the encoding error dialog (only partially glimpsed)"],"clarity_scores":{"symptom":0.85,"cause":0.75,"reproduction":0.80,"impact":0.70,"overall_ambiguity":0.21}}

---

