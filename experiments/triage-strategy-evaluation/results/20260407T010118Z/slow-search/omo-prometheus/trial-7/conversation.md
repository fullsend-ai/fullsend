# Conversation: slow-search x omo-prometheus

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this. A few things would help me narrow this down quickly:

When did you first notice the slowdown? Was it after a specific update to TaskFlow, or did it come on gradually? And roughly how many tasks are we talking about — hundreds, thousands, tens of thousands?

---

### reporter

I think it started maybe 2 weeks ago? I'm pretty sure I updated TaskFlow around that time — went from 2.2 to 2.3 if I remember right. As for tasks, I've been using it for about 2 years so I probably have around 5,000 or so.

---

### triage-agent

Thanks for that context — a 2.2→2.3 upgrade timeline lines up well as a potential regression.

To dig in further: is *every* search slow, or only certain types of queries? For example, does searching for a single word like a task title behave differently than searching with filters (by date, tag, status, etc.)? And when you say 10-15 seconds — is that consistently slow, or does the first search after opening TaskFlow take longer while later searches are faster?

---

### reporter

Now that you mention it, I think searching by task title is still pretty quick. It's when I search for something that would be in the task description that it gets really slow. I haven't really noticed a difference between the first search and later ones — it seems consistently slow when I'm searching descriptions. I don't really use filters much so I can't say about that.

---

### triage-agent

That's a really helpful distinction — title search fast, description search slow is a strong clue.

Here's my working theory: the 2.3 update likely changed how description search works under the hood (possibly dropped or changed an index, or switched to a different search method for description content). Does that line up with anything you noticed in the release notes?

Also, one more detail that would help: are your task descriptions typically short (a sentence or two) or longer (paragraphs, pasted logs, etc.)? And when description search is slow, does it seem to scale with how common the search term is — e.g., searching for a rare word vs. a common one like "update"?

---

### reporter

I didn't really read the release notes honestly. I just updated when it told me there was a new version available. As for descriptions, yeah some of mine are pretty long — I tend to paste meeting notes and stuff into task descriptions so some of them are probably a couple thousand words. I haven't really paid attention to whether the search term matters, it just feels slow whenever I'm searching descriptions in general.

---

### triage-agent

[RESOLVED] {"title":"Regression in 2.3: full-text search on task descriptions is 10-15x slower than expected","problem":"After upgrading from TaskFlow 2.2 to 2.3, searching by task description content takes 10-15 seconds to return results. Title-based search remains fast. The slowdown is consistent across searches and does not improve with repeated queries (ruling out cold-cache effects).","root_cause_hypothesis":"The 2.3 release likely changed the description search implementation — most probably dropped or failed to migrate a full-text index on the description field, or switched from indexed search to a naive LIKE/substring scan. With ~5,000 tasks and some descriptions containing thousands of words, an unindexed scan would explain the 10-15 second latency while title search (shorter field, likely still indexed) remains fast.","reproduction_steps":["Have a TaskFlow 2.3 instance with a large dataset (~5,000 tasks, some with multi-paragraph descriptions)","Perform a search using a term that appears in task descriptions (not just titles)","Observe search taking 10-15 seconds to return results","Perform the same search scoped to titles only — observe it returns quickly","Optionally: repeat on a TaskFlow 2.2 instance with the same dataset to confirm regression"],"environment":"TaskFlow 2.3 (upgraded from 2.2), work laptop (OS unspecified), ~5,000 tasks accumulated over 2 years, some descriptions contain pasted meeting notes (~2,000+ words)","severity":"medium","impact":"Users with large task databases who rely on description search experience significant delays. Search is functional but painfully slow. Title search remains usable as a partial workaround. Likely affects all users with non-trivial dataset sizes on 2.3.","recommended_fix":"Diff the search implementation between 2.2 and 2.3, focusing on how description content is queried. Check for: (1) dropped or altered full-text index on the description column, (2) migration that failed to rebuild the index, (3) query change from indexed full-text search to unindexed LIKE/ILIKE scan. Verify the index exists in a 2.3 database and compare query plans (EXPLAIN ANALYZE) for description vs. title search.","proposed_test_case":"Create a test database with 5,000+ tasks where at least 500 have descriptions over 1,000 words. Run a description search query and assert it completes in under 1 second. Run the same test against both 2.2 and 2.3 schemas to verify the regression and confirm the fix.","information_gaps":["Exact OS and hardware specs of the reporter's laptop (unlikely to be root cause given the title-vs-description split)","Whether the 2.3 release notes mention any search or database migration changes","Whether the slowdown scales with search term frequency (common vs. rare words)"]}

---
