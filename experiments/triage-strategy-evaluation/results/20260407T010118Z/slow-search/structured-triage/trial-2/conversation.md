# Conversation: slow-search x structured-triage

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help us investigate, could you clarify what you expected to happen? Specifically: how fast was search performing before (roughly), and do you recall when it started slowing down — for example, after an update, after adding a certain number of tasks, or after changing any settings?

---

### reporter

It used to be pretty much instant, like under a second. I think it started getting slow maybe 2 weeks ago? I did update to v2.3 around that time, so it might be related to that, but I'm not 100% sure. I haven't changed any settings or anything like that.

---

### triage-agent

Thanks for that context — the timing around the v2.3 update is a useful clue.

Could you walk me through the steps to reproduce the slow search? For example:
- Where in TaskFlow are you searching (the main task list, a specific project, etc.)?
- What are you typing into the search box — a specific word, a partial phrase, or something else?
- Does it happen with every search query, or only certain ones (e.g., longer queries, common words)?

Even a single example like "I type 'weekly report' into the search bar on the main dashboard and it takes ~12 seconds" would be really helpful.

---

### reporter

I usually search from the main task list — I just type a word or phrase into the search bar at the top. Like if I search for "budget review" or "quarterly planning" it takes a really long time, maybe 10-15 seconds. It seems to happen with pretty much any search term. Actually, now that I think about it, I noticed that when I search by task title specifically (using the title filter dropdown) it's still fast. It's only slow when I'm searching across the full task descriptions. Not sure if that helps.

---

### triage-agent

That detail about title search being fast vs. full-description search being slow is really helpful — that narrows things down a lot.

Could you share a bit more about your environment? Specifically:
- What operating system is your work laptop running (e.g., Windows 11, macOS Sonoma, etc.)?
- Are you using TaskFlow in a browser (if so, which one?) or the desktop app?
- Roughly how many tasks do you have in your account? (You mentioned having a lot — even a ballpark like "hundreds" or "thousands" would help.)

---

### reporter

I'm on Ubuntu 22.04, it's a ThinkPad T14 with 32GB of RAM. I'm using the desktop app, not the browser version. As for tasks, I've been using TaskFlow for about 2 years now so I've got around 5,000 tasks in there. A lot of them have pretty long descriptions — I tend to paste in meeting notes and stuff like that.

---

### triage-agent

[RESOLVED] {"title":"Full-description search takes 10-15 seconds with large task corpus (title search unaffected)","problem":"Since approximately the v2.3 update (~2 weeks ago), searching across full task descriptions from the main task list takes 10-15 seconds for any query. Searching by title only (via the title filter dropdown) remains fast. The user has ~5,000 tasks, many with lengthy descriptions (pasted meeting notes).","root_cause_hypothesis":"The v2.3 update likely changed the full-text search implementation for task descriptions — possibly dropping or failing to use an index on description content, switching to a non-indexed scan, or introducing a regression in the search query path that doesn't affect the title-only filter. The large volume of long descriptions (~5,000 tasks with pasted meeting notes) amplifies the impact of any missing index or inefficient query.","reproduction_steps":["Have an account with a large number of tasks (~5,000) with substantial description text","Open the main task list in the desktop app","Type any search term (e.g., 'budget review') into the search bar at the top","Use the default full-description search (do NOT use the title filter dropdown)","Observe that results take 10-15 seconds to appear","Switch to the title filter dropdown and search the same term — observe that it returns near-instantly"],"environment":"Ubuntu 22.04, Lenovo ThinkPad T14, 32GB RAM, TaskFlow desktop app v2.3, ~5,000 tasks with long descriptions","severity":"medium","impact":"Users with large task corpora experience unacceptably slow full-description search. Title-only search is unaffected. This degrades daily workflow for power users who rely on full-text search across task descriptions.","recommended_fix":"Investigate what changed in the full-text search path in v2.3. Check whether an index on task descriptions was dropped, altered, or is no longer being used. Compare the query plan for description search vs. title search. Profile the description search query against a dataset with ~5,000 tasks with long descriptions.","proposed_test_case":"Create a test dataset with 5,000+ tasks containing multi-paragraph descriptions. Run a full-description search and assert that results return within an acceptable threshold (e.g., under 2 seconds). Compare performance between title-only and full-description search to verify they are in the same order of magnitude.","information_gaps":["No error messages or logs collected from the desktop app — may reveal query timeouts or warnings","Exact v2.3 release notes not consulted to identify relevant changes to search","Unknown whether other users with fewer tasks also experience the slowdown on v2.3"]}

---
