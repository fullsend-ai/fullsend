# Conversation: slow-search x superpowers-brainstorming

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! A couple of things would help me narrow this down quickly.

Roughly how many tasks are we talking about?

1. Hundreds (under 1,000)
2. Low thousands (1,000–5,000)
3. Tens of thousands or more
4. Not sure

And do you recall approximately when search started feeling slow — was it after a specific update, or did it degrade gradually over time as you added more tasks?

---

### reporter

I'd say low thousands — probably around 5,000 tasks at this point, been using TaskFlow for about 2 years. And now that you mention it, I think it started about 2 weeks ago when I upgraded to v2.3. It was fine before that.

---

### triage-agent

[RESOLVED] {"title":"Search performance regression in v2.3 (~10-15s latency with ~5K tasks)","problem":"After upgrading to TaskFlow v2.3 approximately two weeks ago, search queries that previously returned quickly now take 10-15 seconds. The user has approximately 5,000 tasks accumulated over two years of use. Search was performant on the prior version.","root_cause_hypothesis":"A change introduced in v2.3 likely degraded search performance — possible causes include a missing or dropped database index, a change from indexed search to full table scan, removal or misconfiguration of search result caching, or a new search feature (e.g., full-text search across more fields) that scales poorly with dataset size.","reproduction_steps":["Set up a TaskFlow instance on v2.3","Populate the database with ~5,000 tasks","Execute a search query and measure response time","Repeat on v2.2 with the same dataset to confirm the regression"],"environment":"TaskFlow v2.3, ~5,000 tasks, work laptop (specific OS and hardware unknown but not relevant to a server/app-side regression)","severity":"high","impact":"Search is a core workflow feature. 10-15 second latency on a dataset of only 5K tasks makes it effectively unusable for active users. Likely affects all users with non-trivial task counts after upgrading to v2.3.","recommended_fix":"Diff all search-related code and database migration changes between v2.2 and v2.3. Profile the search query execution plan against a ~5K task dataset to identify whether the bottleneck is query execution (missing index, full scan), application-layer processing, or network/serialization. Check for dropped indexes in v2.3 migrations. If a new search feature was added, verify it has appropriate indexing and pagination.","proposed_test_case":"Performance regression test: seed database with 5,000 tasks, execute a representative search query, and assert that results return within an acceptable threshold (e.g., under 1 second). Run this test against both v2.2 and v2.3 to validate the fix closes the gap.","information_gaps":["Exact search queries that are slow (all searches vs. specific patterns)","Whether the issue is present on TaskFlow's server-side or is a self-hosted instance","Whether other v2.3 users have reported the same degradation"]}

---
