# Conversation: crash-on-save x structured-triage

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I can see how frustrating it must be to lose your work.

To help us track this down, could you walk us through the exact steps you take before the crash? For example:
- What screen or feature are you using when you hit save? (e.g., editing a task, updating project settings, etc.)
- What does the crash look like — does the app close entirely, freeze, or show an error message?

Any detail you can share will help us reproduce and fix this faster.

---

### reporter

I'm editing my task list and I click Save in the toolbar and the whole app just closes. I see some error dialog flash for a split second but it disappears too fast to read. I have like 200 tasks in there. Can you just fix it? I need to get my work done.

---

### triage-agent

Thanks for those details — that's really helpful. Editing a large task list (~200 tasks) and hitting Save in the toolbar gives us a solid reproduction path.

One more thing that will help us narrow this down: what environment are you running the app in? Specifically:
- **OS:** Windows, macOS, or Linux (and version if you know it)
- **App version:** You can usually find this under Help → About or in Settings
- **Browser** (if this is the web version): Chrome, Firefox, Safari, etc.

This will help us reproduce the crash on our end.

---

### reporter

I'm on a Mac, macOS 14.2. The app version is... I think 2.3.1? Something like that. It's not a web app, it's the desktop version.

---

### triage-agent

Thanks — macOS 14.2 on the desktop app, that's exactly what we needed.

One last thing: since the app is crashing on macOS, there's almost certainly a crash report saved automatically. Could you check for it?

1. Open **Finder** → Go → Go to Folder → type `~/Library/Logs/DiagnosticReports`
2. Look for any recent files with "TaskFlow" in the name (they'll end in `.ips` or `.crash`)
3. If you find one, paste the first ~30 lines here — that will give us the exact error.

Alternatively, open **Console.app** (search for it in Spotlight), click "Crash Reports" in the sidebar, and look for TaskFlow.

This will tell us exactly what's going wrong and help us fix it much faster.

---

### reporter

Look, I'm not really a technical person and I don't want to go digging around in system folders. I just want to save my tasks without the app blowing up. I will say that this started happening after I imported a bunch of tasks from a CSV file, if that helps. Before that it was working fine.

---

### triage-agent

[RESOLVED] {"title":"Desktop app crashes on save with large task list imported from CSV","problem":"The desktop app (macOS) crashes immediately when the user clicks Save in the toolbar while editing a task list containing approximately 200 tasks. The crash began occurring after the user imported tasks from a CSV file. An error dialog appears briefly before the app closes but disappears too quickly to read.","root_cause_hypothesis":"The CSV import likely introduced malformed data, excessively long fields, or special characters that the save/serialization path cannot handle. With ~200 tasks, this could also be a memory or payload-size issue during serialization, but the fact that it worked before the CSV import and broke after strongly points to bad data introduced by the import rather than a pure scale problem.","reproduction_steps":["Create or use a task list in the desktop app","Import a large number of tasks (~200) from a CSV file","Edit the task list","Click Save in the toolbar","Observe: app crashes with a briefly-visible error dialog"],"environment":"macOS 14.2 (Sonoma), TaskFlow desktop app version ~2.3.1","severity":"high","impact":"User is completely unable to save their work, resulting in repeated data loss. This blocks normal usage of the app for anyone with a CSV-imported task list of this size.","recommended_fix":"Investigate the save/serialization code path for large task lists, particularly with CSV-imported data. Check for: (1) unescaped or malformed characters from CSV import that break serialization, (2) field length or row count limits in the save handler, (3) the briefly-visible error dialog — find the exception it reports (likely an uncaught error in the save routine). Adding a try/catch around the save operation to surface the actual error would be a good first step. Also review the CSV import path for insufficient validation.","proposed_test_case":"Import a CSV file with ~200 tasks (including edge cases: special characters, very long fields, empty fields, unicode) into a new task list, then attempt to save. Verify save completes without crashing and data round-trips correctly.","information_gaps":["Exact crash log / stack trace (reporter declined to retrieve macOS crash reports)","Contents or structure of the CSV file used for import","Whether the crash occurs with fewer CSV-imported tasks (threshold)"]}

---
