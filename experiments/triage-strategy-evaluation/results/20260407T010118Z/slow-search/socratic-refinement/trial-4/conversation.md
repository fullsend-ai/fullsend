# Conversation: slow-search x socratic-refinement

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! I'd like to understand the context a bit better.

Can you walk me through what you were doing when you noticed the slowdown? For instance, what kind of search were you running — a simple keyword, a filtered search, or something else? And when you say it *used to be* fast, do you have a sense of when things changed? Was it after an update, after your task list grew past a certain point, or did it seem to happen suddenly one day?

---

### reporter

Yeah so I mostly search by typing keywords into the search bar and it looks through my task descriptions. I think the slowdown started maybe 2 weeks ago? I recently updated TaskFlow — I'm pretty sure I went from 2.2 to 2.3 around that time, so it might be related to that. I have about 5,000 tasks built up over a couple years so it's a decent amount of data, but it was fine before the update. The weird thing is if I search just by task title it's still quick, it's the full-text search through descriptions that's really slow.

---

### triage-agent

That's really helpful — the fact that title search is still fast but full-text description search is slow is a great clue, and the timing with the 2.3 update is suspicious.

You mentioned it *sometimes* takes 10-15 seconds. I'm curious about the pattern there — is there anything different about the times it's slow versus when it's faster? For example, is the first search after opening TaskFlow the worst one, or does it not matter? And when it's slow, what do you see — does the app freeze up entirely, do you get a spinner, or does it just sit there looking like nothing is happening?

---

### reporter

Hmm, I'm not sure there's a huge pattern to it honestly. It feels like it's pretty consistently slow whenever I search through descriptions. I don't think the first search is notably worse than later ones. When it's slow the app kind of freezes up — like the UI gets unresponsive for those 10-15 seconds. I can see my CPU fan spin up too, if that helps. I will say some of my tasks have really long descriptions — I paste meeting notes into them sometimes, so some are pretty beefy. Not sure if that matters.

---

### triage-agent

[RESOLVED] {"title":"Full-text description search freezes UI for 10-15s since v2.3 upgrade (title search unaffected)","problem":"After upgrading from TaskFlow 2.2 to 2.3 approximately two weeks ago, keyword searches that scan task descriptions take 10-15 seconds and freeze the UI entirely, with visible CPU spike. The same searches that only match against task titles remain fast. The user has ~5,000 tasks accumulated over two years, with some containing very large descriptions (pasted meeting notes).","root_cause_hypothesis":"The v2.3 update likely changed the full-text search implementation for task descriptions — most probably removed or broke a search index, switched from indexed/async search to brute-force synchronous scanning, or moved the search operation onto the main/UI thread. The CPU spike and UI freeze indicate a CPU-bound synchronous operation blocking the render loop while scanning large text fields across 5,000 records.","reproduction_steps":["Install TaskFlow 2.3","Have a dataset with ~5,000 tasks, including tasks with large description fields (multi-paragraph text)","Perform a keyword search using the search bar with full-text description search enabled","Observe 10-15 second UI freeze with elevated CPU usage","Compare: perform the same search restricted to title-only and observe it completes quickly"],"environment":"TaskFlow 2.3 (upgraded from 2.2), work laptop (specific OS not confirmed), ~5,000 tasks with some very large description fields","severity":"high","impact":"Any user with a moderately large task database (~5,000+ tasks) using full-text description search is affected. The UI freeze makes the application appear unresponsive and disrupts core search workflows. Title-only search is a partial workaround but limits functionality.","recommended_fix":"1. Diff the search implementation between v2.2 and v2.3, specifically for description-field search. 2. Check whether a full-text search index was dropped, altered, or is no longer being used. 3. Verify whether description search is running on the main/UI thread — if so, move it to a background thread or web worker. 4. Profile the search query against a dataset with ~5,000 records with large text fields. 5. Consider adding pagination or debouncing if not already present.","proposed_test_case":"Create a test dataset with 5,000+ tasks where at least 10% have description fields >2,000 characters. Execute a full-text description keyword search and assert that results return within an acceptable threshold (e.g., <1 second) and that the UI/main thread is not blocked during execution.","information_gaps":["Exact OS and hardware specs of the reporter's work laptop","Whether the issue reproduces on a clean 2.3 install (vs. upgrade path)","Specific changelog or migration notes for the 2.2→2.3 upgrade that might reference search changes"]}

---
