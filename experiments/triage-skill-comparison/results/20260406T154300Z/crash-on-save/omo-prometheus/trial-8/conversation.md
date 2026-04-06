# Conversation: crash-on-save x omo-prometheus

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — losing work is definitely frustrating, and I want to help get this resolved.

To start narrowing this down: what specifically are you saving when the crash happens? (e.g., a new task, editing an existing task, project settings, etc.) And can you tell me what version of TaskFlow you're running, what platform you're on (web, desktop, mobile), and whether this used to work before — i.e., did saving work fine at some point and then stop?

---

### reporter

I'm on TaskFlow v2.3.1, macOS 14.2, desktop app. I'm editing a task list — I have about 200 tasks in it. The crash happens when I click the Save button in the toolbar. It was working fine until recently... actually, now that I think about it, it started after I imported a bunch of tasks from a CSV file. Before that import, saving worked perfectly.

---

### triage-agent

Thanks for that detail — the CSV import timing is a really helpful clue.

Two things I'd like to pin down:

1. **What exactly happens when it crashes?** Does the app suddenly close (disappears from the dock), do you get an error dialog or macOS crash report, or does it freeze/hang and you have to force-quit?

2. **Is the crash tied to the imported tasks specifically?** If you create a brand-new task list with just a couple of manually-created tasks and hit Save, does that work? I'm trying to figure out if the problem is with the imported data itself or if something broke globally after the import.

---

### reporter

When I click Save, I see a quick flash of an error dialog — it says something about 'encoding' I think, but it disappears so fast I can barely read it — and then the app just closes completely. It disappears from the dock, no macOS crash report or anything like that. It doesn't freeze, it just... poof, gone.

As for your second question — yes, if I create a small test list with just a few tasks and save, it works fine. And actually, I tried removing the imported tasks from my main list and saving worked again after that. So it definitely seems tied to the imported data. The CSV had some tasks with special characters in the names like fancy quotes and dashes, if that matters.

---

### triage-agent

[RESOLVED] {"title":"Desktop app crashes on save when task list contains CSV-imported tasks with special characters (encoding error)","problem":"When saving a task list that contains tasks imported from a CSV file, the desktop app shows a brief error dialog referencing 'encoding' and then immediately closes. The crash is isolated to lists containing CSV-imported data — manually created tasks save without issue. The CSV contained tasks with special characters such as curly/smart quotes and em-dashes.","root_cause_hypothesis":"The CSV import path is ingesting text with non-ASCII characters (smart quotes, em-dashes, etc.) that are stored in a form the save/serialization layer cannot handle. Most likely the CSV is read with one encoding (e.g., UTF-8 or Windows-1252) but the save routine assumes a different or more restrictive encoding (e.g., ASCII or a mismatched UTF variant), causing an unhandled encoding exception that crashes the app instead of being caught gracefully.","reproduction_steps":["Open TaskFlow v2.3.1 desktop app on macOS","Prepare a CSV file containing tasks with special characters (curly quotes, em-dashes, etc.)","Import the CSV into a task list","Click the Save button in the toolbar","Observe: brief error dialog mentioning 'encoding' appears, then the app closes immediately"],"environment":"TaskFlow v2.3.1, macOS 14.2 (Sonoma), desktop app. Task list with ~200 items, including tasks imported from CSV with non-ASCII characters.","severity":"high","impact":"Any user who imports tasks from CSV files containing non-ASCII characters (common in real-world data) will be unable to save their work. The app crashes without a proper error message or recovery path, causing data loss. This blocks a core workflow (import + save).","recommended_fix":"1. Investigate the save/serialization code path for encoding assumptions — ensure it handles UTF-8 (and other encodings) consistently with the CSV import path. 2. Add proper error handling around the save operation so encoding failures surface as a user-visible error message rather than crashing the app. 3. Consider normalizing special characters (e.g., curly quotes → straight quotes) during CSV import, or at minimum validating that imported text is round-trippable through the save path. 4. Check for an unhandled exception in the encoding/serialization layer that is causing the abrupt app exit.","proposed_test_case":"Import a CSV file containing tasks with various non-ASCII characters (smart/curly quotes, em-dashes, accented characters, emoji) into a task list, then save. Verify: (a) the save completes successfully, (b) the special characters are preserved correctly when the list is reopened, and (c) if any character cannot be saved, a clear error message is shown instead of a crash.","information_gaps":["Exact error message in the flash dialog (reporter could not read it fully)","Whether macOS Console.app or ~/Library/Logs contains a crash log with a stack trace","The exact encoding of the original CSV file (UTF-8, Windows-1252, etc.)","Whether the issue reproduces on other platforms (web, Windows) or is macOS-desktop-specific"]}

---

