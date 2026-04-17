# Conversation: slow-search x omo-prometheus

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this. To help narrow down what's going on:

When did you first notice the slowdown? Was it after a specific update to TaskFlow, or did it gradually get worse over time? And when you say "search," are you using the main search bar, a filtered view, or something else?

---

### reporter

It started maybe 2 weeks ago? I think that's around when I updated to v2.3, so it might be related to that but I'm not 100% sure. I'm just using the main search bar at the top. I search for stuff in my task descriptions a lot since I paste meeting notes into them. Now that I think about it, searching by title still seems pretty quick — it's when I'm searching through the full descriptions that it really drags.

---

### triage-agent

That's really helpful — the distinction between title search being fast and description search being slow narrows this down a lot.

Can you give me a rough sense of scale? Approximately how many tasks do you have, and how large are the meeting notes you're pasting in (a few paragraphs, or full multi-page transcripts)? Also, is this a self-hosted instance or are you using the cloud version?

---

### reporter

I have around 5,000 tasks — I've been using TaskFlow for about 2 years now so they've built up. Some of the meeting notes are pretty long, like full meeting minutes that can be a couple thousand words. It's a self-hosted instance on my work laptop.

---

### triage-agent

Thanks — 5,000 tasks with full meeting minutes is a meaningful dataset, especially on a self-hosted laptop instance. That context helps a lot.

Do you know which database your TaskFlow instance is using? For self-hosted setups it's often SQLite by default, but you may have configured PostgreSQL or MySQL. You can usually check in your TaskFlow config file (typically `config.yaml` or `.env`). Also, have you noticed whether the slowdown gets worse with longer search queries, or is it roughly the same 10-15 seconds regardless of what you type?

---

### reporter

I'm not sure what database it's using honestly, I just installed it with the default settings so probably SQLite? I haven't really dug into the config files. As for the search queries, it feels like it's roughly the same amount of slow regardless of what I type — even a single word search takes a long time when searching descriptions. Oh and I did notice my laptop fan kicks on when I search, so it seems like it's really working the CPU hard.

---

### triage-agent

Based on everything you've described, here's what I think is happening: the v2.3 update likely changed how description search works under the hood — possibly switching from an indexed full-text search to scanning through every task's full description text on each query. That would explain why title search is still fast (small fields), why description search is uniformly slow regardless of what you type (it's reading all 5,000 tasks' descriptions every time), and why your CPU spikes (SQLite crunching through all that text).

Could you confirm your exact version? You can usually find it in **Settings → About** or by running `taskflow --version`. If it is v2.3.x, do you happen to recall whether it was a minor update (like 2.3.0 → 2.3.1) or a jump from an older version like 2.2?

---

### reporter

I updated from v2.2 to v2.3, so it was a bigger jump not just a minor patch. Let me check the exact version... yeah it says v2.3.0 in the About page. Your explanation makes sense with what I'm seeing — it really does feel like it's grinding through everything every time I search.

---

### triage-agent

[RESOLVED] {"title":"Description search regression in v2.3.0: full-text search likely falling back to full table scan on SQLite","problem":"Since upgrading from v2.2 to v2.3.0, searching task descriptions takes 10-15 seconds per query. Title search remains fast. The issue occurs on a self-hosted instance with ~5,000 tasks containing large description fields (full meeting minutes, thousands of words each). CPU usage spikes visibly during searches.","root_cause_hypothesis":"The v2.3.0 release likely changed the description search implementation — either removing SQLite FTS (full-text search) indexing, failing to migrate the FTS index during upgrade, or switching to a naive LIKE/%pattern% scan against the raw description column. This would cause SQLite to perform a sequential scan of all 5,000 task descriptions on every query, explaining the uniform ~10-15s latency regardless of query complexity and the CPU spike.","reproduction_steps":["Set up a self-hosted TaskFlow v2.3.0 instance with default (SQLite) configuration","Populate with a large number of tasks (~5,000) where many have long description fields (1,000+ words)","Perform a single-word search using the main search bar","Observe that title-matching results return quickly but description-matching results take 10-15 seconds","Monitor CPU usage during description search — expect a spike"],"environment":"TaskFlow v2.3.0, self-hosted on laptop, default installation (SQLite database), ~5,000 tasks with large description fields","severity":"high","impact":"Affects any self-hosted user with a non-trivial number of tasks who searches descriptions. Power users with large datasets (like this reporter) are most affected. Search is a core workflow — 10-15s latency per query significantly degrades usability. No known workaround other than avoiding description search.","recommended_fix":"1. Diff the search query logic between v2.2 and v2.3.0 — look for changes to how description search queries are constructed (FTS vs LIKE). 2. Check whether the v2.3.0 migration preserves or rebuilds the SQLite FTS virtual table for descriptions. 3. If FTS was removed, restore it. If the migration fails to rebuild the index, add a migration step. 4. Consider adding EXPLAIN QUERY PLAN logging for search queries in debug mode to catch future regressions.","proposed_test_case":"Create a test with 5,000+ tasks with description fields of 1,000+ words each on SQLite. Assert that a description search query completes in under 2 seconds. Run this test against both v2.2 (baseline) and v2.3.0 (regression) to confirm the performance delta.","information_gaps":["Exact search query SQL generated by v2.3.0 (requires code inspection, not reporter input)","Whether the SQLite FTS index exists in the reporter's database file (could be checked but wouldn't change the fix direction)","Whether other self-hosted users on v2.3.0 with smaller datasets also experience slower description search (just less noticeably)"]}

---
