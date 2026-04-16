# Conversation: crash-on-save x omc-deep-interview

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I can see how frustrating losing work would be. To help us track this down, could you walk me through exactly what happens when the crash occurs? Specifically: what are you saving (a new task, edits to an existing one?), and what do you see when it crashes — does the app freeze, close entirely, or show an error message?

---

### reporter

It just closes. Like, the whole app just shuts down. I'm editing my task list and I hit Save in the toolbar and it's gone. I think there's some kind of error that flashes for a split second but I can't read it. I have like 200 tasks in there and I just need to be able to save my work.

---

### triage-agent

That sounds like a hard crash — thanks for the detail about the error flashing briefly. A couple of things that would really help us narrow this down: did this start happening recently, or has Save never worked for you? And if it did start recently, can you think of anything that changed around that time — like an app update, or your task list growing past a certain size? Also, does it crash every single time you hit Save, or does it sometimes work?

---

### reporter

It started maybe a week or two ago, I don't remember exactly. It crashes every single time now. I did import a bunch of tasks from a CSV file around that time but I don't see how that would matter. Look, I just need to save my work — is there a fix coming or not?

---

### triage-agent

I hear you, and we're working toward getting this fixed — your data is important. The CSV import you mentioned actually could be very relevant, since the timing lines up. Could you try something quick for me? If you create a brand-new task list with just a couple of tasks (not imported), does Save work there? That would tell us whether the crash is tied to the imported data specifically. Also, do you happen to still have that CSV file? If the crash is related to something in the imported data, having the file would let us reproduce the issue on our end much faster.

---

### reporter

I don't have time to be creating test lists and running experiments for you. But yeah, I already know that smaller lists work fine — I have another list with like 30 tasks and it saves no problem. The CSV file is on my computer somewhere, I'd have to dig for it. Can you just fix the save function so it doesn't crash? It worked fine before I added all those tasks.

---

### triage-agent

Totally understood — no experiments needed. Just two quick details so a developer can reproduce this on their end: what platform are you on (Windows, Mac, or Linux), and what version of TaskFlow are you running? You can find the version under Help → About. Also — do you remember roughly what was in the CSV? For instance, was it just task names, or did it include things like dates, descriptions, or special characters? That'll help us figure out whether the crash is about the number of tasks or something specific in the imported data.

---

### reporter

I'm on Mac. Version is 2.3.1 I think. The CSV was just task names, nothing fancy. I exported it from another app. I don't know about special characters or whatever, it's just a list of tasks. Can someone actually look at the save code? It shouldn't be crashing no matter what's in my list.

---

### triage-agent

[RESOLVED] {"title":"App crashes on Save with large task list imported from CSV (~200 tasks, Mac, v2.3.1)","problem":"TaskFlow hard-crashes (immediately closes) every time the user clicks Save in the toolbar. The crash began after the user imported approximately 200 tasks from a CSV file. A separate list with ~30 tasks saves without issue. A brief error message flashes on screen before the app closes but is unreadable.","root_cause_hypothesis":"The save operation likely fails when serializing or validating a large number of tasks, possibly due to an unhandled exception in the save path that scales with list size. The CSV import may have introduced data that the save function doesn't handle gracefully — either the volume itself triggers a timeout/memory issue, or the imported data contains subtle formatting (e.g., encoding, line breaks, or characters from the source app's export) that corrupts the save payload. The fact that it started after the import and affects only that list points to a data-dependent crash rather than a general save regression.","reproduction_steps":["Install TaskFlow 2.3.1 on macOS","Create a CSV file with ~200 task names (plain text, one per row — simulate an export from another task app)","Import the CSV into a new task list","Click Save in the toolbar","Observe: app crashes immediately with a brief error flash"],"environment":"macOS, TaskFlow v2.3.1 (reported as approximate)","severity":"critical","impact":"User cannot save their primary task list at all (100% crash rate), resulting in data loss on every attempt. Any user who imports a large CSV is likely to hit this. Smaller lists are unaffected.","recommended_fix":"1. Add crash logging / exception handling around the save path so the fleeting error is captured to a log file. 2. Reproduce with a ~200-row imported CSV and inspect the stack trace. 3. Investigate whether the crash is size-dependent (try 50, 100, 150, 200 rows) or data-dependent (try 200 rows of trivial 'Task N' names). 4. Check for unbounded memory allocation, synchronous blocking on large payloads, or malformed data from CSV import (encoding issues, unexpected delimiters). 5. Regardless of root cause, the save function should never hard-crash — add graceful error handling and user-visible error reporting.","proposed_test_case":"Import a CSV with 200+ task names into a new list, then invoke Save. Verify the app does not crash, the file is persisted correctly, and all 200 tasks are present when the list is reopened. Additionally, test with CSVs containing edge-case data (unicode, empty rows, very long task names) to cover data-dependent failures.","information_gaps":["Exact error message (flashes too fast for user to read — crash logs would reveal this)","Original CSV file (user may be able to locate it later for exact reproduction)","Exact TaskFlow version (user said '2.3.1 I think')","Whether the source app's CSV export uses any unusual encoding or formatting"]}

---
