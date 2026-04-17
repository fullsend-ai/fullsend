# Conversation: crash-on-save x structured-triage

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I can see how frustrating it must be to lose your work.

To help us track this down, could you walk us through the exact steps that lead to the crash? For example:
- What are you working on when you hit save (creating a new task, editing an existing one, etc.)?
- How are you triggering the save (clicking a button, using a keyboard shortcut)?
- What happens when it crashes — does the app freeze, close entirely, or show an error message?

---

### reporter

I'm just editing my task list and clicking Save in the toolbar. The app just closes — like it completely crashes. I think there's some kind of error dialog that flashes for a split second but I can't read it before everything disappears. I have a pretty big task list, like 200 tasks or so. It's been working fine until recently and now it just crashes every time I try to save.

---

### triage-agent

Thanks for those details — that's really helpful. The large task list and the fact that it started recently are good clues.

Could you let us know what environment you're running the app in? Specifically:
- What operating system are you on (e.g., Windows 11, macOS Sonoma, Ubuntu)?
- Which browser, if it's the web version (e.g., Chrome 125, Firefox, Safari)?
- Do you know which version of TaskFlow you're using? (You can usually find this under Help → About or in Settings.)

---

### reporter

I'm on a Mac, macOS 14 something. I think the app version is 2.3, maybe 2.3.1? It's not a web app, it's the desktop version. Look, I don't think this is an environment issue — it was working fine like a week ago and I haven't updated anything. Something broke with the app itself.

---

### triage-agent

[RESOLVED] {"title":"Desktop app crashes on save with large task list (~200 tasks)","problem":"The desktop app crashes (completely closes) every time the user clicks Save in the toolbar while editing a large task list of approximately 200 tasks. A brief error dialog flashes before the app disappears. The issue started recently without any user-side changes.","root_cause_hypothesis":"A recent change (likely in the 2.3.x release) introduced a regression in the save path that fails under large task lists. Possible causes include a memory limit or buffer overflow when serializing ~200 tasks, a timeout on the write operation, or a UI-thread-blocking operation that triggers the OS watchdog. The recency of the issue and the large dataset size point toward a scaling-related regression.","reproduction_steps":["Create or load a task list with approximately 200 tasks in TaskFlow desktop app","Edit one or more tasks in the list","Click the Save button in the toolbar","Observe that the app crashes and closes entirely, with a brief error dialog flashing before exit"],"environment":"macOS 14 (Sonoma), TaskFlow desktop app version 2.3.x (likely 2.3 or 2.3.1)","severity":"high","impact":"Users with large task lists (~200+ tasks) cannot save their work at all, leading to data loss on every attempt. The crash is deterministic (every time), making the app unusable for this workflow.","recommended_fix":"Investigate the save code path for regressions introduced in the 2.3.x release. Focus on how large task lists are serialized and written to disk — check for memory allocation issues, synchronous I/O blocking the main thread, or new validation logic that fails on large datasets. Review git history for recent changes to the save/persistence layer. Test with task lists of varying sizes (50, 100, 200, 500) to find the threshold.","proposed_test_case":"Create a task list with 200+ tasks, modify at least one task, and invoke save. Verify the app saves successfully without crashing. Additionally, add a performance/stress test for the save path with task lists of 100, 500, and 1000 tasks to prevent future regressions.","information_gaps":["Exact app version (2.3 vs 2.3.1) — reporter was unsure","macOS crash report from Console.app or ~/Library/Logs/DiagnosticReports that would reveal the exact exception or signal","Whether the issue occurs with smaller task lists (e.g., 50 tasks) — would help identify the threshold","Whether the task list contains any unusual content (attachments, special characters, very long descriptions) that might trigger a serialization edge case"]}

---
