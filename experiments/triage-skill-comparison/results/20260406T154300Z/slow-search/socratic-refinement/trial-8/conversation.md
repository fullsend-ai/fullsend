# Conversation: slow-search x socratic-refinement

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! I'd like to understand the timeline a bit better. Can you recall roughly when the search started feeling slow — was it a gradual decline over time as you added more tasks, or did it seem to happen suddenly (e.g., after an update or a specific change to your workflow)? And when you say you have "a lot of tasks," could you give me a rough sense of the scale — are we talking hundreds, thousands, more?

---

### reporter

It was pretty sudden actually. I upgraded from v2.2 to v2.3 about two weeks ago and I think that's when it started. I have around 5,000 tasks — I've been using TaskFlow for about 2 years now. Before the upgrade, search was snappy, well under a second.

---

### triage-agent

[RESOLVED] {"title":"Search performance regression in v2.3: 10-15s response time (was <1s in v2.2)","problem":"After upgrading from v2.2 to v2.3, search queries that previously returned results in under a second now take 10-15 seconds. The reporter has approximately 5,000 tasks accumulated over 2 years of use.","root_cause_hypothesis":"A change in the v2.3 search implementation likely introduced a performance regression — possible causes include a dropped or changed database index, a switch from indexed search to full-table scan, removal of query result caching, or a new search feature (e.g., full-text or fuzzy matching) that scales poorly with large task counts.","reproduction_steps":["Set up a TaskFlow instance with ~5,000 tasks (or use a seeded test database at that scale)","Run search queries on v2.2 and record response times","Upgrade to v2.3 and run the same search queries","Compare response times — expect sub-second on v2.2 and 10-15 seconds on v2.3"],"environment":"TaskFlow v2.3 (upgraded from v2.2), work laptop (OS and specs unspecified), ~5,000 tasks","severity":"high","impact":"Search is a core workflow feature. A 10-15x slowdown affects any user with a substantial task history, making the feature effectively unusable for power users. Likely affects all v2.3 users at scale, not just this reporter.","recommended_fix":"Diff the search-related code and database migrations between v2.2 and v2.3. Check for: (1) dropped or altered indexes on task tables, (2) changes to the search query (e.g., switching from indexed lookup to LIKE/full-scan), (3) removal of search result caching, (4) new search features that lack optimization for large datasets. Profile the search query on a 5,000-task dataset to confirm the bottleneck.","proposed_test_case":"Performance regression test: seed a database with 5,000 tasks, execute a representative search query, and assert that results return in under 2 seconds. Run this test as part of CI to prevent future regressions.","information_gaps":["Whether all search queries are equally slow or only certain query patterns","Exact OS and hardware specs of the reporter's laptop (unlikely to be the root cause given the version correlation)","Whether other v2.3 users have reported the same issue"]}

---

