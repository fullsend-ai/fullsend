# Conversation: slow-search x structured-triage

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! The slowness you're describing definitely sounds frustrating.

To help us dig into this, could you walk us through the steps to reproduce the issue? Specifically:
- What are you searching for when it's slow? (e.g., a keyword, a filter, a tag)
- Are you searching from the main search bar, or using a filtered view?
- Does it happen with any search term, or only certain queries?

Even a single example like "I type 'weekly report' in the main search bar and it takes ~12 seconds to return results" would be very helpful.

---

### reporter

Sure! I typically search for keywords in my task descriptions — like searching for 'budget review' or 'quarterly planning' from the main search bar. It happens with pretty much any search term. For example, I just typed 'meeting notes' and it took about 12 seconds to come back with results. The weird thing is, if I search for something that I know is in a task *title*, it comes back almost instantly. It's only slow when the results seem to be matching on the description text.

---

### triage-agent

Thanks for those details — that's a really useful clue that title searches are fast but description searches are slow. That narrows things down a lot.

Could you share a few environment details so we can try to reproduce this on our end?

- **OS and browser** (e.g., Windows 11 / Chrome 124, macOS / Firefox, etc.)
- **TaskFlow version** (you can usually find this under Settings → About or in the page footer)
- **Approximate number of tasks** in your account (a rough ballpark like "a few hundred" or "several thousand" is fine)

---

### reporter

I'm running Ubuntu 22.04 on a ThinkPad T14. TaskFlow version is 2.3 — I actually upgraded from 2.2 about two weeks ago, and now that I think about it, the slowness started around that same time. I have roughly 5,000 tasks built up over the past couple of years.

---

### triage-agent

[RESOLVED] {"title":"Search on task descriptions is extremely slow since v2.3 upgrade (~12s for 5,000 tasks)","problem":"Full-text search against task description fields takes 10-15 seconds to return results, while searches matching task titles return almost instantly. The reporter has approximately 5,000 tasks and the slowness began after upgrading from TaskFlow 2.2 to 2.3.","root_cause_hypothesis":"The v2.3 upgrade likely introduced a regression in how description text is searched — possibly a missing or dropped database index on the task descriptions column, a change from indexed full-text search to unindexed LIKE/ILIKE queries, or a search implementation change that scans descriptions without leveraging an index. Title searches remain fast because the title index was unaffected.","reproduction_steps":["Have an account with a large number of tasks (~5,000)","Open the main search bar","Search for a keyword that appears in task descriptions but not titles (e.g., 'meeting notes', 'budget review')","Observe that results take 10-15 seconds to return","Search for a keyword that matches a task title and observe near-instant results"],"environment":"Ubuntu 22.04, ThinkPad T14, TaskFlow v2.3 (upgraded from v2.2 approximately two weeks ago), ~5,000 tasks","severity":"high","impact":"Any user with a non-trivial number of tasks experiences severely degraded search performance when searching description text, which is a core workflow. The regression affects all description-based searches regardless of query term.","recommended_fix":"Compare the v2.2 and v2.3 database migrations and search query logic. Check whether a full-text index on the task descriptions column was dropped, altered, or is no longer being used by the search query path. Verify the query plan (EXPLAIN ANALYZE) for description searches against a dataset of ~5,000 tasks. If an index is missing, add it back; if the query strategy changed, restore indexed full-text search for descriptions.","proposed_test_case":"Create a test with 5,000+ tasks with varied description text. Execute a search for a term that matches only in descriptions and assert that results return within an acceptable threshold (e.g., under 1 second). Also verify that the search query plan uses an index scan rather than a sequential scan on the descriptions column.","information_gaps":["No server-side logs or query timing data to confirm the bottleneck is database-side vs. application-side","Unknown whether other users with large task counts on v2.3 are also affected","Browser developer tools network timing not provided (would confirm if latency is server response time vs. client rendering)"]}

---
