# Conversation: crash-on-save x omc-deep-interview

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — sorry you're dealing with crashes and lost work. To help us track this down, could you describe exactly what happens when it crashes? Specifically: what are you working on when you hit save (e.g., creating a new task, editing an existing one), and do you see any error message or does the app just close/freeze without warning?

---

### reporter

It just crashes. I'm editing my task list and when I click Save it just closes. I think there's some error that flashes for a second but it's gone too fast to read — something about encoding maybe? I don't know. It's been happening for a few days now and I just need it to work.

---

### triage-agent

That brief 'encoding' flash is a really helpful clue — thank you. Can you tell me what kind of content is in the task you're saving when it crashes? For example, does it contain any special characters, emoji, or text pasted from another app? Also, does this happen with every task you try to save, or only specific ones?

---

### reporter

Look, I have like 200 tasks in there. I don't know which specific one is causing it. I did import a bunch of tasks from a CSV file a while back and I think that's around when this started happening, but I'm not sure if that matters. It happens every time I click Save. Can you just fix the save button?

---

### triage-agent

That CSV import is likely the key here — encoding issues from imported data would line up perfectly with what you're seeing. Could you try something quick for me? If you create a brand new task list (without importing anything) and add a simple task manually, does saving that one also crash? That'll tell us whether it's the imported data causing the problem or something else entirely. And if you still have the original CSV file, would you be able to share it (or a few rows from it)? That would let us reproduce the crash on our end and get this fixed fast.

---

### reporter

I don't have time to go creating new task lists to test things for you. I just need Save to work. I don't think I still have the CSV file either. Look, all I know is it was working fine before I imported those tasks, and now it crashes every time I hit Save. Smaller lists I've made for other stuff seem to save fine but my main list with all 200 tasks just dies every time.

---

### triage-agent

Totally fair — you shouldn't have to test things on our behalf. One last thing that could help us nail this down quickly: does TaskFlow have a logs or crash report file on your machine? On most setups you can find it at `~/.taskflow/logs/` (macOS/Linux) or `%APPDATA%\TaskFlow\logs\` (Windows). If there's a recent file in there, the exact error message would tell us precisely what's going wrong with the encoding so we can push a fix. Also, which version of TaskFlow are you running and what OS are you on?

---

### reporter

I'm on macOS 14.2 and TaskFlow 2.3.1. I don't know about log files, I'm not going to go digging around in hidden folders. Can someone just look at the save code? It crashes when you have a big list with imported data. That's all I know.

---

### triage-agent

[RESOLVED] {"title":"Crash on save when task list contains CSV-imported data (encoding error)","problem":"TaskFlow crashes immediately when the user clicks Save on a task list containing ~200 tasks that were previously imported from a CSV file. A brief error message mentioning 'encoding' flashes before the app closes. Smaller, manually-created task lists save without issue.","root_cause_hypothesis":"The CSV import path does not normalize character encoding (likely accepts raw bytes in Latin-1, Windows-1252, or mixed encoding). When the save/serialization path later attempts to encode the full task list (probably as UTF-8), it hits an invalid byte sequence and throws an unhandled encoding exception that crashes the app.","reproduction_steps":["Create a CSV file containing task data with non-UTF-8 characters (e.g., Windows-1252 curly quotes, accented characters, or raw Latin-1 bytes)","Import the CSV into TaskFlow to create a task list with a large number of tasks (~200)","Click Save","Observe: app crashes with a brief encoding-related error"],"environment":"macOS 14.2, TaskFlow 2.3.1","severity":"high","impact":"User cannot save their primary task list and loses work on every attempt. Any user who has imported tasks via CSV with non-UTF-8 encoded content is likely affected. Workaround: smaller lists without imported data save normally.","recommended_fix":"1. Inspect the save/serialization code path for unhandled encoding exceptions — add a try/catch that surfaces a user-friendly error instead of crashing. 2. Audit the CSV import path to normalize all incoming text to UTF-8 (or the app's canonical encoding) at import time. 3. Consider adding a migration or repair pass that re-encodes existing task data to fix already-imported lists.","proposed_test_case":"Import a CSV containing text in multiple encodings (UTF-8, Latin-1, Windows-1252 with smart quotes) into a task list with 200+ entries, then save. Verify: (a) no crash, (b) characters are preserved or gracefully replaced, (c) if any characters can't be converted, a clear error message is shown instead of a crash.","information_gaps":["Exact error message from crash logs (reporter declined to retrieve logs)","Contents of the original CSV file (reporter no longer has it)","Whether the issue is specific to certain characters/rows or affects the entire serialization of imported data"]}

---
