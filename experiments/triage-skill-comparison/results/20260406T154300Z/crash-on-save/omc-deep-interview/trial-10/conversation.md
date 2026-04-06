# Conversation: crash-on-save x omc-deep-interview

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — losing work is definitely frustrating, and we want to get this sorted out.

To help us track down the issue: when the app crashes, what exactly do you see? For example, does the app close suddenly without warning, does it freeze and become unresponsive, or do you get an error message or dialog? If there's an error message, could you share the exact text?

Also, could you let us know what version of TaskFlow you're running and what operating system you're on?

---

### reporter

I'm on TaskFlow v2.3.1, macOS 14.2. When I hit Save, there's a quick flash of a dialog box — it goes by really fast but I think it says something about 'encoding' — and then the app just closes completely. No freeze, it just vanishes. I lose whatever I hadn't saved before that point, which is super annoying.

---

### triage-agent

That's really helpful detail, thank you — the 'encoding' flash gives us a good lead.

Does this crash happen every time you save, or only with certain tasks/documents? For example, if you create a brand new task with just a few plain English words and try to save it, does it still crash? I'm trying to figure out whether something specific in your content (like special characters, emoji, or pasted text from another app) might be triggering the encoding error.

---

### reporter

Good question — actually now that you mention it, it started after I imported a bunch of tasks from a CSV file. That CSV had some fancy characters in it, like em-dashes and curly quotes. If I create a new small list from scratch, saving works fine. But my main list has about 200 tasks in it and that's the one that crashes. I actually tried removing the imported tasks and the crash stopped, so it definitely seems related to that CSV data.

---

### triage-agent

[RESOLVED] {"title":"(forced - parse error)","problem":"Agent could not produce JSON","root_cause_hypothesis":"unknown","reproduction_steps":[],"environment":"unknown","severity":"medium","impact":"unknown","recommended_fix":"Manual triage required","proposed_test_case":"N/A","information_gaps":["All"]}

---

