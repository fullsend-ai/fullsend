# Conversation: crash-on-save x omo-prometheus

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I can see how losing work would be frustrating, and I want to help get this resolved.

To start narrowing this down, I need a few specifics:

1. **What exactly are you saving?** (e.g., a new task, editing an existing task, project settings, etc.)
2. **What exactly happens when it crashes?** (e.g., the app closes entirely, you get a white screen, an error message appears — if so, what does it say?)
3. **Is this new behavior, or has saving always been broken for you?** If it's new, did anything change recently (app update, new browser, etc.)?

Please be as specific as you can — exact error messages and steps to reproduce will help us fix this much faster.

---

### reporter

I'm saving my task list. I have like 200 tasks. When I hit Save the app just dies — closes completely. I think there's some error dialog that flashes for a split second but I can't read it, something about encoding maybe? I don't know. It wasn't always like this, it started recently but I don't remember exactly when. Can you just fix the save button?

---

### triage-agent

Thanks for those details — the encoding error flash is a really useful clue.

I want to figure out whether this is about the **number** of tasks or something about the **content** of specific tasks. Could you try this quick test?

1. Create a new task list with just 2–3 simple tasks (plain text, no special characters) and try saving it.
2. If that works, does your main list of 200 tasks contain any tasks with **special characters** — things like emoji, accented letters (é, ñ), curly quotes, or text pasted from another app like Word or Google Docs?

This will help us narrow down whether the crash is triggered by list size or by specific content in your tasks.

---

### reporter

Yeah fine, small lists save okay. And yeah there are special characters — I imported a bunch of tasks from a CSV file a while back and those have stuff like fancy quotes and dashes in them. Look, I just need to be able to save my work. Is this going to be fixed or not?

---

### triage-agent

[RESOLVED] {"title":"Save crashes with encoding error when task list contains special characters from CSV import","problem":"The application crashes (closes completely) when the user attempts to save a task list containing tasks that were imported from a CSV file. These tasks contain non-standard characters such as curly/smart quotes and em-dashes. A brief error dialog referencing 'encoding' flashes before the crash. Small lists with plain-text tasks save without issue.","root_cause_hypothesis":"The save/serialization path does not handle non-ASCII or non-UTF-8 characters gracefully. Tasks imported from CSV likely contain Windows-1252 or similar encoded characters (smart quotes U+2018/U+2019, em-dashes U+2014) that cause an unhandled encoding exception during save — either when serializing to JSON/storage format or when writing to disk. The lack of error handling causes the app to crash instead of surfacing a recoverable error.","reproduction_steps":["Create or import a task list from a CSV file containing smart/curly quotes (‘ ’ “ ”), em-dashes (—), or other non-ASCII characters","Ensure the list has a non-trivial number of such tasks (reporter had ~200, but the trigger is likely character content, not count)","Click Save","Observe: app crashes with a brief encoding-related error dialog"],"environment":"Not specified (reporter did not provide OS/browser/app version). Reproduction likely environment-independent since it is a data-driven encoding bug.","severity":"high","impact":"Any user who imports tasks from external sources (CSV, Word, Google Docs) with non-ASCII characters cannot save their work. Data loss occurs on every save attempt. No known workaround short of manually stripping special characters from all tasks.","recommended_fix":"1. Investigate the save/serialization code path for encoding assumptions — look for implicit ASCII encoding or missing UTF-8 declarations. 2. Ensure all string serialization uses UTF-8 throughout. 3. Add a try/catch around the save operation so encoding failures surface as a user-facing error message rather than a crash. 4. Consider sanitizing or normalizing special characters on CSV import (e.g., converting smart quotes to straight quotes) as a defense-in-depth measure.","proposed_test_case":"Create a task list containing tasks with smart quotes (‘example’), em-dashes (—), accented characters (é, ñ), and emoji. Save the list. Verify: save completes successfully, and reloading the list preserves all special characters without corruption.","information_gaps":["Exact app version and platform (desktop vs. web, OS, browser) — unlikely to change the fix direction since this is a data-driven bug","Exact error message from the crash dialog — would help pinpoint the exception type but developer can reproduce","Whether the CSV import itself introduced encoding corruption vs. the save path failing on valid Unicode — developer should check both paths"]}

---
