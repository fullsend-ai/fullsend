# Conversation: slow-search x omc-deep-interview

## Issue

**Search is really slow**

The search feature used to be fast but now it takes forever. Sometimes it takes 10-15 seconds to get results. I have a lot of tasks but this seems excessive. Running TaskFlow on my work laptop.

---

## Comments

### triage-agent

Thanks for reporting this! To help us narrow down the issue — when you say the search is slow, which search are you referring to? For example, is it the main search bar at the top, a filtered view within a project, or something else? Also, does the slowness happen with every search term, or only with certain queries (like short keywords vs. longer phrases)?

---

### reporter

It's the main search bar at the top. I think it might be slow mainly when searching through task descriptions — like when I search for something that's in the body of a task rather than the title. Searching by title still seems pretty quick actually. I have about 5,000 tasks and some of them have really long descriptions (I paste meeting notes into them sometimes), so maybe that's related? It pretty much happens every time I do a description search, not just with certain keywords.

---

### triage-agent

That's really helpful context — searching through 5,000 task descriptions with long content is a great lead. You mentioned it used to be fast. Can you recall roughly when the slowdown started? For instance, did it coincide with a TaskFlow update, a big import of tasks, or around the time you started pasting meeting notes into descriptions? Knowing what changed around that time would help us figure out whether this is a data-volume issue or something that broke in a recent release.

---

### reporter

I think it started about 2 weeks ago, around when I upgraded from v2.2 to v2.3. I've been pasting meeting notes for a while now so I don't think that's what changed. The timing lines up pretty well with the upgrade actually. Before that it was always snappy, like under a second.

---

### triage-agent

That timing with the v2.3 upgrade is a strong clue — thanks for pinpointing that. One thing I want to double-check: are you using TaskFlow in a browser (and if so, which one?) or the desktop app? Also, when the search is slow, if you open your browser's developer tools (F12 → Network tab) and run a description search, does the network request itself take the full 10-15 seconds to respond, or does the response come back quickly but the results take a while to appear on screen? That would help us figure out whether the bottleneck is on the server side or in the UI rendering.

---

### reporter

It's the desktop app, not a browser, so I can't really check the network tab thing. But I can tell you that when I search, I can hear the laptop fan spin up and if I check the system monitor, one CPU core goes to 100% for the whole time the search is running. So it seems like it's doing something heavy locally rather than a UI issue. Hope that helps narrow it down.

---

### triage-agent

[RESOLVED] {"title":"Description search regression in v2.3: CPU-bound full-text scan causes 10-15s delays on large task sets","problem":"After upgrading from v2.2 to v2.3, searching by task description via the main search bar takes 10-15 seconds (previously sub-second). Title search remains fast. During the search, a single CPU core pins at 100%, indicating a CPU-bound local computation bottleneck rather than a network or UI rendering issue.","root_cause_hypothesis":"The v2.3 upgrade likely introduced a regression in the description search code path — possibly replacing an indexed or optimized search with a naive full-text scan over all task description content. The fact that title search is unaffected suggests the two search paths diverged in v2.3. The CPU saturation on a single core also suggests the search may not be leveraging any indexing or is performing expensive operations (e.g., regex over raw text) synchronously on the main thread.","reproduction_steps":["Install TaskFlow v2.3 desktop app","Create or import ~5,000 tasks, with some containing long descriptions (e.g., pasted meeting notes)","Use the main search bar to search for a term that appears in task descriptions but not titles","Observe 10-15 second delay with high CPU usage on one core","Repeat on v2.2 to confirm the search returns in under 1 second with the same dataset"],"environment":"TaskFlow v2.3 desktop app, work laptop (OS not specified), ~5,000 tasks with long descriptions","severity":"high","impact":"Any user with a large task set is effectively unable to use description search. The 10-15 second delay on every search makes the feature impractical. Title search still works as a partial workaround but limits search capability significantly.","recommended_fix":"Compare the description search implementation between v2.2 and v2.3 — look for changes to indexing, query execution, or full-text search logic in the desktop app's local search path. Likely candidates: removal of a search index, switch from indexed lookup to sequential scan, or a new preprocessing step (e.g., parsing/rendering description content before matching). Consider profiling the search with a ~5,000 task dataset to pinpoint the hot path. If the desktop app uses a local database (e.g., SQLite), check whether description search queries changed or an index was dropped.","proposed_test_case":"Performance test: with 5,000 tasks (including tasks with 2,000+ character descriptions), a description search must return results in under 2 seconds. This test should run against both the current and previous versions to catch regressions.","information_gaps":["Exact operating system and hardware specs (unlikely to change diagnosis but could affect benchmarking)","Whether other v2.3 users with large task sets experience the same issue (would confirm it's not environment-specific)","v2.3 changelog — what specifically changed in the search subsystem"]}

---
