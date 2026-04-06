# Conversation: slow-search x structured-triage

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help us investigate the slowness, could you walk us through the steps to reproduce it? Specifically: What are you searching for (a keyword, a tag, a filter combination)? And roughly how many tasks do you have in your account? For example: "I type a word into the search bar on the Tasks page and press Enter" — that level of detail would be really helpful.

---

### reporter

Sure! I just type a keyword into the search bar on the main Tasks page and hit Enter. For example, I'll search for something like "budget" or "quarterly review" — words that appear in my task descriptions. I have about 5,000 tasks built up over the past couple years. It used to come back almost instantly, but now it sits there for 10-15 seconds before showing results. Interestingly, if I search for something that's in a task title, it seems to come back pretty quick still. It's the searches that dig into the task descriptions that are painfully slow.

---

### triage-agent

Thanks for the detailed reproduction info — that distinction between title vs. description searches is a really useful clue!

Could you let us know a few details about your environment?

- **OS:** e.g., Windows 11, macOS Sonoma, Ubuntu 24.04
- **Browser:** e.g., Chrome 124, Firefox 126, Safari 18
- **TaskFlow version:** You can usually find this under Settings → About or in the page footer

This will help us narrow down whether the slowness is tied to a specific platform or release.

---

### reporter

I'm on Ubuntu 22.04, running on a ThinkPad T14. I'm not sure which browser matters since TaskFlow is a desktop app for me, but I access the web UI through Firefox. As for the version, I'm on v2.3 — I actually upgraded from v2.2 about two weeks ago, which now that I think about it, is right around when the slowness started. Not sure if that's related or just a coincidence.

---

### triage-agent

[RESOLVED] {"title":"Task description search degraded to 10-15s after v2.3 upgrade (~5,000 tasks)","problem":"Full-text search against task descriptions takes 10-15 seconds to return results, whereas searching task titles remains fast. The reporter has approximately 5,000 tasks. The slowness began around the time of upgrading from TaskFlow v2.2 to v2.3.","root_cause_hypothesis":"The v2.3 release likely introduced a regression in how description searches are executed — possibly a missing or dropped database index on the task descriptions column, a change from indexed full-text search to unindexed LIKE/ILIKE queries, or a search implementation change that now scans descriptions without leveraging a full-text index. The fact that title searches remain fast suggests title indexing is intact while description indexing is not.","reproduction_steps":["Have an account with a large number of tasks (~5,000)","Navigate to the main Tasks page","Type a keyword that appears in task descriptions (e.g., \"budget\" or \"quarterly review\") into the search bar","Press Enter","Observe that results take 10-15 seconds to appear","Repeat with a keyword that appears only in task titles and observe that results return quickly"],"environment":"Ubuntu 22.04, ThinkPad T14, Firefox, TaskFlow v2.3 (upgraded from v2.2 approximately two weeks ago)","severity":"high","impact":"Users with large task histories experience unusable search performance when searching by description content — a core workflow. Likely affects all users with non-trivial task counts on v2.3.","recommended_fix":"1. Diff the v2.2→v2.3 changes to search and database migration code. 2. Check whether a full-text index on the task descriptions column exists in v2.3 (it may have been dropped or not created by a migration). 3. Run EXPLAIN/ANALYZE on the description search query against a dataset of ~5,000 tasks to confirm whether it's doing a sequential scan. 4. Restore or add the appropriate full-text index on task descriptions.","proposed_test_case":"Performance test: execute a description keyword search against a dataset of 5,000+ tasks and assert results return within an acceptable threshold (e.g., under 1 second). Include a regression test that verifies the full-text index on the descriptions column exists after running all migrations.","information_gaps":["No error messages or logs from the reporter (may not be visible in the UI)","Exact v2.3 patch version not confirmed","Server-side vs. client-side timing not isolated (could profile with browser dev tools or server logs)"]}

---

