# Conversation: wrong-search-results x socratic-refinement

## Issue

**Search shows wrong results — finding archived tasks instead of active ones**

Something is wrong with search. When I search for tasks, I'm getting results that include old archived tasks that should be hidden, and I'm not seeing some active tasks that I know exist. For example, I searched for 'Q2 planning' and got an archived task from last year but not the active one I created yesterday. This is happening for all my searches. My teammate says she sees the same thing.

---

## Comments

### triage-agent

Thanks for the detailed report — that's definitely not the behavior you should be seeing.

To help me understand the timeline: was search working correctly for you before this started? If so, do you recall roughly when you first noticed archived tasks showing up — was it today, this week, or has it been going on for a while? And did anything change around that time, like a product update or changes to how your team uses archiving?

---

### reporter

Yes, search was working perfectly fine before this. I first noticed the problem about three days ago — it started right after the system was updated to v2.3.1. I remember because we got a notification about a patch update and then shortly after that my searches started returning weird results. Nothing changed on our end in terms of how we use archiving. It's definitely tied to that update.

---

### triage-agent

That's really helpful — the v2.3.1 timing is a strong signal.

I'd like to understand a bit more about what you're seeing in the results themselves. When those archived tasks show up in search, are they visually marked as archived (like grayed out or tagged), or do they look just like active tasks? And for the active tasks that are missing — if you navigate to them directly (not through search), are they still there and working normally?

This will help me figure out whether the search index itself got scrambled during the update or whether it's more of a filtering issue.

---

### reporter

Good question — yes, the archived tasks that show up in search results are clearly marked as archived. They have the 'Archived' tag on them and appear grayed out like they normally do. So it's not like they've been unarchived or anything. And yes, the active tasks that are missing from search are definitely still there — I can find them by browsing my task list directly. They show up as active and everything looks normal when I open them. It's only when I use search that things are wrong. It's like the search filter is backwards — showing me archived tasks when I want active ones, and hiding the active ones.

---

### triage-agent

[RESOLVED] {"title":"Search results inverted after v2.3.1: archived tasks shown, active tasks hidden","problem":"Since the v2.3.1 update (approximately 3 days ago), search returns archived tasks instead of active ones. Active tasks that exist and are accessible via direct browsing do not appear in search results, while archived tasks (correctly displayed as archived/grayed out) appear instead. The issue affects all searches for all users — at least two users have confirmed the behavior.","root_cause_hypothesis":"The v2.3.1 update likely introduced an inverted filter condition in the search query logic. The archived/active status filter appears to be negated — including archived tasks and excluding active ones, when it should do the opposite. Since archived tasks render correctly with their archived styling, the data model and display layer are unaffected; only the search index query or filter predicate is wrong.","reproduction_steps":["Log in to TaskFlow on a version running v2.3.1","Create or confirm an active task exists with a known keyword (e.g., 'Q2 planning')","Confirm an archived task also exists with the same or similar keyword","Use the search function to search for that keyword","Observe that the archived task appears in results (grayed out, tagged as Archived) while the active task is absent","Navigate to the task list directly and confirm the active task exists and is accessible"],"environment":"TaskFlow v2.3.1 (post-patch update). Multiple users affected across at least one team. No user-side configuration or workflow changes preceded the issue.","severity":"high","impact":"Search is effectively broken for all users — the primary discovery mechanism returns wrong results. Users cannot find active tasks via search and must resort to manual browsing. This degrades productivity for any team relying on search for task management.","recommended_fix":"Diff the search query/filter logic between v2.3.0 and v2.3.1. Look for an inverted boolean condition, negated predicate, or flipped enum comparison on the archived/active status field in the search index query. Likely candidates: a `!is_archived` that became `is_archived`, a filter direction reversal, or a migration that flipped the meaning of a status flag in the search index without updating the query layer.","proposed_test_case":"Given a dataset with both active and archived tasks sharing a common keyword: when searching for that keyword with default filters, assert that only active tasks appear in results and no archived tasks are included. Additionally, regression-test that explicitly filtering for archived tasks still works correctly.","information_gaps":["Exact search interface used (web, mobile, API) — unlikely to matter if the bug is in a shared backend query layer","Whether any specific search filter combinations produce correct results (e.g., explicitly selecting 'all statuses')","Whether the search index was rebuilt during the v2.3.1 update or if this is purely a query-side issue"]}

---
