# Conversation: wrong-search-results x socratic-refinement

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for reporting this — that sounds really frustrating, especially when you know the task is there but search won't surface it.

To help me understand the full picture: can you walk me through what you were doing when you noticed this? Specifically, were you searching from the main search bar, a filtered view, or somewhere else in the app? And has search ever worked correctly for you — if so, do you have a sense of when it stopped behaving as expected?

---

### reporter

Sure! I was using the main search bar at the top of the dashboard — that's how I always search. I had the default filters on, which I believe is 'Active tasks only.' I noticed the problem about three days ago, right after our team updated to v2.3.1. Before that update, search was working perfectly fine for me. Since the update, every search I do seems to pull up archived tasks instead of my active ones. I have a lot of archived tasks (around 300) and about 150 active ones, so the results are very noticeably wrong — I'm wading through old completed work instead of seeing my current tasks.

---

### triage-agent

That timing with the v2.3.1 update is really helpful — it gives us a clear boundary to work with.

You mentioned the default filter is what you believe is 'Active tasks only.' I'm curious about that — after the update, does the filter control still look the same as it did before? And if you try explicitly toggling it (say, switching to 'All tasks' and then back to 'Active tasks only'), do the search results change at all, or do you get the same archived results regardless of what the filter is set to?

---

### reporter

Good question! Yes, the filter control looks the same as before — nothing visually changed there. And actually, when I toggle it, the results DO change, but in a weird way. When I switch to 'Archived tasks,' I suddenly see my active tasks — the ones I was looking for! And when I switch back to 'Active tasks only,' I get the archived ones again. It's like the filter is working backwards. If I set it to 'All tasks,' I see everything, which is correct, but it's a jumbled mix of active and archived. The individual task cards still show the correct status though — like if I click into a task that showed up under 'Active tasks only,' it's clearly marked as archived when I look at it. So the tasks themselves know their correct status, it's just the search filter that seems inverted.

---

### triage-agent

[RESOLVED] {"title":"Search filter inverted since v2.3.1: 'Active tasks only' returns archived tasks and vice versa","problem":"After updating to v2.3.1, the main search bar's status filter is inverted. Selecting 'Active tasks only' returns archived tasks, and selecting 'Archived tasks' returns active tasks. 'All tasks' works correctly but without proper ordering. The issue affects all searches for all users on v2.3.1.","root_cause_hypothesis":"The v2.3.1 update likely introduced an inverted boolean or swapped enum mapping in the search query's status filter predicate. Since task records store correct status (visible when clicking into a task) and 'All tasks' returns everything, the data layer is intact — the bug is isolated to how the filter selection is translated into the search query. A likely candidate is a negated condition (e.g., `status == 'archived'` where it should be `status == 'active'`) or swapped enum values in the filter-to-query mapping.","reproduction_steps":["Log in to a TaskFlow instance running v2.3.1 with a mix of active and archived tasks","Use the main search bar on the dashboard with the default 'Active tasks only' filter","Search for a term that matches both active and archived tasks (e.g., 'Q2 planning')","Observe that archived tasks appear in results instead of active ones","Toggle filter to 'Archived tasks' and observe that active tasks now appear","Toggle filter to 'All tasks' and observe that all tasks appear correctly","Click into any result to confirm the task's own status label is correct (data integrity is fine)"],"environment":"TaskFlow v2.3.1, main search bar on dashboard, default filters. Confirmed by multiple users. User has ~300 archived and ~150 active tasks. Worked correctly on the version prior to v2.3.1.","severity":"high","impact":"All users on v2.3.1 are affected. Search — a core workflow — returns wrong results by default, forcing users to either mentally invert the filter or wade through 'All tasks' unfiltered. Users with many archived tasks (like this reporter with 300) are especially impacted as archived results bury active work.","recommended_fix":"Diff the search filter/query logic between v2.3.0 and v2.3.1. Look for an inverted condition or swapped enum/constant in the code path that maps the UI filter selection ('Active tasks only' / 'Archived tasks') to the database or API query predicate. The fix is almost certainly a one-line boolean or enum swap. Check for similar inversions in any other status-based filters that may have been touched in the same changeset.","proposed_test_case":"Add a test with a mix of active and archived tasks that asserts: (1) filtering by 'Active tasks only' returns exactly the active tasks, (2) filtering by 'Archived tasks' returns exactly the archived tasks, (3) filtering by 'All tasks' returns both. This should be a regression test gated on the search query builder.","information_gaps":["Exact code change in v2.3.1 that introduced the inversion (requires inspecting the diff)","Whether other filter types (e.g., by priority, assignee) were similarly affected by the same changeset"]}

---
