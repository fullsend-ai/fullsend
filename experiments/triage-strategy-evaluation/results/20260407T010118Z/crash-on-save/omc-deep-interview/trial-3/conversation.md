# Conversation: crash-on-save x omc-deep-interview

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Sorry to hear about the crashes — that sounds really disruptive. To help us track this down, could you tell us a bit more about what you see when the crash happens? Specifically: does the app close entirely, freeze, or show an error message? And what are you working on when you hit save — for example, are you saving a new task, editing an existing one, or saving changes to something like project settings?

---

### reporter

The app just closes. Like, completely gone. I think there's some kind of error box that flashes for a split second but it disappears too fast to read. I'm just editing my task list and hitting Save in the toolbar. It's not complicated — I just want to save my work. This has been happening for like a week now and I'm tired of it.

---

### triage-agent

Thanks for those details — that vanishing error box is a strong clue we'll want to chase down.

Does this happen every single time you hit Save, or only sometimes? If it's intermittent, have you noticed any pattern — for example, does it seem related to how many tasks you have, how long the app has been open, or anything specific about the tasks you're editing (like long descriptions, attachments, or special characters)?

Also, what platform are you on (Windows, Mac, Linux) and which version of TaskFlow are you running? You can usually find the version under Help → About.

---

### reporter

It happens every time I hit Save. I'm on a Mac. I don't know the exact version off the top of my head, maybe 2.3 something? I have a lot of tasks in there, like a couple hundred. I don't know if that matters. Look, I just need this to work — I've been copy-pasting my stuff into a text file as a backup because I can't trust the app anymore.

---

### triage-agent

That's really helpful — the fact that it happens every single time actually makes it easier to track down.

You mentioned this started about a week ago. Can you think of anything that changed around that time? For example, did TaskFlow update itself, did you update macOS, or did you do anything different with your tasks — like import tasks from somewhere, add a large attachment, or paste content from another app? Even something that seems unrelated could be the trigger.

Also, if you're up for a quick test: could you try creating a brand-new task list with just one or two simple tasks and see if saving that also crashes? That would help us figure out whether the problem is tied to your specific data or to the save function itself.

---

### reporter

Yeah actually now that you mention it, I did import a bunch of tasks from a CSV file around that time. I had a spreadsheet of tasks from another tool and brought them in. I doubt that's the problem though, the import worked fine. And no, I'm not going to start making test lists — I have actual work to do. But I will say that I had a smaller project with like 30 tasks and that one saves fine, so maybe it is something about this specific list. I just want to save my stuff without the app disappearing on me.

---

### triage-agent

That CSV import is actually a really promising lead — especially since your smaller project without the imported tasks saves just fine. A successful import doesn't always mean the data is clean on the other side.

Could you tell us a bit about that CSV? Roughly how many tasks were in it, and did any of them have long descriptions, unusual characters (like emojis, accented letters, or symbols), or fields that were left blank? Also, do you still have the original CSV file? If so, a developer on our team could try importing it on their end to reproduce the crash directly — that would be the fastest path to a fix.

No need to create test lists or do anything extra on your end. We just want to understand the data that came in.

---

### reporter

I don't remember exactly how many, maybe like 150 tasks or something? It was a big list from our old project management tool. I don't know about unusual characters — they were just normal task names. I mean, some of them might have had dashes or quotes or whatever, nothing weird. I think I still have the CSV somewhere on my laptop. But honestly can you just fix the save button? I don't care about the import, that part worked. The saving is what's broken.

---

### triage-agent

Totally understand — you shouldn't have to troubleshoot this yourself, and the CSV lead gives us a strong direction already.

One last thing that would really speed up the fix: since the app is crashing on your Mac, macOS will have automatically saved a crash report. If you open **Finder → Go → Go to Folder** and paste `~/Library/Logs/DiagnosticReports`, you should see files with "TaskFlow" in the name (probably from the last week). Could you grab the most recent one and paste its contents here, or attach the file? That report will contain the exact error message — including the one that flashes too fast to read — and will tell our developers precisely where things are going wrong.

No testing needed on your end, just that one file.

---

### reporter

Ugh, I don't even know what that is. I'm not a developer, I don't go digging around in system folders. Look, I told you — the app crashes when I hit Save. I imported some tasks, saving doesn't work anymore. Can someone just fix it? If I remove the tasks I imported it seems to work but I need those tasks, that's the whole point. I shouldn't have to delete my work to use the app.

---

### triage-agent

[RESOLVED] {"title":"App crashes on save when task list contains CSV-imported tasks (~150 items)","problem":"TaskFlow crashes immediately (window closes entirely) every time the user clicks Save in the toolbar when working with a task list that contains ~150 tasks imported from a CSV file. A brief error dialog flashes but disappears too quickly to read. The crash is 100% reproducible on the affected project. A separate, smaller project (~30 tasks, no CSV import) saves without issue. Removing the imported tasks restores save functionality.","root_cause_hypothesis":"The CSV import likely introduced data that the save/serialization path cannot handle. Most probable causes: (1) a field contains characters that break serialization — unescaped quotes, null bytes, or encoding mismatches (e.g., Latin-1 data in a UTF-8 pipeline), (2) a field exceeds an assumed length limit causing a buffer or memory error during write, or (3) the import left orphaned or malformed relational references (e.g., missing project IDs, circular dependencies) that the save routine chokes on. The fact that the import 'succeeded' while save fails suggests validation happens at save time, not import time.","reproduction_steps":["Install TaskFlow ~2.3.x on macOS","Create a new project/task list","Import a large CSV (~150 tasks) exported from another project management tool — include varied punctuation (dashes, quotes, etc.)","Edit any task in the list","Click Save in the toolbar","Observe: app window closes abruptly with a momentary error flash"],"environment":"macOS (version unknown), TaskFlow ~2.3.x (exact build unknown), task list with ~200 total tasks including ~150 imported from CSV","severity":"high","impact":"Complete loss of save functionality for any project containing CSV-imported tasks. User is forced to maintain a manual text-file backup. The workaround (deleting imported tasks) defeats the purpose of the feature. Likely affects any user who imports a large or similarly-structured CSV.","recommended_fix":"1. Check the macOS crash report for TaskFlow in ~/Library/Logs/DiagnosticReports to identify the exact exception and stack trace. 2. Inspect the save/serialization code path for unhandled exceptions — add proper error handling so the crash dialog persists and the app doesn't terminate. 3. Audit the CSV import pipeline for data sanitization gaps: check for encoding issues, unescaped special characters, excessively long fields, and malformed relational data. 4. Add import-time validation that catches data the save path cannot handle. 5. Test with a variety of CSVs including edge-case characters and large row counts.","proposed_test_case":"Import a CSV containing 150+ tasks with mixed punctuation (quotes, dashes, ampersands, accented characters, empty fields) into a new project. Verify that saving succeeds without crashing. Additionally, create a unit test for the serialization path that exercises all supported field types with boundary-length and special-character inputs.","information_gaps":["Exact TaskFlow version and macOS version","Contents of the macOS crash report (would pinpoint the exact exception)","The original CSV file (would enable direct reproduction)","Whether specific imported tasks trigger the crash or if it's a volume/aggregate issue"]}

---
