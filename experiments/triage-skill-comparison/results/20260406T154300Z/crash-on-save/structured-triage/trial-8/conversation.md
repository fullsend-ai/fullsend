# Conversation: crash-on-save x structured-triage

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I can see how frustrating it must be to lose your work.

To help us track this down, could you describe what exactly happens when the app crashes? For example:
- Does the app freeze and become unresponsive, close/disappear entirely, or show an error message or dialog?
- If you see any error text or a crash screen, could you share what it says (a screenshot works too)?

---

### reporter

The app just closes entirely — it disappears. But right before it does, I see a dialog flash very briefly that mentions something about 'encoding.' It's too fast to read the whole thing, maybe on screen for less than a second before everything just shuts down. No freeze or anything like that, it just goes poof.

---

### triage-agent

Thanks for that detail — the brief 'encoding' dialog is a helpful clue.

Could you walk me through the exact steps you take before the crash? Specifically:
- What screen or section of TaskFlow are you in when you hit save? (e.g., editing a task, creating a new task, updating project settings)
- What kind of content are you entering or changing right before you save? (e.g., task description with special characters, pasted text, an attachment)

This will help us figure out exactly where and why the crash is happening.

---

### reporter

I'm editing a task list — it's a pretty big one, around 200 tasks. I'm not creating anything new, just working with existing tasks. When I click 'Save' in the toolbar, that's when it crashes. The thing is, a lot of these tasks were imported from a CSV file, and some of them have special characters like em-dashes and curly quotes in the task names. I didn't think much of it at the time, but the crashing started after I did that import. If I remove those imported tasks, saving works fine again.

---

### triage-agent

Thanks — that's really useful context. The CSV import with special characters like em-dashes and curly quotes is a strong lead.

Could you share a few details about your environment?
- What operating system are you on? (e.g., Windows 11, macOS Sonoma, Ubuntu 24.04)
- Which browser are you using, and roughly what version? (e.g., Chrome 124, Firefox 126)
- Do you know which version of TaskFlow you're running? (You can usually find this under Help → About or in the app footer.)

---

### reporter

I'm on macOS 14.2 (Sonoma). TaskFlow is a desktop app, not a browser — version 2.3.1. I checked under Help → About.

---

### triage-agent

[RESOLVED] {"title":"(forced - parse error)","problem":"Agent could not produce JSON","root_cause_hypothesis":"unknown","reproduction_steps":[],"environment":"unknown","severity":"medium","impact":"unknown","recommended_fix":"Manual triage required","proposed_test_case":"N/A","information_gaps":["All"]}

---

