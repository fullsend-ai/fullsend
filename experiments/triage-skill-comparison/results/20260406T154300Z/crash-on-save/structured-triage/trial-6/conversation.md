# Conversation: crash-on-save x structured-triage

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — losing work to crashes is definitely frustrating, and we want to get this sorted out.

To help us track this down, could you describe what exactly happens when the app crashes? For example:
- Does the app close entirely, freeze, or show an error message?
- If you see an error message or dialog, what does it say?

This will help us narrow down where the problem is occurring.

---

### reporter

When I hit Save, the app just closes completely. Right before it dies, I see a dialog box flash for like a split second — I can't read the whole thing but I've caught the word 'encoding' in it a couple times. Then the app is just gone. No freeze or anything, it just shuts down instantly.

---

### triage-agent

Thanks for those details — that flashing 'encoding' dialog is a helpful clue.

Could you walk us through the steps you take right before the crash? Specifically:
- What are you working on when you hit Save? (e.g., creating a new task, editing an existing one)
- Does the content you're saving include anything unusual — like special characters, emoji, or text pasted from another application?

This will help us try to reproduce the crash on our end.

---

### reporter

So here's what happened — I imported a bunch of tasks from a CSV file a few days ago, and that's when the problems started. The CSV had about 200 tasks in it, and some of the task names have special characters like em-dashes and curly quotes (the fancy ones, not straight quotes). When I hit Save from the toolbar, that's when it crashes. Interestingly, the auto-save doesn't seem to cause the crash, only when I manually click Save. If I delete the imported tasks and just work with my original ones, saving works fine.

---

### triage-agent

That CSV import detail is really useful — the combination of special characters and manual-save-only crashing gives us a strong lead.

Could you let us know what environment you're running TaskFlow in? Specifically:
- What operating system are you on? (e.g., Windows 11, macOS Sonoma, Ubuntu 24.04)
- Which version of TaskFlow are you using? (you can usually find this under Help → About)
- If it's the web version, which browser and version?

This matters because encoding handling can differ across platforms, so knowing your setup will help us reproduce the crash.

---

### reporter

I'm on macOS 14.2 (Sonoma) and TaskFlow version 2.3.1. It's the desktop app, not the web version.

---

### triage-agent

[RESOLVED] {"title":"Desktop app crashes on manual save when tasks contain non-ASCII characters imported from CSV","problem":"TaskFlow 2.3.1 desktop app crashes immediately (closes without recovery) when the user manually saves via the toolbar, but only when the task list contains tasks imported from a CSV file that include non-ASCII characters such as em-dashes and curly quotes. Auto-save does not trigger the crash. Removing the imported tasks restores normal save behavior.","root_cause_hypothesis":"The manual save code path likely uses a different character encoding (or lacks encoding specification) compared to auto-save. When serializing task data containing non-ASCII characters (em-dashes, curly quotes from the CSV import), the manual save path probably attempts an encoding conversion that fails — the flashing 'encoding' dialog suggests an unhandled encoding exception that triggers an app-level crash handler. Auto-save may use a different serialization method or already handles UTF-8 correctly.","reproduction_steps":["Open TaskFlow 2.3.1 desktop app on macOS","Prepare a CSV file containing ~200 tasks with non-ASCII characters (em-dashes, curly/smart quotes) in task names","Import the CSV file into TaskFlow","Click Save from the toolbar","Observe: a dialog briefly flashes with the word 'encoding', then the app closes entirely"],"environment":"macOS 14.2 (Sonoma), TaskFlow 2.3.1, desktop app (not web)","severity":"high","impact":"Users who import task data containing non-ASCII characters from CSV files cannot manually save, causing data loss. Any user working with internationalized text or content pasted from word processors is likely affected.","recommended_fix":"Compare the encoding handling between the manual save and auto-save code paths. The manual save path likely needs to explicitly use UTF-8 encoding when serializing task data. Check for any encoding conversion or validation step in the manual save flow that is absent from auto-save. The flashing dialog suggests an error dialog is shown before the crash — adding proper error handling there would at minimum prevent the hard crash.","proposed_test_case":"Create a task with non-ASCII characters (em-dashes '—', curly quotes '‘’“”', accented characters) via CSV import, then trigger a manual save and verify it completes without error. Also verify that round-tripping (save then reload) preserves the special characters correctly.","information_gaps":["Full text of the flashing error dialog — may contain a specific encoding name or stack trace","Whether the crash also occurs if special characters are typed directly into a task (not via CSV import)","Application crash logs from macOS Console that might reveal the exact exception"]}

---

