# Conversation: crash-on-save x omo-prometheus

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I understand how frustrating it is to lose work. I want to help get this resolved quickly.

To start narrowing this down: **where exactly in TaskFlow are you when the crash happens?** For example, are you saving a new task, editing an existing task, updating project settings, or something else? And when you say "crashes," what exactly do you see — does the app freeze, close entirely, show an error message, or something else?

Also, was this working for you before? If so, do you recall roughly when it started happening?

---

### reporter

I'm editing my task list and hitting Save in the toolbar. The whole app just closes on me. I think there's some error dialog that flashes for a split second but I can't read it before everything disappears. It started maybe a week or two ago? I don't remember exactly. It was working fine before that.

---

### triage-agent

Thanks for those details — that's really helpful. The flashing error dialog is a strong clue and I'd love to capture it.

Could you try to reproduce the crash and, right before you hit Save, **open your OS's screen recorder or press Print Screen repeatedly** so we can catch that error? On Windows you can also check **Event Viewer → Windows Logs → Application** for a crash entry, or on Mac check **Console.app** for recent crash logs around the time it happens.

Also, two quick specifics:
1. **What platform are you on?** (Windows/Mac/Linux, and which version of TaskFlow — you can usually find this under Help → About)
2. **Does it happen every single time you hit Save, or only sometimes?** If only sometimes, is there anything different about the times it works vs. doesn't (e.g., number of tasks, specific task content)?

---

### reporter

I'm on Mac, macOS 14.2, TaskFlow 2.3.1. It happens every time I hit Save. I don't really have time to mess around with screen recorders, I just need this fixed. I have like 200 tasks in there and it crashes every single time. I think the error says something about encoding but it disappears so fast I can barely see it.

---

### triage-agent

Got it, no worries about the screen recorder — Mac actually saves crash reports automatically. Could you do this:

1. **Open Finder → Go → Go to Folder** and paste: `~/Library/Logs/DiagnosticReports`
2. Look for any file with "TaskFlow" in the name and paste its contents here (or just the first 20 lines).

And one more thing — since the error mentions **encoding**: do any of your 200 tasks contain **emoji, accented characters, or non-English text** (e.g., café, naïve, 🎯, or anything with special symbols)? I'm asking because an encoding bug during save would crash exactly the way you're describing, and it would explain why it started recently if a new task with those characters was added around the same time.

---

### reporter

I don't really want to go digging through system folders... look, now that you mention it, I did import a bunch of tasks from a CSV file right around when this started. Some of those tasks might have weird characters in them, I pulled the data from a Word document so probably fancy quotes and dashes and stuff like that. But I mean, that's not exactly exotic, the app should be able to handle that.

---

### triage-agent

You're absolutely right — the app should handle those characters, and this sounds like a bug on our side. I have a strong theory: the Word-originated characters in your CSV import (smart quotes, em-dashes, etc.) are hitting an encoding bug in the save path.

One quick test to confirm before I hand this off to the dev team: **could you create a brand-new, empty task list, add one plain-text task like "test task," and try saving that?** If that saves fine, it confirms the imported data is the trigger and we'll know exactly where to look.

You wouldn't need to delete your real data — just a throwaway list to test with.

---

### reporter

Yeah fine, a new list with a simple task saves no problem. It's only my main list with all my imported stuff that crashes. Can you guys just fix it already?

---

### triage-agent

[RESOLVED] {"title":"App crashes on save when task list contains non-ASCII characters from CSV import (smart quotes, em-dashes)","problem":"TaskFlow crashes immediately (app closes) every time the user hits Save in the toolbar when the task list contains tasks imported from a CSV file that originated from a Word document. The CSV data contains Word-style typographic characters such as smart quotes (“”), em-dashes (—), and similar non-ASCII glyphs. A flashing error dialog mentioning 'encoding' appears briefly before the app closes. Saving a new list with plain ASCII tasks works fine.","root_cause_hypothesis":"The save/serialization path is not handling non-ASCII characters (specifically Windows-1252 or UTF-8 multi-byte characters like smart quotes and em-dashes) correctly. Most likely the save routine assumes ASCII or uses a strict encoding mode that throws a fatal exception on encountering these characters, rather than encoding them properly as UTF-8. This may have been introduced or exposed in a recent update (the user reports it worked before, timeline ~1-2 weeks).","reproduction_steps":["Create a CSV file containing tasks with Word-style smart quotes (“ ”), em-dashes (—), or other non-ASCII typographic characters","Import the CSV into TaskFlow as a task list","Click Save in the toolbar","Observe: app crashes with a brief encoding-related error dialog"],"environment":"macOS 14.2 (Sonoma), TaskFlow 2.3.1. Task list contains ~200 tasks, many imported from CSV sourced from a Word document.","severity":"high","impact":"User is completely unable to save their primary task list (~200 tasks). All edits are lost on every save attempt. No workaround exists short of removing the imported tasks. Any user who imports CSV data containing non-ASCII characters would hit this.","recommended_fix":"Investigate the save/serialization code path for encoding handling. Ensure all file writes use UTF-8 encoding (or at minimum handle multi-byte and extended-ASCII characters gracefully). Check for recent changes to the save path in the last 2-3 weeks that may have introduced a regression. Also verify the CSV import path — it may be ingesting data in one encoding but the save path may assume another.","proposed_test_case":"Create a task list containing tasks with smart quotes (“” ‘’), em-dashes (—), accented characters (é, ç, ñ), and emoji. Verify that saving and re-loading the list preserves all characters without errors or crashes.","information_gaps":["Exact crash log or stack trace (reporter declined to retrieve diagnostic reports)","Exact text of the flashing error dialog","Whether a specific recent TaskFlow update introduced this regression or if it correlates purely with the CSV import timing","The specific encoding of the original CSV file (likely Windows-1252 from Word)"]}

---
