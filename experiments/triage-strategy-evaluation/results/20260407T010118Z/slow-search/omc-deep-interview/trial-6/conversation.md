# Conversation: slow-search x omc-deep-interview

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help us narrow down what's going on — do you recall roughly when the search started feeling slow? For example, did it coincide with a TaskFlow update, a big increase in the number of tasks you have, or any other change to your setup?

---

### reporter

Yeah, I think it started about 2 weeks ago. I updated to v2.3 around that time, so it might be related to that? I'm not 100% sure if the timing lines up exactly but it definitely wasn't this slow before. I don't think I added a huge number of tasks recently — I've had around 5,000 for a while now.

---

### triage-agent

That timing with v2.3 is really helpful — we'll definitely look into what changed there.

Could you walk me through what a typical slow search looks like for you? For instance: are you using the main search bar, a filtered view, or something else? And does it happen with every search, or only with certain kinds of queries (like searching by keyword vs. filtering by tag or status)?

---

### reporter

I just use the main search bar. Now that you mention it, I think it's mostly slow when it's searching through the task descriptions — like if I search for a word that's in the body of a task. Searching for task titles seems to still be pretty quick actually. I have some tasks with really long descriptions (I paste meeting notes into them sometimes) so maybe that's part of it? But it used to handle those fine.

---

### triage-agent

That's a really useful distinction — title search being fast while description search is slow gives us something concrete to investigate.

A couple of follow-up details that would help us reproduce this on our end: is the slowness consistent every time you search descriptions (always 10-15 seconds), or does it vary — for example, is the first search after opening TaskFlow slower than subsequent ones? And does it matter how common the search term is — like, does searching for a word that appears in hundreds of tasks feel different from one that only matches a few?

---

### reporter

It's pretty consistently slow, yeah. Every time I search descriptions it takes a long time, doesn't matter if it's the first search or not. I haven't really paid attention to whether common vs rare words make a difference — it all just feels slow. I did notice my laptop fan kicks on when I search, like it's working really hard, if that helps.

---

### triage-agent

That detail about the fan spinning up is really helpful — it tells us the search is doing heavy computation rather than just waiting on something.

To help us zero in on the cause: if you try searching for a term that you know only appears in tasks with shorter descriptions (not the ones with pasted meeting notes), does it still take just as long? Or put another way — if you temporarily edited one of those long-description tasks to remove most of the text and then searched, would you expect it to be any different?

No worries if you haven't tried that — even a rough sense of whether the long descriptions seem to be the bottleneck would be useful.

---

### reporter

Hmm, I haven't tried that specifically. I don't think it matters though — even searching for pretty unique words that probably only match a couple tasks still feels slow. It's not like it's fast when there are fewer matches. The whole description search just seems broken compared to how it used to be. I guess the long descriptions could be making it worse but honestly I'm not sure, it just feels uniformly slow whenever I'm not searching by title.

---

### triage-agent

[RESOLVED] {"title":"v2.3 regression: full-text description search is CPU-bound and takes 10-15s (~5,000 tasks)","problem":"After updating to v2.3, searching task descriptions via the main search bar takes 10-15 seconds consistently, regardless of query specificity or number of matches. Title search remains fast. The search is CPU-intensive (laptop fan spins up), indicating a computational bottleneck rather than a network or I/O wait. The user has ~5,000 tasks, some with long descriptions (pasted meeting notes), and reports this previously worked fine.","root_cause_hypothesis":"v2.3 likely changed the description search implementation — most probably removed or broke a search index, switched from indexed/optimized search to a naive full-text scan across all task descriptions, or introduced an inefficient regex/string matching approach. The fact that title search is still fast suggests titles may still use an index while descriptions now do a brute-force scan. The CPU spike confirms the work is computational (scanning/matching text) rather than waiting on a database query or network call.","reproduction_steps":["Set up TaskFlow v2.3","Create or import ~5,000 tasks, including some with long descriptions (e.g., multi-paragraph meeting notes)","Open the main search bar","Search for any term that appears in task descriptions (not just titles)","Observe search taking 10-15 seconds with high CPU usage","Compare: search for a term that only appears in task titles — should return quickly"],"environment":"TaskFlow v2.3, work laptop (OS unspecified), ~5,000 tasks with some containing lengthy descriptions","severity":"high","impact":"Any user with a non-trivial number of tasks is likely affected when searching by description content. The feature is still functional but degraded to the point of being painful (10-15s waits). Title-only search serves as a partial workaround but limits the utility of search significantly.","recommended_fix":"Diff the search implementation between v2.2 and v2.3, focusing on how description content is searched. Likely areas: (1) check if a full-text search index on task descriptions was dropped, disabled, or is no longer being updated; (2) check if the search query path changed from an indexed database query to an in-memory or brute-force scan; (3) profile the description search path with ~5,000 tasks to confirm where CPU time is spent. If an index was removed, restore it. If the search algorithm changed, revert or optimize it.","proposed_test_case":"Performance test: with 5,000 tasks (including ~50 with descriptions over 2,000 characters), a description search query should return results in under 2 seconds. Regression test: compare description search latency between v2.2 and v2.3 with identical datasets to verify the fix restores prior performance.","information_gaps":["Exact OS and hardware specs of the reporter's laptop (unlikely to affect fix direction since this is an algorithmic regression)","Whether the issue reproduces on v2.3 with fewer tasks (e.g., 100) — would clarify if it's O(n) scaling or a fixed overhead","v2.3 changelog details on what changed in the search subsystem"]}

---
