# Conversation: slow-search x omo-prometheus

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this. To help us narrow down the cause — can you tell us roughly when the slowdown started? Specifically: did it happen suddenly (e.g., after an update or migration), or has it been getting gradually worse over time? And do you know approximately how many tasks you currently have in the system?

---

### reporter

I think it started maybe 2 weeks ago? I recently updated to the latest version — I was on 2.2 before and upgraded to 2.3, so it might have been around that time but I'm not 100% sure. As for tasks, I've been using TaskFlow for about 2 years now so I have a lot — probably around 5,000 or so.

---

### triage-agent

Thanks for those details — the timing around the 2.3 upgrade is a useful lead.

I want to understand the search behavior more precisely: **is every search slow (10-15 seconds), or only certain kinds of searches?** For example, does searching for a single word like a task title behave differently than a broader search? And when you run the same search twice in a row, is the second attempt just as slow as the first?

---

### reporter

Now that you mention it, I think searching for task titles is still pretty quick. It's when I search for something that would be in the task description that it gets really slow. I have some tasks with really long descriptions — like I paste meeting notes into them sometimes. And yeah, searching the same thing twice seems equally slow both times, it doesn't get faster on repeat.

---

### triage-agent

This is shaping up nicely — the pattern you're describing (title search fast, description search slow, no improvement on repeat queries) points strongly toward a database-level issue with how description content is being searched.

To help us confirm: **are you running TaskFlow self-hosted (e.g., with your own PostgreSQL/MySQL database), or are you using the hosted/cloud version?** If self-hosted, have you noticed any increase in disk usage or CPU spikes when you run a search? This will help us determine whether the 2.3 upgrade may have changed how full-text search on descriptions works.

---

### reporter

I'm running it locally on my laptop, not the cloud version. It's a ThinkPad T14 with Ubuntu. I have noticed that when I do a search, the fan kicks up — I checked the system monitor once and it looked like one CPU core was pegged at 100% during the search. I have 32GB of RAM so I don't think memory is the issue. Not sure about disk usage though, I haven't checked that.

---

### triage-agent

Thanks — the CPU spiking to 100% on a single core during description searches is a strong signal. Here's what I'm thinking: the v2.3 upgrade likely changed how description content is searched, possibly dropping a full-text index or switching to a brute-force scan of every description. With ~5,000 tasks (some with lengthy meeting notes), that would explain both the 10-15 second delays and the CPU spike.

One more thing that will help us pin this down: **do you know which database TaskFlow is using on your setup — is it SQLite (the default for local installs), PostgreSQL, or something else?** If you're not sure, you can check your TaskFlow config file or the data directory for a `.sqlite` or `.db` file.

---

### reporter

Honestly I'm not sure, I just installed it with the defaults so it's probably whatever comes out of the box. I haven't messed with any database settings. I can check the data folder if that helps but I wouldn't know offhand.

---

### triage-agent

[RESOLVED] {"title":"Full-text search on task descriptions regressed to brute-force scan in v2.3 (~10-15s latency)","problem":"After upgrading from v2.2 to v2.3, searching for text that appears in task descriptions takes 10-15 seconds and pegs a CPU core at 100%. Title-based searches remain fast. The slowdown is consistent across repeat queries (no caching benefit). The user has ~5,000 tasks, some with lengthy descriptions containing pasted meeting notes.","root_cause_hypothesis":"The v2.3 release likely altered how description content is searched — most probably by dropping or failing to create a full-text search index on the descriptions column, or by switching from an indexed FTS query to a LIKE/sequential scan. With ~5,000 rows containing large text blobs, a sequential scan would explain both the 10-15 second latency and the single-core CPU spike. The lack of caching improvement on repeat queries further supports a per-query full scan rather than a missing warm-up.","reproduction_steps":["Install TaskFlow v2.3 with default (likely SQLite) database configuration","Populate the database with ~5,000 tasks, including several with long description text (e.g., pasted meeting notes)","Perform a search using a term that appears only in task descriptions (not titles)","Observe 10-15 second response time and high CPU usage on one core","Compare: perform a search for a term that appears in a task title — this should return quickly"],"environment":"TaskFlow v2.3 (upgraded from v2.2), self-hosted local install with default settings (likely SQLite), Ubuntu on ThinkPad T14, 32GB RAM","severity":"high","impact":"Any self-hosted user with a non-trivial number of tasks (~thousands) will experience severely degraded search performance on description content. This is a core workflow feature. Partial workaround exists (searching by title still works), but users who rely on description search are effectively blocked.","recommended_fix":"1. Diff the search query logic between v2.2 and v2.3 — look for changes to how description fields are queried (FTS index usage, LIKE vs. FTS, query planner hints). 2. Check database migrations in v2.3 for any dropped or missing full-text index on the descriptions column. 3. If using SQLite, verify that the FTS5 virtual table for descriptions is being created and queried correctly. 4. Add an EXPLAIN QUERY PLAN check to confirm whether the description search is using an index or performing a full table scan. 5. If the index was inadvertently dropped, add a migration to recreate it; if the query changed, revert to the indexed query path.","proposed_test_case":"Create a database with 5,000 tasks (descriptions averaging 500+ words each). Run a description-content search and assert that results return in under 2 seconds. Run the same test against v2.2 as a baseline to confirm the regression. Additionally, verify via EXPLAIN QUERY PLAN that the description search uses an FTS index rather than a sequential scan.","information_gaps":["Exact database engine not confirmed (assumed SQLite based on default local install)","Exact v2.3 changelog or migration diff not reviewed — root cause is inferred from symptoms","Whether the issue reproduces on a clean v2.3 install (vs. upgrade path only) is unknown"]}

---
