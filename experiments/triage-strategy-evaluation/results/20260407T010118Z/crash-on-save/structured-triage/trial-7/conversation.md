# Conversation: crash-on-save x structured-triage

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I'm sorry you're losing work, that's definitely frustrating.

To help us investigate, could you describe what you expect to happen when you click save? For example, are you saving a new task, editing an existing one, or saving changes to settings? And what does the crash look like — does the app close entirely, freeze, or show an error screen?

---

### reporter

I'm editing tasks, not settings. When I hit Save in the toolbar the whole app just closes. There's like a flash of some error dialog but it disappears too fast to read. I think it said something about encoding? I don't know. The point is I can't save my work and I've lost changes multiple times now.

---

### triage-agent

Thanks for those details — that helps narrow it down.

Could you let us know what environment you're running TaskFlow in? Specifically:
- What operating system are you on (e.g., Windows 11, macOS Sonoma, Ubuntu 24.04)?
- Are you using the desktop app or the web version? If web, which browser and version?
- What version of TaskFlow are you running? (You can usually find this under Help → About or in Settings.)

---

### reporter

macOS, the desktop app, and I don't remember the exact version. Maybe 2.3 something? Look, can we focus on fixing the crash instead of going through 20 questions?

---

### triage-agent

[RESOLVED] {"title":"Desktop app crashes (force-closes) when saving an edited task via toolbar Save button on macOS","problem":"When editing a task and clicking the Save button in the toolbar, the entire desktop app force-closes. A brief error dialog flashes before the app disappears, reportedly mentioning something about 'encoding'. The reporter has lost work multiple times due to this crash.","root_cause_hypothesis":"An unhandled exception in the task serialization/save path, likely related to character encoding — possibly a task containing special characters (emoji, non-ASCII text, or smart quotes from macOS) that the save routine fails to encode properly, causing a fatal crash instead of a graceful error.","reproduction_steps":["Open TaskFlow desktop app on macOS","Open an existing task for editing","Make changes to the task","Click the Save button in the toolbar","Observe: app force-closes with a brief error dialog"],"environment":"macOS (version unspecified), TaskFlow desktop app, version approximately 2.3.x","severity":"high","impact":"Users lose unsaved work every time they attempt to save edited tasks. The reporter has experienced data loss multiple times. This blocks a core workflow (editing and saving tasks).","recommended_fix":"Investigate the task save/serialization code path in the desktop app for encoding-related exceptions. Check for unhandled errors when serializing task content containing non-ASCII characters. Add proper error handling so encoding failures surface a user-visible error message rather than crashing the app. Review crash logs on macOS (Console.app / ~/Library/Logs/DiagnosticReports) for the exact stack trace.","proposed_test_case":"Create a task containing various special characters (emoji, accented characters, smart quotes, CJK characters), edit it, and verify that saving via the toolbar completes without crashing. Also verify that a graceful error is shown if encoding does fail.","information_gaps":["Exact TaskFlow version number","Exact macOS version","Full text of the error dialog","Whether the crash occurs with all tasks or only specific ones (e.g., those containing special characters)","Whether crash logs are available in macOS Console"]}

---
