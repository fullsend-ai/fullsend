# Conversation: crash-on-save x socratic-refinement

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I can see how frustrating it must be to lose your work repeatedly.

To help us dig into what's going on, could you walk me through what you were doing right before the crash? Specifically, what were you working on in TaskFlow when you hit save — for example, were you editing a task, updating a project, or something else? And when you say the app "crashes," what exactly do you see — does it freeze, close entirely, show an error message, or something else?

---

### reporter

I'm just editing my task list and hitting Save. That's it. Nothing fancy. The app just closes on me — like it fully quits. I think there's some kind of error dialog that flashes for a split second but it disappears too fast to read. Look, I've been using this app for months and it was fine until recently. Can you just fix the save function?

---

### triage-agent

That's really helpful context — knowing it worked fine for months and only recently started crashing narrows things down a lot.

Can you think back to roughly when the crashes started? For instance, did it begin after an app update, or after you changed something about your setup — like a new OS update, a bigger task list, or anything like that?

Also, regarding that error dialog that flashes — if it happens again, could you try taking a quick screenshot or screen recording? On most systems you can hit Print Screen right as it appears. That message would be incredibly useful for tracking this down.

---

### reporter

I don't know, I didn't update anything. I guess... maybe it started around the time I imported a bunch of tasks from a spreadsheet? I had a CSV file with like 200 tasks I brought in. But that import worked fine, it's the SAVING that's broken. And I'm not going to sit there trying to screenshot something that flashes for half a second, come on. It says something about 'encoding' maybe? I don't know. The point is the app just dies when I hit Save.

---

### triage-agent

That detail about the CSV import is actually really interesting — even though the import itself worked, the save process handles data differently than the import does, so it's worth exploring that thread.

Here's what I'm curious about: does the crash happen every single time you save, or could you test something for me? If you create a brand new task (just a simple one-word title, nothing from the spreadsheet) and save *only* that, does it still crash? Basically I'm wondering whether the crash is tied to the data that came in from that CSV, or whether saving is broken across the board regardless of what's in your task list.

---

### reporter

I'm not going to start deleting my tasks just to test your theory. I have 200 tasks in there that I need. But... fine, yeah, I guess when I had fewer tasks in there before the import it was saving fine. And if I think about it, the crash is only when I click Save in the toolbar. The auto-save thing seems to work? I don't know why that would matter. Can you just look at the save code or whatever?

---

### triage-agent

That distinction between toolbar Save crashing and auto-save working is actually a really important clue — thank you for noticing that. It tells us the two features probably handle your data differently under the hood, which gives developers a specific place to look.

One more thing that would help me connect the dots: do you remember anything about the content of that CSV you imported? For instance, did any of your 200 tasks have special characters, accented letters, emoji, or non-English text in them? Or were they copied from another app that might format text in an unusual way (like smart quotes or special dashes from Word/Google Docs)? That 'encoding' error you glimpsed makes me think the save function might be tripping over a specific character in that imported data.

---

### reporter

Yeah, the spreadsheet was originally from some Word doc or something, so it probably has those fancy quotes and dashes and whatever. I didn't really pay attention to that stuff. Look, I just need to save my tasks. Are you saying the app can't handle normal characters? That seems like a pretty basic thing to get right.

---

### triage-agent

[RESOLVED] {"title":"Toolbar Save crashes with encoding error when task list contains non-ASCII characters from CSV import","problem":"The application force-quits when the user clicks Save in the toolbar. A brief error dialog mentioning 'encoding' flashes before the app closes. Auto-save continues to work correctly. The issue began after the user imported ~200 tasks from a CSV file that originated from a Microsoft Word document, likely containing smart quotes, em-dashes, and other extended characters.","root_cause_hypothesis":"The toolbar Save code path uses a different serialization or encoding method than auto-save. It likely writes with a strict encoding (e.g., ASCII) or fails to handle non-ASCII characters (smart quotes U+2018/U+2019, em-dashes U+2014, etc.), causing an unhandled encoding exception that crashes the app. Auto-save probably uses a more permissive encoding (e.g., UTF-8) or a different serialization library that handles these characters gracefully.","reproduction_steps":["Create or import a task with smart quotes (e.g., ‘example’) or em-dashes (—) in the title or description","Click the Save button in the toolbar","Observe the app crash with a brief encoding error dialog"],"environment":"User has been using the app for several months. Recently imported ~200 tasks from a CSV file originally created from a Word document. Specific OS and app version not confirmed.","severity":"high","impact":"User cannot manually save their work at all. The toolbar Save button is completely non-functional with their current dataset. Data loss risk is mitigated only by auto-save working, but user has no way to force a save.","recommended_fix":"Compare the encoding and serialization logic between the toolbar Save handler and the auto-save handler. The toolbar Save path likely needs to use UTF-8 encoding (or match whatever encoding auto-save uses). Additionally, the save function should catch encoding errors gracefully rather than crashing the app — surface a user-readable error identifying the problematic content instead of force-quitting.","proposed_test_case":"Create a task list containing various non-ASCII characters (smart quotes, em-dashes, accented characters, emoji) and verify that toolbar Save completes without error. Include a regression test that saves a task with content copied from a Word document and asserts no encoding exception is thrown.","information_gaps":["Exact OS and app version","Exact text of the flashing error dialog","Whether the issue reproduces with a single task containing special characters (isolated from the 200-task dataset)"]}

---
