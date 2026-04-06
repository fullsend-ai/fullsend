# Conversation: crash-on-save x omc-deep-interview

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — sorry you're losing work, that's really frustrating.

Could you help me understand what exactly happens when it crashes? For example:
- Do you see an error message or dialog before it closes, or does the app just disappear?
- What kind of task/document are you working on when you hit save (and roughly how large is it)?
- What version of TaskFlow are you on, and what OS are you running it on?

This will help us narrow down the cause quickly.

---

### reporter

So when I hit Save, there's a quick flash of some error dialog — I can barely read it but I think it says something about 'encoding' — and then the whole app just closes. No crash report or anything, it just disappears.

I'm working on a task list with about 200 tasks. I'm on TaskFlow v2.3.1, macOS 14.2.

One thing that might be relevant — this started happening after I imported a bunch of tasks from a CSV file. Before that import, saving worked fine.

---

### triage-agent

That's really helpful — the CSV import timing is a strong clue, especially combined with the 'encoding' error flash.

Two quick things that would help us pin this down:
1. Did the CSV file contain any non-English text, special characters, or emoji? (Even things like accented names, curly quotes from Word/Google Docs, or unusual symbols would count.)
2. If you create a brand-new task list with just a couple of tasks (no import), does saving that one work fine?

This will help us confirm whether the imported data itself is the trigger.

---

### reporter

Yeah, the CSV had a bunch of special characters — things like em-dashes and curly quotes that probably came from copying out of a Word doc originally. Nothing exotic like emoji or foreign languages, just those fancy punctuation marks.

And yes, if I make a new small task list, saving works totally fine. Even if I keep some of the imported tasks in a smaller list (like under 50 or so), it seems to save okay. It's specifically my big list with all ~200 tasks that crashes.

---

### triage-agent

That's really narrowing it down, thanks.

One more thing that would help us tell whether it's the special characters or the sheer size causing this: if you duplicate your big list and then find-and-replace the curly quotes and em-dashes with plain equivalents (straight quotes and regular hyphens), does saving that cleaned-up 200-task list work?

Alternatively, if that's too much effort — have you ever had a 200+ task list that saved fine *before* the CSV import days? That would tell us whether TaskFlow has ever handled that size without issues on your setup.

---

### reporter

I haven't tried replacing all the special characters myself — that sounds like a lot of work across 200 tasks. But I can tell you that before I did the CSV import, I had about 180 tasks in this same list and saving worked perfectly fine. So it's not just the size. It really seems like it's the combination of having a big list AND those special characters from the import. Actually, I did try removing the imported tasks as a test, and saving worked again with my original tasks. So yeah, it's definitely something about that imported data.

---

### triage-agent

[RESOLVED] {"title":"App crashes on save when task list contains special characters imported from CSV","problem":"TaskFlow v2.3.1 on macOS 14.2 crashes when saving a task list that contains special characters (em-dashes, curly quotes) introduced via CSV import. The app briefly flashes an error dialog mentioning 'encoding' before closing without a crash report. The user loses unsaved work each time.","root_cause_hypothesis":"The save/serialization path likely fails on non-ASCII punctuation characters (smart quotes, em-dashes) that were imported from a Word-originated CSV. The encoding error suggests the save routine assumes ASCII or a specific encoding and chokes on these characters. The crash instead of graceful error handling suggests an unhandled exception in the serialization or file-write layer. The size correlation (works under ~50 imported tasks, fails at ~200) may indicate a buffer or memory interaction, or simply that fewer tasks means lower probability of hitting a problem character.","reproduction_steps":["Install TaskFlow v2.3.1 on macOS 14.2","Create a CSV file containing tasks with Word-style special characters: curly/smart quotes (“ ” ‘ ’) and em-dashes (—)","Create a new task list in TaskFlow","Import the CSV file (aim for ~200 tasks to match reporter's scenario)","Attempt to save the task list","Observe: brief encoding error dialog flashes, then app closes/crashes"],"environment":"TaskFlow v2.3.1, macOS 14.2 (Sonoma)","severity":"high","impact":"Any user who imports CSV data containing non-ASCII punctuation (common when data originates from Word or Google Docs) risks a crash on save with total loss of unsaved work. This is a data-loss scenario with no graceful fallback.","recommended_fix":"Investigate the save/serialization code path for encoding assumptions. Likely fix: ensure the file writer uses UTF-8 encoding throughout. Add proper error handling around the save operation so encoding failures surface as a user-facing error message rather than crashing the app. Also review the CSV import path — it should either normalize special characters on import or ensure they are stored in a save-compatible encoding from the start.","proposed_test_case":"Create a task list containing tasks with em-dashes, curly quotes, and other common non-ASCII punctuation. Verify that saving succeeds without error. Test at various list sizes (50, 200, 500 tasks) to confirm no size-dependent regression. Also verify that the save operation fails gracefully (error message, no data loss) if an encoding issue is deliberately introduced.","information_gaps":["Exact error message in the flashing dialog (reporter could not read it fully)","Whether the issue reproduces on other OSes (Windows, Linux)","Whether specific characters are the trigger vs. any non-ASCII character","Whether the crash produces any log output in Console.app or system logs"],"clarity_scores":{"symptom":0.85,"cause":0.85,"reproduction":0.80,"impact":0.75,"overall_ambiguity":0.175}}

---

