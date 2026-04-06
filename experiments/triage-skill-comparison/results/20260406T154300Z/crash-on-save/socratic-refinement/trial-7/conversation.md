# Conversation: crash-on-save x socratic-refinement

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I can see how frustrating it must be to lose your work repeatedly.

To help us get to the bottom of this, could you walk me through what you were doing right before the crash? Specifically, what kind of task or document were you working on, and what did you click or do to trigger the save? And when the crash happens, what exactly do you see — does the app freeze, close entirely, or show an error message?

---

### reporter

I've got a task list with about 200 tasks that I've been working on. When I click the 'Save' button in the toolbar, the app just closes on me — no freeze or anything, it just disappears. Right before it closes I can see a dialog flash for a split second that mentions something about 'encoding' but it's gone too fast to read the whole thing. This started happening after I imported a bunch of tasks from a CSV file.

---

### triage-agent

[RESOLVED] {"title":"App crashes on save with encoding error after CSV task import","problem":"After importing tasks from a CSV file, clicking the Save button causes the application to immediately close. A dialog referencing 'encoding' flashes briefly before the app disappears. The user has approximately 200 tasks in the list. No freeze or gradual degradation — the app simply terminates.","root_cause_hypothesis":"The CSV import likely introduced task data containing characters in an encoding (e.g., Latin-1, Windows-1252) that differs from the application's expected encoding (likely UTF-8). When the save routine attempts to serialize the task data, it encounters characters it cannot encode, throws an unhandled exception, and the application crashes. The briefly-visible dialog is likely the unhandled exception or encoding error surfacing before the process terminates.","reproduction_steps":["Create or obtain a CSV file with tasks containing non-ASCII or mixed-encoding characters (e.g., accented characters, smart quotes, em-dashes)","Import the CSV file into TaskFlow","Click the Save button in the toolbar","Observe the app crashes with a brief encoding-related dialog"],"environment":"Not specified — likely desktop application. OS and version not yet confirmed.","severity":"high","impact":"The user cannot save their work at all after importing CSV data, leading to repeated data loss. Any user who imports CSV files with non-ASCII characters is likely affected. This is a data-loss scenario.","recommended_fix":"Investigate the save/serialization code path for encoding assumptions. Ensure imported CSV data is normalized to a consistent encoding (UTF-8) at import time, or that the save routine handles mixed encodings gracefully. The unhandled exception should be caught and surfaced as a user-visible error rather than crashing the app. Specifically: (1) Add encoding detection/normalization in the CSV import pipeline, (2) Wrap the save serialization in proper error handling, (3) If encoding fails, show a meaningful error identifying which task(s) contain problematic characters.","proposed_test_case":"Import a CSV file containing tasks with various non-ASCII characters (accented letters, CJK characters, smart quotes, emoji) and verify that saving completes successfully without crashing. Additionally, test with a CSV file explicitly saved in Latin-1 or Windows-1252 encoding to confirm the import normalizes encoding correctly.","information_gaps":["Exact OS and application version","The specific encoding of the source CSV file","Whether the crash occurs with a smaller subset of the imported tasks","Full text of the encoding error dialog"]}

---

