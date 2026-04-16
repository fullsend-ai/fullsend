# Conversation: slow-search x omc-deep-interview

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help us narrow down what's going on, could you tell us a bit more about the searches that are slow? Specifically: does this happen with every search query, or only certain ones (e.g., longer search terms, searches within a specific project or tag)? And do you recall roughly when the slowdown started — was it after an update, after your task list grew past a certain size, or did it seem to happen suddenly?

---

### reporter

Now that you mention it, I think it started after I upgraded to v2.3 a couple weeks ago. And actually, it's not every search — when I search by task title it's still pretty quick. It's when I'm searching through task descriptions that it gets really slow. I have about 5,000 tasks and some of them have pretty long descriptions (I paste meeting notes into them sometimes). Not sure if that's relevant but figured I'd mention it.

---

### triage-agent

That's really helpful context — the v2.3 timing and the description-vs-title distinction are great clues. One more thing that would help us pin this down: when you search descriptions, is it *every* search that's slow, or does the speed vary? For example, if you search for a single common word like "meeting" vs. a more specific phrase, do you notice a difference? And is the slowness consistent (always ~10-15s) or does it sometimes come back faster?

---

### reporter

Hmm, I haven't really tested it that systematically. It feels like it's always slow when searching descriptions, whether it's a common word or something specific. Maybe sometimes it's like 8 seconds instead of 15, but it never feels fast like it used to. I did notice my laptop fan kicks on when I search, if that helps — like it's working really hard. But yeah, it's pretty consistently slow for any description search.

---

### triage-agent

That's useful — the fan spinning up is a good signal that something is working harder than it should.

One thing I want to make sure we're not overlooking: when you do a description search, does the slowness happen *during* the search (i.e., you hit search and wait 10-15 seconds with nothing on screen), or does the search seem to return but then the results are slow to appear or scroll? I'm trying to figure out whether the bottleneck is in *finding* the results or in *displaying* them, since your descriptions can be quite long.

---

### reporter

It's definitely the searching part. I hit search and then just sit there waiting — nothing shows up for like 10-15 seconds. Once the results finally appear they display fine, scrolling is normal and everything. It's just that waiting period after I hit the search button where nothing happens and my fan is going crazy.

---

### triage-agent

[RESOLVED] {"title":"Description search regression in v2.3: 10-15s query time with large task sets","problem":"After upgrading to v2.3, searching by task description takes 10-15 seconds and causes high CPU usage (fan spin-up). Searching by task title remains fast. The delay occurs during the search/query phase, not result rendering. The user has approximately 5,000 tasks, some with very long descriptions (pasted meeting notes).","root_cause_hypothesis":"v2.3 likely introduced a change to how description searches are executed — most probably a switch to unindexed full-text scanning of description fields, removal of a description search index, or a new search code path that performs brute-force string matching across all description content. The CPU spike and consistent slowness regardless of query specificity both point to a sequential scan rather than indexed lookup. Title search remaining fast confirms titles are still indexed.","reproduction_steps":["Install or upgrade to TaskFlow v2.3","Create or import ~5,000 tasks, ensuring many have long descriptions (multiple paragraphs, e.g., pasted meeting notes)","Perform a search filtering by task description using any search term","Observe 10-15 second delay before results appear and elevated CPU usage","Compare with a title-only search on the same dataset to confirm title search is fast"],"environment":"TaskFlow v2.3, ~5,000 tasks with long descriptions, running on a work laptop (specific OS/specs not collected but not needed to reproduce)","severity":"high","impact":"Any user with a moderately large task set searching by description will experience severe slowdowns after upgrading to v2.3. This makes description search effectively unusable for power users, forcing them to rely only on title search as a workaround.","recommended_fix":"Diff the search implementation between v2.2 and v2.3 to identify what changed in the description search path. Most likely fix: add or restore a full-text index on the task description field. If the search was moved to application-layer string matching, push it back to the database/search engine. Profile the description search query to confirm whether the bottleneck is a table scan.","proposed_test_case":"Performance test: seed the database with 5,000 tasks (descriptions averaging 500+ words). Assert that a description search for an arbitrary term returns results in under 2 seconds. Run this test against both v2.2 (baseline) and v2.3 (regression) to confirm the fix restores prior performance.","information_gaps":["Exact OS and hardware specs of the reporter's laptop (unlikely to affect fix direction since this is a search algorithm/indexing issue)","Whether other users on v2.3 with large datasets experience the same issue (likely yes, given root cause hypothesis)","Whether the reporter has tried any workarounds such as title-only search"]}

---
