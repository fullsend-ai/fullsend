# Conversation: crash-on-save x omo-prometheus

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I can see how losing work would be frustrating, and I want to help get this resolved.

To start narrowing this down, I need a few specifics:

**What exactly are you saving when the crash occurs?** For example, are you saving a task, editing a project, updating settings, or something else?

Also, when you say "crashes" — what exactly happens? Does the app close entirely, do you see a white screen, an error message, or does it freeze? Any details on what you see right before and after the crash would be very helpful.

---

### reporter

I'm saving my task list. I have like 200 tasks in there. When I hit Save the whole app just closes on me. I think there's some kind of error dialog that flashes for a second but it disappears too fast to read it. Something about encoding maybe? I don't know. It just crashes and I lose everything.

---

### triage-agent

Thanks for those details — that's very helpful. The encoding error you glimpsed is a strong lead.

Here's what I'd like to narrow down next: **do any of your tasks contain special characters, emoji, or non-English text?** For example, things like accented letters (é, ñ), symbols (€, ©), or emoji (✅, 🔥). Even if only a few tasks have them, that could be triggering the crash during save.

If you're not sure, could you try duplicating your task list, removing a chunk of tasks (say, keep only the first 50), and attempting to save? That would tell us whether it's related to the volume of tasks or to specific task content.

---

### reporter

Yeah actually now that you mention it, I imported a bunch of tasks from a CSV file a while back and I think some of them have weird characters like fancy quotes and dashes. The crashing started after that import I think. I don't really want to sit here and delete tasks one by one to test this for you though, can't you just fix the save so it doesn't crash?

---

### triage-agent

Thanks — that's a really helpful timeline. I'm not going to ask you to delete tasks one by one, don't worry.

Here's what I think is happening: the CSV import brought in characters like curly quotes (“ ”) and em-dashes (—) that our save function isn't handling correctly, which causes the crash. The fact that it started right after the import lines up with this.

One more thing I need to know so we can route this to the right developer: **what platform are you running TaskFlow on?** Specifically — is this the desktop app (Windows/Mac/Linux) or the web app in a browser? And do you happen to know which version you're on? (You can usually find it under Help → About or in Settings.)

---

### reporter

It's the Mac desktop app. I don't know the exact version off the top of my head, maybe 2.3 something? Look can you just fix the save feature, I've been dealing with this for days now.

---

### triage-agent

[RESOLVED] {"title":"Mac desktop app crashes on save when task list contains special Unicode characters from CSV import","problem":"The Mac desktop app (approximately v2.3.x) crashes immediately when the user saves a task list that contains special characters — specifically curly quotes, em-dashes, and similar non-ASCII characters introduced via CSV import. The app closes abruptly with a briefly-visible encoding-related error dialog, and unsaved work is lost.","root_cause_hypothesis":"The save/serialization function is not handling certain Unicode characters (smart quotes, em-dashes, and potentially other non-ASCII characters commonly introduced by CSV exports from tools like Excel). The encoding error suggests the save path is either using ASCII-only encoding, failing to properly encode UTF-8, or encountering an unhandled exception during character serialization that crashes the app instead of being caught gracefully.","reproduction_steps":["Install TaskFlow Mac desktop app (~v2.3.x)","Create or import a CSV file containing tasks with special characters: curly/smart quotes (“ ”), em-dashes (—), and other non-ASCII Unicode characters","Import the CSV into TaskFlow (a large number of tasks, e.g. 200, may increase likelihood but the trigger is the character content, not volume)","Attempt to save the task list","Observe: app crashes with a briefly-visible encoding error dialog"],"environment":"Mac desktop app, approximately version 2.3.x. CSV data source likely originated from a tool that uses smart/curly quotes (e.g., Excel, Google Sheets).","severity":"high","impact":"User cannot save their task list at all, resulting in repeated data loss. The user has been blocked for days. Any user who imports CSV data containing non-ASCII characters will hit this. There is no known workaround short of manually sanitizing the imported data.","recommended_fix":"1. Investigate the save/serialization path for character encoding — ensure it uses UTF-8 throughout. 2. Add proper error handling around the save operation so encoding failures are caught and reported gracefully rather than crashing the app. 3. Consider sanitizing or normalizing Unicode characters during CSV import (e.g., replacing smart quotes with ASCII equivalents, or ensuring proper encoding at import time). 4. Add an auto-save or recovery mechanism so crashes during save don't result in data loss.","proposed_test_case":"Create a task list containing smart quotes (“”‘’), em-dashes (—), and other common non-ASCII characters (accented letters, emoji, symbols like € ©). Verify that saving completes successfully without errors. Additionally, test with a CSV import containing these characters followed by a save operation.","information_gaps":["Exact app version number (user said approximately 2.3)","Exact error message from the dialog (it disappears too quickly for the user to read)","Whether the issue reproduces with a small number of tasks containing special characters (isolating volume vs. content as the trigger)","Whether the crash also affects the web app or is Mac-desktop-specific","Console/crash logs from the Mac app that would pinpoint the exact failure location"]}

---
