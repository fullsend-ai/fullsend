# Conversation: slow-search x socratic-refinement

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this — 10-15 seconds definitely sounds excessive. You mentioned it used to be fast, which is a really helpful clue. Can you tell me roughly when you first noticed the slowdown? And thinking back to around that time, did anything change — like a TaskFlow update, a big import of tasks, or any changes to your setup? Even things that seem unrelated might help us narrow this down.

---

### reporter

Now that you mention it, I think it started about 2 weeks ago, right around when I updated from v2.2 to v2.3. I didn't connect the two at first but the timing lines up. I haven't done any big imports or anything like that — I've had around 5,000 tasks built up over the past couple years and that number hasn't changed much recently.

---

### triage-agent

That timing with the v2.3 update is a really strong lead — thanks for piecing that together.

I'd love to understand the slowness pattern a bit more before we hand this off. When you search, is it slow every single time, or is there a pattern? For instance, is the first search after opening TaskFlow the worst and then it gets a bit better? And does it matter what you search for — like, are short common words just as slow as longer, more specific queries? Anything you've noticed about when it's worse vs. better would be really helpful.

---

### reporter

It's pretty much slow every single time, doesn't seem to matter if it's my first search or my tenth. And the length of what I search for doesn't really make a difference either — short words, long phrases, all equally slow. One thing I did notice though is that searching by task title seems to still be fast. It's specifically when I'm searching through task descriptions that it crawls. I search through descriptions a lot because I paste meeting notes and stuff into my tasks, so some of those descriptions are pretty long.

---

### triage-agent

[RESOLVED] {"title":"Search regression in v2.3: description search takes 10-15 seconds while title search remains fast","problem":"After upgrading from v2.2 to v2.3, full-text search over task descriptions takes 10-15 seconds consistently, regardless of query length or frequency. Title-only search remains fast. The user has ~5,000 tasks with lengthy descriptions (including pasted meeting notes).","root_cause_hypothesis":"The v2.3 update likely introduced a regression in how description fields are searched — most probably a dropped or unused database index on the description column, a change from indexed full-text search to a naive sequential scan, or a query planner change that bypasses the description index. The fact that title search is unaffected suggests the issue is isolated to the description search path.","reproduction_steps":["Set up a TaskFlow instance with v2.3 installed","Populate the database with ~5,000 tasks, including tasks with long descriptions (multiple paragraphs, pasted meeting notes)","Perform a search that targets task descriptions","Observe response time of 10-15 seconds","Repeat the same search targeting only task titles and observe fast response","Optionally: downgrade to v2.2 and confirm description search is fast again"],"environment":"TaskFlow v2.3 (upgraded from v2.2), work laptop (OS unspecified), ~5,000 tasks with long descriptions","severity":"high","impact":"Users who search task descriptions — especially power users with large task counts and long descriptions — experience unusable search performance. This is a core workflow regression from a stable release.","recommended_fix":"Diff the search implementation and database migrations between v2.2 and v2.3. Specifically investigate: (1) whether a full-text index on the description column was dropped or altered, (2) whether the search query for descriptions changed (e.g., switched from indexed FTS to LIKE/ILIKE scan), (3) whether a new ORM or query builder is generating a different query plan. Run EXPLAIN ANALYZE on the description search query against a dataset of ~5,000 tasks to confirm the scan type.","proposed_test_case":"Performance regression test: populate a test database with 5,000 tasks having realistic description lengths (500-2,000 words each). Assert that a description search completes in under 2 seconds. Run this test as part of the search module's CI suite to prevent future regressions.","information_gaps":["Exact OS and hardware specs (unlikely to be relevant given the version correlation)","Whether the user has any custom plugins or integrations that might interact with search","Database engine in use (SQLite vs PostgreSQL vs other) — may affect index behavior"]}

---

