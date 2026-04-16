# Conversation: slow-search x omc-deep-interview

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! A couple of things that would really help narrow this down: roughly when did you first notice the slowdown — was it after a TaskFlow update, after importing a batch of tasks, or did it seem to happen gradually? And when you say you have a lot of tasks, could you give a rough number (hundreds, thousands)? That'll help us figure out where to look.

---

### reporter

Now that you mention it, I think it started about 2 weeks ago when I upgraded from v2.2 to v2.3. I don't remember importing anything new around that time. I have roughly 5,000 tasks — I've been using TaskFlow for about 2 years now and everything gets logged in there.

---

### triage-agent

That's really helpful — the v2.3 upgrade timing is a strong clue. One more thing: is every search slow, or does it depend on what you're searching for? For example, is searching by a single word just as slow as searching with filters or longer phrases? And does the first search after opening TaskFlow feel slower than subsequent ones, or is it consistently 10-15 seconds every time?

---

### reporter

Good question — I just tested it. Searching by task title is still fast, pretty much instant like it used to be. But when I search across task descriptions, that's when it gets really slow. It's consistently slow too, not just the first time. Every description search takes 10-15 seconds. I have some tasks with really long descriptions (like copy-pasted meeting notes) so maybe that's related? I also noticed my laptop's fan kicks into high gear when I search, like it's working really hard.

---

### triage-agent

[RESOLVED] {"title":"Description search regression in v2.3: 10-15s latency with CPU spike on datasets with ~5,000 tasks","problem":"After upgrading from v2.2 to v2.3, searching across task descriptions takes 10-15 seconds consistently and causes high CPU usage (fan spin-up). Title-only search remains instant. The user has ~5,000 tasks accumulated over 2 years, some with very long descriptions (copy-pasted meeting notes).","root_cause_hypothesis":"The v2.3 release likely introduced a regression in the description search path — most probably a dropped or changed full-text index on the description field, a switch from indexed search to brute-force substring scanning, or a new search algorithm that doesn't scale with large text bodies. The fact that title search is unaffected and the CPU spikes heavily suggests the app is doing an unoptimized sequential scan over all 5,000 description fields in-process rather than using a database index.","reproduction_steps":["Install TaskFlow v2.3","Populate the database with ~5,000 tasks, including some with long descriptions (e.g., multi-paragraph text)","Perform a search that targets task descriptions (not just titles)","Observe 10-15 second latency and high CPU usage","Compare with TaskFlow v2.2 on the same dataset to confirm regression"],"environment":"TaskFlow v2.3, upgraded from v2.2. Work laptop (specific OS/specs not provided but likely not the bottleneck given v2.2 worked fine). ~5,000 tasks, some with very long description fields.","severity":"high","impact":"Any user with a moderately large task database (~5,000+ tasks) will experience unusable description search performance after upgrading to v2.3. Title search still works as a partial workaround, but description search is a core feature. Heavy CPU usage may also degrade other application responsiveness during searches.","recommended_fix":"Diff the search implementation between v2.2 and v2.3 to identify what changed in the description search code path. Likely candidates: (1) check if a full-text index on the description column was dropped or altered in a migration, (2) check if the search query was changed from an indexed lookup to a LIKE/regex scan, (3) check if description content is now being loaded into memory and searched in-process rather than at the database level. Restore indexed search behavior and verify with a 5,000+ task dataset.","proposed_test_case":"Performance regression test: populate a test database with 5,000 tasks (including 100+ tasks with descriptions over 1,000 characters). Run a description search and assert that results return in under 1 second. Run this test against both v2.2 and v2.3 to confirm the regression and validate the fix.","information_gaps":["Exact OS and hardware specs of the reporter's laptop (unlikely to matter given v2.2 worked fine)","Whether the v2.3 changelog mentions any search-related changes","Whether the database backend is SQLite, PostgreSQL, or another engine (affects index investigation)"],"clarity_scores":{"symptom":0.9,"cause":0.8,"reproduction":0.8,"impact":0.75,"overall_ambiguity":0.17}}

---
