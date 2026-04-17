# Conversation: crash-on-save x structured-triage

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I can see how frustrating it would be to lose your work repeatedly.

To help us investigate, could you describe what exactly happens when the app crashes? For example:
- Do you see an error message or dialog before it closes?
- Does the app freeze first, or does it close immediately?
- What type of content are you saving when this happens (e.g., a new task, editing an existing task, a specific field)?

---

### reporter

It just closes. There's maybe a quick flash of something on screen but it happens so fast I can't read it. I'm trying to save my task list, I have a bunch of tasks in there. It worked fine before, now it just crashes every time I hit Save.

---

### triage-agent

Thanks for those details — that quick flash sounds like it could be an unhandled error dialog disappearing as the app closes, which will be useful to investigate.

Could you let us know what environment you're running TaskFlow in? Specifically:
- What operating system are you on (e.g., Windows 11, macOS Sonoma, Ubuntu 24.04)?
- Are you using the desktop app or the web version? If web, which browser?
- Do you know which version of TaskFlow you're running? (You can usually find this under Help → About or in Settings.)

---

### reporter

I'm on a Mac, macOS 14.2. Desktop app. I don't know the exact version off the top of my head, maybe 2.3 something? Look, does it really matter what OS I'm on? The app is crashing when I save, it shouldn't be doing that on any OS.

---

### triage-agent

That's fair — you're right that it shouldn't crash on any OS, and we're taking this seriously.

You mentioned it worked fine before. Can you help me narrow down what changed? Specifically:
- Did TaskFlow update recently, or did this start happening out of the blue one day?
- Roughly how many tasks do you have in the list you're trying to save — are we talking dozens, hundreds?

This will help us reproduce the crash on our end so we can get it fixed.

---

### reporter

I don't know if it updated, I don't keep track of that stuff. I have like 200 tasks or so. Actually now that I think about it, it might have started after I imported a bunch of tasks from a CSV file, but I'm not sure why that would matter. The point is it crashes every single time I hit Save and I need this fixed.

---

### triage-agent

[RESOLVED] {"title":"App crashes immediately on save with large task list (~200 tasks, possibly after CSV import)","problem":"TaskFlow desktop app closes instantly when the user clicks Save on their task list. A brief flash appears on screen (likely an unhandled error dialog) but disappears too quickly to read. The user loses unsaved work each time.","root_cause_hypothesis":"The crash is likely triggered by saving a large number of tasks (~200), possibly related to data imported from a CSV file. The CSV import may have introduced malformed data, special characters, or a volume of records that exceeds a buffer or triggers an unhandled serialization error during the save operation.","reproduction_steps":["Create or import a large task list (~200 tasks) via CSV import in TaskFlow desktop app","Click Save","Observe that the app crashes immediately with a brief visual flash"],"environment":"macOS 14.2 (Sonoma), TaskFlow desktop app, version approximately 2.3.x","severity":"high","impact":"User cannot save any work at all, resulting in complete data loss for the session. This is a total workflow blocker for any user with a similarly large or CSV-imported task list.","recommended_fix":"Investigate the save/serialization path for large task lists. Specifically: (1) Check whether CSV-imported tasks contain data that fails validation or serialization on save (encoding issues, unexpected fields, oversized content). (2) Add a try/catch around the save operation to surface the error instead of crashing. (3) Test save behavior with 200+ tasks created manually vs. imported from CSV to isolate whether volume or import-specific data is the trigger.","proposed_test_case":"Import a CSV file containing 200+ tasks into TaskFlow, then trigger a save. Verify that the save completes successfully without crashing. Additionally, test with CSVs containing edge-case data (special characters, very long fields, empty fields) to confirm robustness.","information_gaps":["Exact TaskFlow version (user said approximately 2.3.x)","Crash log from macOS Console/crash reporter that would pinpoint the exact exception","Whether the crash also occurs with a smaller number of tasks or only at scale","Contents/format of the original CSV file used for import"]}

---
