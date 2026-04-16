# Conversation: crash-on-save x omc-deep-interview

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I can see how frustrating it must be to lose your work. To help us track this down, could you tell us a bit more about what's happening when it crashes? Specifically: what are you working on when you hit save (e.g., creating a new task, editing an existing one, updating a description), and does the app freeze, show an error message, or just close entirely?

---

### reporter

It just closes. Like, I'm editing my task list and I click Save and the whole thing just disappears. I think there's some kind of error that flashes for a split second but I can't read it. I have a pretty big task list, like 200 tasks or so. Can you just fix the save button? It's been working fine before.

---

### triage-agent

That's really helpful, thank you. The fact that it was working fine before and you have around 200 tasks makes me wonder — did this start happening after your task list grew to a certain size, or did it start suddenly regardless of how many tasks you had? Also, if you create a brand new, small task list with just a few items, does saving that one work fine?

---

### reporter

I don't know, I didn't really keep track of when exactly it started. I think smaller lists work fine but honestly I don't remember. I did import a bunch of tasks from a CSV file a while back and I feel like it might have started around then but I'm not sure. Can you just look into the save function? It's broken.

---

### triage-agent

That detail about the CSV import is really useful — it could be a key clue. Could you try something quick for me? If you duplicate your task list (or export it) as a backup, then delete a handful of the tasks you imported from CSV and try saving, I'm curious whether it still crashes. Also, that error message that flashes by could tell us a lot — if you're able to try saving one more time and either take a quick screen recording or screenshot (even a phone camera pointed at the screen would work), that would help us narrow this down much faster.

---

### reporter

Look, I'm not going to sit here deleting tasks one by one to debug your app for you. But fine, I did actually try removing the stuff I imported from that CSV and the save worked after that. So yeah it's something with the imported tasks. I don't know about a screen recording, I'm not set up for that. The error flashes way too fast to even read but I think it said something about encoding or whatever. Just fix the CSV import or the save, I don't care which, I just need it to work.

---

### triage-agent

Totally fair — you shouldn't have to debug this, and what you've already found is genuinely helpful. One last thing that would let us reproduce this on our end without any more back-and-forth from you: could you attach the CSV file you originally imported (or even just a few rows from it)? That way we can reproduce the crash directly and get it fixed. If the data is sensitive, even a dummy CSV with the same column headers and any non-English characters or special characters it might contain would work.

---

### reporter

I don't have the CSV anymore, I deleted it after importing. The data had task names with fancy punctuation stuff — like those curly quote marks and long dashes instead of regular ones. I copied it from a Word document into the CSV. I don't remember the exact columns but it was just task names and due dates, nothing crazy. Can you just handle special characters properly? That seems like it should work.

---

### triage-agent

[RESOLVED] {"title":"App crashes on save when task list contains CSV-imported tasks with Unicode smart punctuation (curly quotes, em dashes)","problem":"The application abruptly closes when the user saves a task list that contains tasks imported from a CSV file. The CSV data was copy-pasted from a Word document and contains Unicode smart punctuation characters (curly/typographic quotes and em dashes). A brief error message referencing 'encoding' flashes before the app closes. Removing the imported tasks restores normal save behavior.","root_cause_hypothesis":"The save/serialization path does not handle non-ASCII Unicode characters (specifically Windows-1252 or UTF-8 smart punctuation like U+201C/U+201D curly quotes and U+2014 em dash). The data likely passes through a code path that assumes ASCII or a narrow encoding, causing an unhandled encoding exception that crashes the application instead of being caught and surfaced to the user.","reproduction_steps":["Create a CSV file with two columns: task name and due date","Include task names containing Unicode smart punctuation — e.g., curly quotes (“ ”), em dashes (—), and other typographic characters typically inserted by Word","Include enough rows to approximate a realistic list (50+ should suffice, reporter had ~200)","Import the CSV into TaskFlow","Click Save","Observe: app closes abruptly with a brief encoding-related error flash"],"environment":"Not specified (reporter did not provide OS, app version, or browser). Likely desktop application given the 'app closes entirely' behavior rather than a browser tab crash.","severity":"high","impact":"Users who import CSV data containing non-ASCII characters (common when copy-pasting from Word or other rich-text sources) cannot save their task lists, resulting in data loss. Workaround exists (remove imported tasks) but is impractical for users who need that data.","recommended_fix":"1. Investigate the save/serialization code path for encoding assumptions — ensure it handles UTF-8 throughout. 2. Check the CSV import path: either sanitize/normalize smart punctuation on import (e.g., replace curly quotes with straight quotes) or ensure the data store and serializer support the full Unicode range. 3. Wrap the save operation in proper error handling so encoding failures surface a user-visible error message instead of crashing the app. 4. Consider adding a CSV import validation step that warns about potentially problematic characters.","proposed_test_case":"Import a CSV containing task names with Unicode smart punctuation (curly quotes U+201C/U+201D, em dash U+2014, ellipsis U+2026) and verify that: (a) the import succeeds, (b) the task list saves without error, and (c) the characters round-trip correctly when the list is reopened. Additionally, test that a save failure due to encoding produces a user-visible error rather than a crash.","information_gaps":["Exact error message (reporter could not read it; logs or a debugger would reveal this)","Application version and platform/OS","Whether the issue is specific to CSV import or would also occur if the same characters were typed directly into a task name","Original CSV file (deleted by reporter; reproduction CSV must be constructed from description)"]}

---
