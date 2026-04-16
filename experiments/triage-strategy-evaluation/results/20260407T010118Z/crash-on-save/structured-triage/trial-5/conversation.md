# Conversation: crash-on-save x structured-triage

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I can see how frustrating it must be to lose your work.

To help us track this down, could you walk us through the exact steps you're taking when the crash happens? For example:

- What are you saving? (a new task, an edit to an existing task, project settings, etc.)
- How are you triggering the save? (clicking a button, using a keyboard shortcut, auto-save)
- Does the entire app close/freeze, or do you see an error message before it crashes?

Any detail you can share will help us reproduce and fix this faster.

---

### reporter

I'm editing my task list and clicking Save in the toolbar. The whole app just closes. I think there's some kind of error dialog that flashes for a split second but I can't read it before everything disappears. I have like 200 tasks in there. It's been happening every single time for the past few days and I'm sick of it.

---

### triage-agent

Thanks for those details — that's really helpful. Knowing it happens every time with ~200 tasks and that there's a flash of an error dialog gives us good leads.

Could you let us know what environment you're running TaskFlow in? Specifically:

- **Operating system** (e.g., Windows 11, macOS Sonoma, Ubuntu 24.04)
- **TaskFlow version** (you can usually find this under Help → About or in Settings)
- **Browser**, if you're using the web version (e.g., Chrome 124, Firefox 126)

This will help us narrow down whether it's platform-specific or something more general.

---

### reporter

It's on Mac, I think Sonoma or whatever the latest one is. TaskFlow version... I don't know, whatever the current version is? I updated it a while ago. It's not the web version, it's the desktop app. Look, does it really matter what OS I'm on? The app is crashing and losing my work.

---

### triage-agent

I completely understand the frustration — a crash that loses your work is a serious problem regardless of platform, and we want to get this fixed.

Since the error dialog disappears too fast to read, macOS actually keeps a record of crashes we can use instead. Could you try this?

1. Open **Console.app** (you can find it via Spotlight — press ⌘+Space and type "Console")
2. In the left sidebar, click **Crash Reports**
3. Look for any entries mentioning **TaskFlow**
4. If you find one, copy and paste the first ~20 lines here

Alternatively, if you see nothing there, try reproducing the crash and then checking **Console.app** immediately after — a new entry should appear at the top.

This will tell us exactly what's going wrong and help us fix it much faster than guessing.

---

### reporter

Ugh, I don't really know how to use Console app and I don't have time to dig through system logs. I'm not a developer, I just need this app to work. Look, I can tell you that this started happening after I imported a bunch of tasks from a CSV file. Before that it was saving fine. Can you just look into that?

---

### triage-agent

[RESOLVED] {"title":"Desktop app crashes on save after CSV import of ~200 tasks","problem":"The TaskFlow desktop app on macOS crashes immediately (entire app closes) when the user clicks the Save button in the toolbar. A brief error dialog flashes but disappears too fast to read. The user loses unsaved work each time. The crash is 100% reproducible and began after the user imported a large number of tasks from a CSV file.","root_cause_hypothesis":"The CSV import likely introduced data that triggers a failure during the save path — possible causes include malformed or oversized field values, unsupported characters, or a data volume issue (~200 tasks) that causes a serialization error, memory issue, or unhandled exception during save. The fact that saving worked before the import strongly points to the imported data itself.","reproduction_steps":["Import a large set of tasks (~200) from a CSV file","Edit the task list","Click the Save button in the toolbar","Observe: app crashes immediately with a brief error dialog flash"],"environment":"macOS Sonoma (exact version unconfirmed), TaskFlow desktop app (version unknown — user updated 'a while ago')","severity":"high","impact":"User loses all unsaved work on every save attempt. The app is effectively unusable for this user since they cannot persist any changes. Any user who imports a large CSV may hit the same issue.","recommended_fix":"1. Attempt to reproduce with a large CSV import (~200 tasks) — try with varied data (special characters, long fields, empty fields). 2. Inspect the save/serialization code path for unhandled exceptions, particularly around data validation or size limits. 3. Check for uncaught exceptions in the Electron/native crash handler that could explain the disappearing error dialog. 4. Add proper error handling so save failures display a persistent error message instead of crashing the app.","proposed_test_case":"Import a CSV file containing 200+ tasks (including edge-case data: special characters, very long text, empty optional fields) and verify that clicking Save completes successfully without crashing. Additionally, verify that if a save error does occur, it is caught and displayed in a persistent error dialog rather than crashing the app.","information_gaps":["Exact TaskFlow version (reporter declined to check)","Crash log or stack trace (reporter declined to retrieve from Console.app)","Contents/format of the original CSV file used for import","Exact macOS version (reporter unsure if Sonoma or latest)"]}

---
