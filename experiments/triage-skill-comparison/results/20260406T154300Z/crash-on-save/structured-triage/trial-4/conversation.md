# Conversation: crash-on-save x structured-triage

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I can see how frustrating it must be to lose your work.

To help us track down the cause, could you walk us through the exact steps you take before the crash? For example:

1. What do you do to get to the point of saving? (e.g., create a new task, edit an existing one, etc.)
2. How do you trigger the save — clicking a button, using a keyboard shortcut, or something else?
3. Does the crash happen with any task, or only with certain kinds of content?

The more specific you can be, the faster we can reproduce and fix this.

---

### reporter

Sure, here's what I do: I have a task list with about 200 tasks that I've been working on. I edit tasks normally — updating names, checking things off, etc. When I click the 'Save' button in the toolbar, the app just crashes. I see a quick flash of some dialog box that mentions something about 'encoding' but it disappears too fast to read the whole thing.

It seems to only happen with this particular list. I recently imported a bunch of tasks from a CSV file, and I think the crashing started after that. The imported tasks have some fancy characters in them — like em-dashes and curly quotes. If I make a smaller list with just a few tasks, saving works fine.

---

### triage-agent

Thanks for those details — that's really helpful. The CSV import with special characters like em-dashes and curly quotes is a strong lead.

Could you let us know what environment you're running the app in? Specifically:

- **Operating system** (e.g., Windows 11, macOS Sonoma, Ubuntu 24.04)
- **Browser and version** (e.g., Chrome 124, Firefox 126) — or is this a desktop app?
- **TaskFlow version** (you can usually find this under Help → About or in Settings)

Encoding issues can behave differently across platforms, so this will help us reproduce the crash on our end.

---

### reporter

I'm on macOS 14.2 (Sonoma). TaskFlow is a desktop app, not browser-based — version 2.3.1. Let me know if you need anything else!

---

### triage-agent

[RESOLVED] {"title":"(forced - parse error)","problem":"Agent could not produce JSON","root_cause_hypothesis":"unknown","reproduction_steps":[],"environment":"unknown","severity":"medium","impact":"unknown","recommended_fix":"Manual triage required","proposed_test_case":"N/A","information_gaps":["All"]}

---
