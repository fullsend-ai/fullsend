# Conversation: crash-on-save x superpowers-brainstorming

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — losing work to crashes is definitely frustrating, and we want to get this sorted out.

To help us zero in on the problem, could you tell us which save action is causing the crash?

1. Saving a new task (creating one for the first time)
2. Editing and saving an existing task
3. Saving changes to a project or board
4. Something else (please describe)

Also, do you see any error message or dialog before the crash, or does the app just disappear?

---

### reporter

It's option 2 — I'm editing and saving an existing task list. Specifically, I click the 'Save' button in the toolbar and the app just dies. There IS a brief error dialog that flashes for like a split second before it closes — I can barely read it but it says something about 'encoding'. The weird thing is auto-save doesn't seem to cause the crash, only when I manually hit Save.

---

### triage-agent

That 'encoding' clue is really helpful — thanks for catching it even though it flashed by quickly.

One more question that'll help us nail this down: does the crash happen with **any** task list you manually save, or only with specific ones?

1. It crashes on **every** task list when I hit Save
2. It only crashes on **certain** task lists — others save fine
3. It started happening on a task list that **used to save fine** (something changed)
4. I'm not sure — I've only tried with one task list

If it's option 2 or 3, does the affected task list contain anything unusual — like emoji, accented characters, pasted content from another app, or very long text?

---

### reporter

It's kind of a mix of 2 and 3. I have one main task list with about 200 tasks, and that's the one that crashes. Smaller lists save fine. The thing that changed is I imported a bunch of tasks from a CSV file a few days ago — ever since then, the Save button crashes the app on that list. Now that you mention it, the CSV had some fancy characters in it like curly quotes and em-dashes from a Word document. If I delete the imported tasks, the save works again. So yeah, it's definitely related to that CSV import.

---

### triage-agent

[RESOLVED] {"title":"Manual save crashes on task lists containing special Unicode characters imported via CSV","problem":"When a user manually saves a task list (via the toolbar Save button) that contains tasks imported from a CSV file with special Unicode characters (curly quotes, em-dashes, etc. originating from a Word document), the app crashes. A brief error dialog referencing 'encoding' flashes before the app closes. Auto-save does not trigger the crash, indicating the manual save path uses a different serialization or encoding method that cannot handle these characters.","root_cause_hypothesis":"The manual save code path likely uses a different text encoding or serialization method than auto-save. The manual save probably attempts to encode content as ASCII or a restrictive encoding (e.g., latin-1) rather than UTF-8, causing it to choke on Unicode characters like curly quotes (U+2018/U+2019), em-dashes (U+2014), and similar characters introduced by the CSV import from a Word document. Auto-save likely uses a different writer or encoding setting that handles UTF-8 correctly.","reproduction_steps":["Create a CSV file containing tasks with special Unicode characters (curly quotes, em-dashes — characters typically produced by Microsoft Word)","Import the CSV into TaskFlow as a new task list or into an existing one","Click the 'Save' button in the toolbar","Observe: an error dialog briefly flashes mentioning 'encoding', then the app crashes"],"environment":"Not platform-specific based on available information. Triggered by content (Word-originated Unicode characters in CSV imports), not by OS or version.","severity":"high","impact":"Any user who imports CSV data containing non-ASCII characters (very common when data originates from Word or other rich-text tools) will experience crashes on manual save, leading to data loss anxiety and inability to reliably save work. The workaround (deleting imported tasks) is destructive.","recommended_fix":"Investigate the manual save code path and compare its encoding/serialization logic to the auto-save path. Ensure the manual save writer uses UTF-8 encoding consistently. Specifically: (1) find where the toolbar Save handler serializes task list content, (2) check for hardcoded encoding assumptions (ASCII, latin-1, etc.), (3) align it with the auto-save encoding which already handles Unicode correctly. Also review the CSV import path to ensure it preserves encoding metadata so downstream writers know to expect Unicode.","proposed_test_case":"Create a task list containing text with curly quotes (‘’“”), em-dashes (—), and other common Unicode characters. Perform a manual save via the toolbar button. Verify the save completes without error and that the file can be re-opened with all characters intact. Additionally, test with a CSV import containing these characters followed by a manual save.","information_gaps":["Exact error message in the crash dialog (reporter could only partially read it)","Whether the issue affects all platforms or is platform-specific","The specific file format used by the manual save path (JSON, XML, custom binary, etc.)"]}

---

