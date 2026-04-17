# Conversation: crash-on-save x omc-deep-interview

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Sorry you're running into this — that sounds really disruptive. To help us track this down, could you describe exactly what happens when you try to save? For example: does the app freeze and then close, does it show an error message or dialog before crashing, or does it just disappear instantly? And what are you saving when it happens — a specific type of task, a large project, or does it happen on any save at all?

---

### reporter

When I click Save in the toolbar, I see a dialog flash really quickly — it says something about 'encoding' I think — and then the whole app just closes. It's not a freeze, it happens pretty fast. This started after I imported a bunch of tasks from a CSV file. I have about 200 tasks in the list now. The weird thing is auto-save seems to work fine, it's only when I manually click Save that it crashes.

---

### triage-agent

Thanks for those details — the encoding dialog and the CSV import connection are really helpful clues. One more thing that would help us nail this down: does your CSV data contain any special characters, non-English text, or symbols (like accented letters, emoji, or characters copied from another app like Word)? Also, if you create a brand-new project with just one or two manually-typed tasks and hit Save, does that work fine — or does the crash happen there too?

---

### reporter

Yeah, the CSV had a bunch of tasks that were copied from a Word document originally, so there are curly quotes and em-dashes in some of the task names. And yes, if I create a new project with just a few tasks and save, it works perfectly fine. Even in my current project, if I delete the imported tasks and get the list down to a smaller size, saving works again. It really seems like it's something about those imported tasks causing the problem.

---

### triage-agent

[RESOLVED] {"title":"Manual save crashes with encoding error when project contains non-ASCII characters imported from CSV","problem":"Clicking the Save button causes the app to display a brief encoding-related dialog and then crash (immediate close, not a freeze). This only affects manual save — auto-save works correctly. The issue is triggered by tasks imported from a CSV file that contained special Unicode characters (curly quotes, em-dashes) originating from a Word document.","root_cause_hypothesis":"The manual save code path likely uses a different text encoding or serialization method than auto-save. When it encounters non-ASCII characters (smart/curly quotes, em-dashes, and potentially other Windows-1252 or Unicode characters copied from Word), it hits an unhandled encoding error that causes the application to crash. The brief 'encoding' dialog suggests the error is partially caught but not gracefully handled. Auto-save likely uses a different serializer or encoding setting that handles these characters correctly.","reproduction_steps":["Create a CSV file containing task names with curly quotes (\\u2018, \\u2019, \\u201C, \\u201D) and em-dashes (\\u2014) — copying text from a Word document is an easy way to get these characters","Import the CSV into TaskFlow (import enough tasks to have a sizable project, ~200 tasks were reported)","Click the Save button in the toolbar","Observe: a dialog mentioning 'encoding' flashes briefly, then the app closes/crashes"],"environment":"Not specified (likely desktop app). Issue is content-dependent rather than platform-dependent — any environment importing CSV with non-ASCII characters should reproduce it.","severity":"high","impact":"Any user who imports tasks from CSV files containing non-ASCII characters (common when data originates from Word or other rich-text sources) will be unable to manually save their project. While auto-save provides a partial workaround, users risk losing work if they rely on manual save, and the crash erodes trust in the application. This likely affects many users since CSV import from Word/Excel is a common workflow.","recommended_fix":"1. Investigate the manual save code path's text encoding — compare it to auto-save to identify the divergence. 2. Ensure manual save uses UTF-8 encoding (or matches whatever encoding auto-save uses successfully). 3. Add proper error handling around the encoding step so that even if an unexpected character is encountered, the app shows a meaningful error instead of crashing. 4. Consider normalizing or sanitizing imported text at CSV import time (e.g., replacing curly quotes with straight quotes, em-dashes with hyphens) as defense-in-depth.","proposed_test_case":"Create a project with tasks containing various non-ASCII characters (curly quotes, em-dashes, accented characters, emoji) imported via CSV. Verify that manual save completes without error and that the saved file correctly preserves the original characters when reloaded.","information_gaps":["Exact platform/OS version not confirmed (though likely not relevant to root cause)","Exact app version not provided","Full error message from the encoding dialog not captured (only partial recall)","Whether the issue also affects other special characters beyond curly quotes and em-dashes"],"clarity_scores":{"symptom":0.9,"cause":0.85,"reproduction":0.85,"impact":0.75,"overall_ambiguity":0.15}}

---
