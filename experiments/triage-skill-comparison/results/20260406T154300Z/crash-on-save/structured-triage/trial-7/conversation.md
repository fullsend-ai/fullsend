# Conversation: crash-on-save x structured-triage

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I can see how frustrating it must be to lose your work.

To help us track this down, could you describe exactly what happens when the crash occurs? For example, does the app freeze and then close, do you see an error message or dialog, or does it just disappear? Any specific error text you see would be very helpful.

---

### reporter

When I click Save in the toolbar, there's a brief flash of a dialog box — it goes by really fast but I think it says something about 'encoding' — and then the app just closes completely. No freeze, no spinning wheel, it just disappears. I have to reopen the app and my unsaved changes are gone.

---

### triage-agent

Thanks for that detail — the brief 'encoding' dialog is a really useful clue.

Could you let us know what environment you're running TaskFlow in? Specifically:
- What operating system are you on (e.g., Windows 11, macOS Sonoma, Ubuntu 24.04)?
- Which version of TaskFlow are you using (you can usually find this under Help → About)?
- Are you using the desktop app or the web version, and if web, which browser?

This will help us narrow down whether this is platform-specific.

---

### reporter

I'm on macOS 14.2 (Sonoma), using TaskFlow v2.3.1, the desktop app.

---

### triage-agent

[RESOLVED] {"title":"Desktop app crashes on save with encoding-related error (macOS, v2.3.1)","problem":"When the user clicks Save in the toolbar, a dialog briefly flashes (mentioning 'encoding') and the app immediately closes, losing all unsaved changes. This occurs consistently.","root_cause_hypothesis":"The save operation likely encounters a character encoding issue — possibly the task content contains characters that the serialization layer cannot handle (e.g., non-UTF-8 characters, special Unicode, or an encoding mismatch between the editor and the file writer). The unhandled encoding exception causes the app to crash rather than surfacing a user-facing error.","reproduction_steps":["Open TaskFlow v2.3.1 desktop app on macOS 14.2","Create or open a task (potentially one containing non-ASCII or special characters)","Click the Save button in the toolbar","Observe: a dialog briefly flashes mentioning 'encoding', then the app closes"],"environment":"macOS 14.2 (Sonoma), TaskFlow v2.3.1, desktop app","severity":"high","impact":"Users lose all unsaved work every time they attempt to save. This blocks core functionality — the app is effectively unusable for affected users since saving is a fundamental operation.","recommended_fix":"Investigate the save/serialization path for unhandled encoding exceptions. Check what character encoding the file writer expects versus what the editor produces. Add proper error handling around the save operation so encoding failures surface as a user-facing error dialog rather than crashing the app. Review crash logs on macOS (Console.app / ~/Library/Logs/DiagnosticReports) for the specific exception.","proposed_test_case":"Create a task containing various character classes (ASCII, accented characters, emoji, CJK characters, mixed encodings) and verify that saving succeeds or fails gracefully with an informative error — never crashes.","information_gaps":["Exact error message from the flashing dialog (goes by too fast to read fully)","Whether the crash occurs with all tasks or only tasks containing specific characters/content","Crash logs from macOS diagnostic reports","Whether this is a regression from a previous TaskFlow version"]}

---

