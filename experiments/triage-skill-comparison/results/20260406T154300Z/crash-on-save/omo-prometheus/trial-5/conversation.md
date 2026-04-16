# Conversation: crash-on-save x omo-prometheus

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I understand how frustrating it is to lose work. To help us track this down, I need a few specifics:

1. **What are you saving?** (e.g., a new task, editing an existing task, project settings, etc.)
2. **What exactly happens when it crashes?** Does the app close entirely, do you see an error message or dialog, or does it freeze/hang?
3. **When did this start?** Was saving working for you before, and if so, did anything change (app update, OS update, etc.)?

Any one of these details would help us narrow this down significantly.

---

### reporter

I'm editing my task list — I have about 200 tasks in it. When I hit 'Save' in the toolbar the app just closes on me. I think I see a dialog flash for a split second that mentions something about 'encoding' but it disappears too fast to read. It started after I imported a bunch of tasks from a CSV file. Before that, saving worked totally fine.

---

### triage-agent

[RESOLVED] {"title":"App crashes on save after importing tasks from CSV — likely encoding error in serialized data","problem":"After importing approximately 200 tasks from a CSV file, the application force-closes whenever the user clicks 'Save' in the toolbar. A dialog briefly flashes mentioning 'encoding' before the app terminates. Saving worked correctly prior to the CSV import. The user is losing unsaved work on each crash.","root_cause_hypothesis":"The CSV import introduced task data containing characters that the save/serialization routine cannot encode — most likely non-UTF-8 characters, BOM markers, or special Unicode characters (e.g., smart quotes, accented characters, emoji) from the CSV source. The save routine appears to hit an unhandled encoding exception and crashes instead of gracefully reporting the error.","reproduction_steps":["Start with a working TaskFlow installation with a small task list (confirm save works)","Prepare a CSV file with ~200 tasks containing non-ASCII characters (e.g., accented names, smart quotes, or mixed encoding)","Import the CSV file into TaskFlow","Click 'Save' in the toolbar","Observe: app crashes with a brief 'encoding' error dialog flash"],"environment":"Not fully specified — but the issue is likely platform-independent since it is tied to data content rather than OS. Reporter has ~200 tasks post-import.","severity":"high","impact":"Any user who imports a CSV containing non-standard-encoding characters will be unable to save their task list at all, resulting in repeated data loss. The user is completely blocked from saving until the imported data is removed or the bug is fixed.","recommended_fix":"1. Inspect the save/serialization code path for unhandled encoding exceptions — add try/catch with a user-visible error message instead of crashing. 2. Check the CSV import routine: ensure it normalizes imported text to UTF-8 (or the app's internal encoding) at import time, replacing or flagging characters that cannot be represented. 3. Add a fallback encoding handler in the save path so malformed characters are replaced (e.g., with '?') rather than causing a crash.","proposed_test_case":"Create a CSV file containing tasks with: (a) UTF-8 accented characters, (b) Latin-1/Windows-1252 smart quotes, (c) a BOM marker, (d) null bytes. Import each and verify that save completes without crashing. Verify that any lossy character replacement is surfaced to the user as a warning, not a crash.","information_gaps":["Exact encoding of the reporter's source CSV file (UTF-8, Latin-1, Windows-1252, etc.)","Exact error message from the flashed dialog — could confirm the specific encoding exception","Operating system and app version","Whether deleting the imported tasks restores the ability to save (would confirm the data is the trigger vs. a count/size issue)"],"confidence":{"root_cause":"medium","reproduction":"medium","severity_assessment":"high"}}

---
