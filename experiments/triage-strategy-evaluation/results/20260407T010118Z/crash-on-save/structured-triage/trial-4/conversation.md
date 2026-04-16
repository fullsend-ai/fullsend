# Conversation: crash-on-save x structured-triage

## Issue

**app crashes when I save**

The app keeps crashing whenever I try to save. This is really frustrating because I keep losing my work. Please fix this ASAP.

---

## Comments

### triage-agent

Thanks for reporting this — I can see how frustrating it must be to lose your work. I'd like to help us get to the bottom of this.

Could you walk us through the exact steps that lead to the crash? For example:
- Where in the app are you when you click save? (e.g., editing a task, creating a new task, updating project settings)
- What kind of content are you saving? (e.g., a task with a title and description, an attachment, etc.)
- Does the crash happen immediately when you click save, or after a brief delay?

---

### reporter

I'm editing my task list, it's got a bunch of tasks in it. When I hit Save in the toolbar it just crashes. Like instantly, the whole app just closes. I don't know what else to tell you, it just doesn't work. I need this fixed because I have a deadline coming up.

---

### triage-agent

Thanks for those details — that helps narrow things down. So we can try to reproduce this on our end, could you let us know what environment you're using?

Specifically:
- **Platform:** Are you using the desktop app, web app, or mobile app?
- **OS:** What operating system are you on? (e.g., Windows 11, macOS Sonoma, etc.)
- **App version:** If you're on the desktop app, the version number is usually under Help → About. For the web app, the browser and version would be helpful (e.g., Chrome 124).

This will help us figure out whether the crash is tied to a specific platform.

---

### reporter

It's the desktop app on Mac. I don't remember the exact version, you can look that up can't you? I just need this fixed, I've been dealing with this for days now.

---

### triage-agent

Totally understand the urgency — we want to get this sorted for you.

One thing that would really help us reproduce the crash: **which version of macOS are you running?** You can check under the Apple menu → About This Mac.

Also, when the app crashes, macOS usually shows a crash report dialog — if you see one next time it happens, could you copy and paste the first ~20 lines? That often contains the app version too, plus clues about what triggered the crash.

---

### reporter

I think it's macOS 14 something, Sonoma maybe? I don't get a crash report dialog, the app just closes. Actually now that I think about it there's like a quick flash of some error message right before it dies but it goes away too fast to read. I think it said something about encoding? I don't know. Look, can someone just look into this? It was working fine before.

---

### triage-agent

Thanks for sticking with us on this — I know it's been a frustrating few days.

That flash about "encoding" is actually a really useful clue. Even though the crash dialog didn't appear, macOS keeps a log of it. Could you try this?

1. Open **Console.app** (you can find it via Spotlight — just press ⌘+Space and type "Console")
2. In the search bar at the top right, type **TaskFlow**
3. Look for recent entries around the time of a crash — they'll likely be in red or marked as "error"
4. If you see anything, copy and paste the first few lines here

Alternatively, if that feels like too much, could you try one more save and watch for that error flash — even a partial word or two beyond "encoding" would help us pinpoint whether this is a data issue (like a special character in one of your tasks) or a deeper app bug.

---

### reporter

I'm not going to dig through Console.app, I don't even know what that is. Look, I think the encoding thing might have something to do with a CSV file I imported a while back? I had a bunch of tasks in a spreadsheet and imported them into TaskFlow and that's around when this started happening. But I shouldn't have to debug this myself, that's your job. The import worked fine, it's the saving that's broken.

---

### triage-agent

[RESOLVED] {"title":"Desktop app crashes instantly on save after CSV task import (possible encoding issue)","problem":"The desktop app on macOS crashes immediately when the user clicks Save in the toolbar while editing a task list. The user loses unsaved work each time. The crash began after the user imported tasks from a CSV file. A brief error flash mentioning 'encoding' appears before the app closes.","root_cause_hypothesis":"The CSV import likely introduced task data containing characters in an encoding the app's save/serialization logic cannot handle (e.g., non-UTF-8 characters, malformed Unicode sequences, or BOM markers). When the save routine attempts to serialize the task list, it hits an unhandled encoding exception that crashes the process instead of being caught gracefully.","reproduction_steps":["Import a CSV file containing tasks into TaskFlow (file likely contains non-UTF-8 or mixed-encoding characters)","Open the imported task list for editing","Click Save in the toolbar","App crashes instantly — no crash dialog, brief flash of encoding-related error"],"environment":"Desktop app on macOS 14 (Sonoma). Exact app version and macOS patch version unknown.","severity":"high","impact":"User is completely blocked from saving any work in the affected task list. Data loss occurs on every save attempt. User has an upcoming deadline and has been experiencing this for multiple days.","recommended_fix":"1. Investigate the CSV import path for encoding handling — check whether imported data is normalized to UTF-8. 2. Check the save/serialization code for unhandled encoding exceptions — the crash suggests an uncaught exception rather than a graceful error. 3. Add a try/catch around the save serialization to surface the error to the user instead of crashing. 4. If the root cause is malformed data already in the user's task list, provide a migration or sanitization pass to fix existing data.","proposed_test_case":"Import a CSV file containing mixed encodings (e.g., Latin-1 characters, BOM markers, invalid UTF-8 byte sequences) into a task list, then attempt to save. Verify the app either saves successfully (after normalizing encoding) or displays a user-facing error message without crashing.","information_gaps":["Exact app version","Exact macOS version (only know '14 something')","Contents or encoding of the original CSV file","Crash log from Console.app or macOS crash reporter","Full text of the encoding error message","Whether the crash occurs only on the imported task list or on all task lists"]}

---
