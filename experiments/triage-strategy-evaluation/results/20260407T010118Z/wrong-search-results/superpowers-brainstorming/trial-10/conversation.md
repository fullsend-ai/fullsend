# Conversation: wrong-search-results x superpowers-brainstorming

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for reporting this! To make sure I understand the expected behavior: when you search for tasks, should archived tasks **never** appear in results, or would you expect them to be hidden by default but available with a filter (e.g., an "include archived" toggle)?

Also — is there a search filter or setting you're currently using, or are you just typing into the main search bar with default settings?

(a) Archived tasks should never appear in search results
(b) Archived tasks should be hidden by default but filterable
(c) I'm not sure — they just weren't showing up before and now they are
(d) Something else

---

### reporter

It's (b) — there's an archive filter in the search, and by default it shows only active tasks. You can toggle it to show archived ones too. I'm just using the default settings, so I should be seeing only active tasks, but instead I'm getting archived ones showing up and my actual active tasks are missing from the results. It's like the filter is doing the opposite of what it should.

---

### triage-agent

That's really helpful — it does sound like the filter logic might be inverted. One more question that would help the team track this down: when did you first notice this behavior?

(a) It started recently — search was working correctly before and broke in the last few days/weeks
(b) I'm not sure when it started, but I only just noticed it
(c) It's been like this for as long as I can remember
(d) It started after a specific event (e.g., an update, a settings change)

---

### reporter

It's (a) — it started recently. I'm pretty sure it was about three days ago. Actually, I think there was a system update around that time — we went to version 2.3.1. Search was working perfectly fine before that. So it might be (d) too, since it seems to line up with that update.

---

### triage-agent

Thanks — the v2.3.1 timing is a great clue. I have a hypothesis I'd like to confirm: it sounds like the archive filter might be **inverted** (showing archived when it should show active, and vice versa). Could you try a quick test?

In the search bar, toggle the archive filter to **"include archived"** (or however it's labeled), then search for "Q2 planning" again. What happens?

(a) Now I see my active task but **not** the archived one — it's definitely inverted
(b) Now I see **both** active and archived tasks — so it's not a simple inversion
(c) I still see the same wrong results regardless of the toggle
(d) Something else

---

### reporter

I just tried that. When I toggle the filter to show archived tasks, I actually see my active tasks instead! So it's (a) — it's definitely inverted. With the default 'active only' filter I get all my old archived tasks (I have a lot of those, maybe around 300), and when I switch to the archived view I see my actual active tasks. The filter is just doing the exact opposite of what it says.

---

### triage-agent

[RESOLVED] {"title":"Search archive filter is inverted since v2.3.1 — 'active only' shows archived tasks and vice versa","problem":"The search archive filter is applying the opposite logic: the default 'active only' setting returns archived tasks, while toggling to 'include archived' returns active tasks instead. This affects all users and all searches.","root_cause_hypothesis":"The v2.3.1 update introduced a boolean inversion in the archive filter logic — likely a negation error or swapped enum/flag values in the search query builder that determines which tasks to include based on the filter state.","reproduction_steps":["Update to v2.3.1","Open the search bar with default settings (active tasks only)","Search for a term that matches both an archived task and an active task (e.g., 'Q2 planning')","Observe that archived tasks appear and active tasks are missing","Toggle the archive filter to 'include archived'","Observe that now only active tasks appear"],"environment":"TaskFlow v2.3.1, confirmed by multiple users (reporter and teammate)","severity":"high","impact":"All users are affected. Default search returns stale archived results instead of active tasks, making search effectively unusable without the workaround of manually inverting the toggle. Users with many archived tasks (reporter has ~300) see severe result pollution.","recommended_fix":"Diff the search filter logic between v2.3.0 and v2.3.1. Look for an inverted boolean, negation error, or swapped condition in the code path that translates the archive filter UI state into the search query predicate. The fix is likely a one-line boolean flip.","proposed_test_case":"Unit test: given a dataset with both active and archived tasks, assert that a search with the default 'active only' filter returns only active tasks and excludes archived ones, and that the 'include archived' filter returns both. Integration test: verify the filter parameter is correctly passed from the UI toggle through to the query layer.","information_gaps":["Exact code path changed in v2.3.1 that touched search or archive filter logic — discoverable from the v2.3.1 changelog or diff"]}

---
