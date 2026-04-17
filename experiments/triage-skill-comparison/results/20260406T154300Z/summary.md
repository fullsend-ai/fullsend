# Triage Skill Comparison: Results Summary

Generated: 2026-04-06T19:43:13Z

## Scores by Strategy x Scenario

Each cell shows **mean +/- stddev** over N trials.

| Strategy | auth-redirect-loop | crash-on-save | slow-search | **Average** |
|---|---|---|---|---|
| omc-deep-interview | 3.56 +/- 0.51 (n=10) | 3.75 +/- 1.24 (n=10) | 4.82 +/- 0.15 (n=10) | **4.04** |
| omo-prometheus | 3.42 +/- 0.59 (n=10) | 4.00 +/- 0.72 (n=10) | 4.81 +/- 0.20 (n=10) | **4.08** |
| socratic-refinement | 3.18 +/- 0.16 (n=10) | 3.74 +/- 0.43 (n=10) | 4.31 +/- 0.75 (n=10) | **3.74** |
| structured-triage | 2.98 +/- 0.31 (n=10) | 3.04 +/- 1.39 (n=10) | 4.45 +/- 0.20 (n=10) | **3.49** |
| superpowers-brainstorming | 3.29 +/- 0.12 (n=10) | 3.80 +/- 0.69 (n=10) | 3.26 +/- 0.72 (n=10) | **3.45** |

## Detailed Results

### Scenario: auth-redirect-loop

#### Strategy: omc-deep-interview

**Weighted total (mean):** 3.56 +/- 0.51 over 10 trials

| Trial | Score | Turns |
|---|---|---|
| 1 | 3.55 | 1 |
| 2 | 3.8 | 2 |
| 3 | 3.8 | 1 |
| 4 | 3.30 | 1 |
| 5 | 3.20 | 1 |
| 6 | 3.40 | 1 |
| 7 | 3.15 | 1 |
| 8 | 3.30 | 1 |
| 9 | 3.30 | 1 |
| 10 | 4.85 | 3 |

<details>
<summary>Rubric breakdown (trial 1)</summary>

| Criterion | Score | Rationale |
|---|---|---|
| completeness | 4 | The triage captured most expected information: ~30% affected users, the plus-addressing/alias pattern, incognito/cookie- |
| accuracy | 3 | The triage correctly identified the email normalization mismatch as a contributing factor, but this is actually the seco |
| efficiency | 4 | Only one follow-up question was asked, and it was well-targeted — combining two important lines of inquiry (pattern am |
| question_quality | 4 | The single question was well-constructed and multi-pronged, asking about user patterns (which yielded the plus-addressin |
| actionability | 3 | The recommended fix addresses the email normalization issue well and would partially resolve the problem. However, since |

**Strengths:**
- Efficiently combined multiple diagnostic questions into a single well-structured turn
- Correctly identified the email normalization mismatch pattern from the reporter's response
- Comprehensive recommended fix steps and clear reproduction steps for the email mismatch issue
- Good identification of information gaps that would guide further investigation

**Weaknesses:**
- Missed the primary root cause: SameSite=Strict cookie policy dropping session cookies on cross-origin redirects
- Did not follow up on the cookie/incognito clue — this was a strong signal pointing toward cookie policy issues
- Incorrectly attributed the cookie behavior to 'stale Okta session cookies' rather than investigating the actual cookie-setting mechanism
- Resolved too quickly after only one exchange when the cookie behavior warranted deeper investigation

**Most insightful question:** Have you noticed any pattern among the affected users versus the ones who can log in fine? — This efficiently uncovered the plus-addressing and email alias pattern, which was a key piece of the puzzle.

</details>

---

#### Strategy: omo-prometheus

**Weighted total (mean):** 3.42 +/- 0.59 over 10 trials

| Trial | Score | Turns |
|---|---|---|
| 1 | 3.45 | 2 |
| 2 | 3.55 | 1 |
| 3 | 3.15 | 1 |
| 4 | 3.30 | 1 |
| 5 | 5.00 | 2 |
| 6 | 3.15 | 1 |
| 7 | 3.35 | 1 |
| 8 | 2.90 | 1 |
| 9 | 3.05 | 1 |
| 10 | 3.30 | 1 |

<details>
<summary>Rubric breakdown (trial 1)</summary>

| Criterion | Score | Rationale |
|---|---|---|
| completeness | 3 | The triage captured several expected items: ~30% affected users, pattern among affected users (plus addressing/aliases), |
| accuracy | 3 | The email claim mismatch is correctly identified and well-documented. However, the root cause hypothesis is only partial |
| efficiency | 4 | Only 2 question rounds were needed before producing a summary. The first question efficiently targeted the two most impo |
| question_quality | 4 | The first question was well-crafted — asking about user patterns and cookie clearing in one turn was efficient and dir |
| actionability | 4 | The summary is detailed and well-structured with clear reproduction steps, recommended fixes, and test cases. A develope |

**Strengths:**
- Efficiently identified the email plus-addressing pattern in the first question round
- Provided a very detailed and well-structured summary with reproduction steps, recommended fixes, and test cases
- Good diagnostic intuition asking about user patterns and cookie clearing together
- Correctly identified the email claim mismatch and explained why it worked under Okta

**Weaknesses:**
- Completely missed the SameSite=Strict cookie issue, which the ground truth identifies as the primary cause
- When the reporter explicitly flagged that cookies weren't sticking, the agent acknowledged it but didn't investigate further
- Root cause hierarchy is inverted — email mismatch is treated as primary when it's actually secondary to the cookie issue
- Closed the triage prematurely when the reporter was clearly signaling there was a second problem

**Most insightful question:** Is there a pattern among affected users? For example, are they in specific Entra ID groups, assigned specific roles in TaskFlow, or were they perhaps active sessions during the migration?

</details>

---

#### Strategy: socratic-refinement

**Weighted total (mean):** 3.18 +/- 0.16 over 10 trials

| Trial | Score | Turns |
|---|---|---|
| 1 | 3.15 | 1 |
| 2 | 3.35 | 1 |
| 3 | 3.30 | 1 |
| 4 | 3.35 | 1 |
| 5 | 3.20 | 1 |
| 6 | 3.15 | 1 |
| 7 | 2.90 | 1 |
| 8 | 3.35 | 1 |
| 9 | 3.15 | 1 |
| 10 | 2.95 | 1 |

<details>
<summary>Rubric breakdown (trial 1)</summary>

| Criterion | Score | Rationale |
|---|---|---|
| completeness | 3 | Captured 6 of 8 expected items: the 30% split, plus-addressing/alias pattern, email claim mismatch, incognito workaround |
| accuracy | 3 | The email normalization mismatch hypothesis is correct but is only the secondary cause per the ground truth. The primary |
| efficiency | 3 | Only one question was asked before resolving, which is extremely fast but premature. The single question successfully su |
| question_quality | 4 | The single question asked was genuinely well-crafted: open-ended, multi-dimensional (timing, roles, groups, browsers, se |
| actionability | 3 | The summary is well-structured with clear reproduction steps, a recommended fix, proposed test cases, and identified inf |

**Strengths:**
- Excellent opening question that was open-ended and multi-faceted, successfully eliciting the email pattern
- Well-structured and detailed triage summary with reproduction steps, recommended fix, and test cases
- Correctly identified the email normalization difference between Okta and Entra ID
- Appropriately flagged information gaps including the exact claim used for matching

**Weaknesses:**
- Completely missed the primary root cause: SameSite=Strict cookie being dropped on cross-origin redirect from Entra ID back to TaskFlow
- Resolved after only one question, not investigating the incognito clue which pointed directly to a cookie/browser issue
- Misattributed the incognito workaround to stale Okta session cookies rather than different cookie handling behavior
- Never asked about browser developer tools observations, network tab behavior, or cookie headers

**Most insightful question:** The opening question asking about commonalities among affected users with multiple suggested dimensions (join date, roles, browser, active sessions) — it successfully surfaced the plus-addressing pattern that the reporter might not have connected otherwise.

</details>

---

#### Strategy: structured-triage

**Weighted total (mean):** 2.98 +/- 0.31 over 10 trials

| Trial | Score | Turns |
|---|---|---|
| 1 | 3.15 | 1 |
| 2 | 3.20 | 1 |
| 3 | 3.00 | 1 |
| 4 | 3.30 | 1 |
| 5 | 2.80 | 1 |
| 6 | 3.00 | 1 |
| 7 | 2.25 | 1 |
| 8 | 3.20 | 1 |
| 9 | 2.80 | 1 |
| 10 | 3.15 | 1 |

<details>
<summary>Rubric breakdown (trial 1)</summary>

| Criterion | Score | Rationale |
|---|---|---|
| completeness | 3 | The triage captured the ~30% affected rate, the plus-addressing and email alias pattern, the redirect URI verification,  |
| accuracy | 3 | The triage correctly identified the email claim mismatch as a contributing factor, but missed the primary root cause ent |
| efficiency | 3 | Only 1 question turn before resolving, which is fast but arguably too fast. The agent resolved after getting email patte |
| question_quality | 4 | The initial question was well-structured and multi-part, covering browser, version, SSO config type, and user patterns.  |
| actionability | 3 | A developer could investigate the email matching logic based on this summary, which would eventually surface the seconda |

**Strengths:**
- Efficiently identified the email pattern correlation (plus addressing and aliases) from a single well-crafted question
- Good reproduction steps and proposed test cases for the email mismatch aspect
- Correctly identified this as high severity with complete access blocking impact
- Acknowledged information gaps honestly rather than overcommitting to the hypothesis

**Weaknesses:**
- Completely missed the SameSite=Strict cookie issue, which is the primary root cause
- Resolved too quickly — one more diagnostic question about cookie/session behavior would have been highly valuable
- Did not ask about workarounds (incognito, clearing cookies) which would have pointed directly to the cookie issue
- Did not ask about browser dev tools observations (Set-Cookie headers, network tab) which the reporter could have checked

**Most insightful question:** Since only ~30% of your team is affected, any pattern you've noticed distinguishing those users from the ones who can log in fine would be especially helpful.

</details>

---

#### Strategy: superpowers-brainstorming

**Weighted total (mean):** 3.29 +/- 0.12 over 10 trials

| Trial | Score | Turns |
|---|---|---|
| 1 | 3.30 | 1 |
| 2 | 3.30 | 1 |
| 3 | 3.35 | 1 |
| 4 | 3.55 | 1 |
| 5 | 3.15 | 1 |
| 6 | 3.30 | 1 |
| 7 | 3.35 | 1 |
| 8 | 3.15 | 1 |
| 9 | 3.20 | 1 |
| 10 | 3.30 | 1 |

<details>
<summary>Rubric breakdown (trial 1)</summary>

| Criterion | Score | Rationale |
|---|---|---|
| completeness | 3 | The triage captured ~30% affected users, the plus-addressing/alias email pattern, email claim mismatch between Okta and  |
| accuracy | 3 | The triage correctly identified the email claim mismatch as a contributing factor, which is accurate per the ground trut |
| efficiency | 4 | Only one exchange before producing the summary. The initial question was well-structured and covered multiple hypotheses |
| question_quality | 4 | The opening question was well-structured with multiple hypotheses presented clearly. Asking about incognito was insightf |
| actionability | 3 | A developer could start investigating from this summary, and the recommended fix steps are reasonable. However, because  |

**Strengths:**
- Excellent first question that efficiently probed multiple hypotheses and elicited the email pattern and incognito workaround in a single exchange
- Comprehensive and well-structured summary with clear reproduction steps, severity assessment, and recommended fixes
- Correctly identified the email claim mismatch between Okta and Entra ID as a contributing factor
- Good recognition that other SSO apps working fine points to a TaskFlow-specific issue

**Weaknesses:**
- Missed the primary root cause: SameSite=Strict cookie setting preventing session cookies from being set on cross-origin redirects
- Mischaracterized the cookie issue as 'stale Okta-era sessions' rather than investigating the actual cookie mechanism
- Resolved after only one exchange — a follow-up question about browser dev tools or network inspection would likely have uncovered the SameSite issue
- Inverted the priority of the two root causes (email mismatch is secondary, SameSite is primary)

**Most insightful question:** The incognito/private window question was the most insightful — it directly surfaced evidence of the cookie-related component of the bug and could have led to discovering the SameSite issue with one more follow-up.

</details>

---

### Scenario: crash-on-save

#### Strategy: omc-deep-interview

**Weighted total (mean):** 3.75 +/- 1.24 over 10 trials

| Trial | Score | Turns |
|---|---|---|
| 1 | 2.10 | 2 |
| 2 | 4.80 | 2 |
| 3 | 4.15 | 3 |
| 4 | 4.35 | 2 |
| 5 | 4.85 | 3 |
| 6 | 4.85 | 2 |
| 7 | 2.05 | 2 |
| 8 | 4.15 | 2 |
| 9 | 1.85 | 1 |
| 10 | 4.35 | 2 |

<details>
<summary>Rubric breakdown (trial 1)</summary>

| Criterion | Score | Rationale |
|---|---|---|
| completeness | 1 | The final summary contains none of the expected information. Despite the conversation successfully extracting nearly all |
| accuracy | 1 | The summary states root cause as 'unknown', environment as 'unknown', and problem as 'Agent could not produce JSON' —  |
| efficiency | 4 | The conversation itself was efficient. Two well-targeted questions extracted most of the key information in just 2 turns |
| question_quality | 4 | The first question appropriately asked about error messages and environment. The second question was insightful — aski |
| actionability | 1 | The summary is completely useless for a developer. It says 'Manual triage required' and provides no reproduction steps,  |

**Strengths:**
- Conversational questioning was well-structured and empathetic
- Second question showed genuine diagnostic reasoning by hypothesizing content-specific triggers
- Efficiently extracted most key information in only 2 agent turns

**Weaknesses:**
- Complete failure to produce a usable summary — all gathered information was lost
- The parse error in the summary indicates a tooling or formatting failure that negated an otherwise good triage conversation
- No reproduction steps captured despite reporter providing clear ones
- Did not ask about the size threshold (the 64KB/large list aspect) explicitly, though the reporter partially volunteered it

**Most insightful question:** Does this crash happen every time you save, or only with certain tasks/documents? For example, if you create a brand new task with just a few plain English words and try to save it, does it still crash? I'm trying to figure out whether something specific in your content (like special characters, emoji, or pasted text from another app) might be triggering the encoding error.

</details>

---

#### Strategy: omo-prometheus

**Weighted total (mean):** 4.00 +/- 0.72 over 10 trials

| Trial | Score | Turns |
|---|---|---|
| 1 | 4.0 | 1 |
| 2 | 4.15 | 2 |
| 3 | 4.2 | 1 |
| 4 | 4.80 | 2 |
| 5 | 4.0 | 2 |
| 6 | 4.0 | 1 |
| 7 | 4.2 | 1 |
| 8 | 2.05 | 2 |
| 9 | 4.35 | 2 |
| 10 | 4.2 | 2 |

<details>
<summary>Rubric breakdown (trial 1)</summary>

| Criterion | Score | Rationale |
|---|---|---|
| completeness | 4 | The triage captured most key information: crash on manual save (toolbar), related to CSV import, encoding error message, |
| accuracy | 4 | The root cause hypothesis is close — it correctly identifies encoding issues in the CSV import path causing the save s |
| efficiency | 4 | Only 1 agent turn of questions before resolving — very efficient. The three questions asked were all essential and wel |
| question_quality | 4 | The three questions were well-structured and diagnostic: what's being saved, what happens on crash, and is this new beha |
| actionability | 4 | The summary provides clear reproduction steps, a reasonable root cause hypothesis, and specific recommended fixes target |

**Strengths:**
- Extremely efficient — extracted substantial information in a single well-crafted question turn
- Questions were structured to progressively narrow the problem space (what, how, when)
- Summary is well-organized with clear reproduction steps and actionable fix recommendations
- Correctly identified the CSV import as the trigger and encoding as the mechanism
- Properly noted information gaps rather than fabricating details

**Weaknesses:**
- Did not ask for OS/version/platform — listed it as an information gap instead of asking
- Missed the size-dependent nature of the bug (works with <50 tasks, fails with 200+)
- Did not ask whether auto-save also triggers the crash, which is a key diagnostic differentiator
- Did not ask about the specific content of the CSV (special characters like em-dashes, curly quotes)

**Most insightful question:** Is this new behavior? Was saving working for you before, and if so, do you remember roughly when it started failing?

</details>

---

#### Strategy: socratic-refinement

**Weighted total (mean):** 3.74 +/- 0.43 over 10 trials

| Trial | Score | Turns |
|---|---|---|
| 1 | 3.55 | 2 |
| 2 | 4.2 | 1 |
| 3 | 4.0 | 1 |
| 4 | 3.75 | 1 |
| 5 | 2.95 | 1 |
| 6 | 3.15 | 1 |
| 7 | 4.0 | 1 |
| 8 | 3.55 | 1 |
| 9 | 4.0 | 1 |
| 10 | 4.2 | 1 |

<details>
<summary>Rubric breakdown (trial 1)</summary>

| Criterion | Score | Rationale |
|---|---|---|
| completeness | 3 | Extracted 4 of 6 expected items: manual save vs auto-save distinction, CSV import with special characters, timing after  |
| accuracy | 3 | The hypothesis that encoding of typographic characters causes the crash is partially correct — encoding is involved. H |
| efficiency | 5 | Only 2 questions were asked, and both were productive. The first open-ended question drew out nearly all the key details |
| question_quality | 4 | Both questions were well-framed and diagnostic. The first question effectively invited the reporter to narrate the full  |
| actionability | 3 | The summary is well-structured with clear reproduction steps and a reasonable fix direction. However, the incomplete roo |

**Strengths:**
- Excellent efficiency — extracted substantial information in only 2 turns
- Smart follow-up asking the reporter to test removing imported tasks, confirming the causal link
- Well-structured final summary with clear reproduction steps and actionable fix recommendations
- Correctly identified the specific typographic characters involved (smart quotes, em-dashes)

**Weaknesses:**
- Failed to collect environment details (OS, app version) despite these being standard triage data
- Missed the size threshold entirely — never explored whether smaller lists with the same characters also crash
- Root cause hypothesis incorrectly frames the issue as a manual-save vs auto-save encoding discrepancy rather than a size-dependent encoding failure

**Most insightful question:** Can you tell me a bit about that CSV file? Specifically, where did it come from, and did any of the imported tasks contain special characters? Also, does the crash go away if you delete the imported tasks and try saving again?

</details>

---

#### Strategy: structured-triage

**Weighted total (mean):** 3.04 +/- 1.39 over 10 trials

| Trial | Score | Turns |
|---|---|---|
| 1 | 3.75 | 2 |
| 2 | 4.0 | 2 |
| 3 | 4.0 | 2 |
| 4 | 4.0 | 2 |
| 5 | 2.10 | 2 |
| 6 | 3.0 | 2 |
| 7 | 4.80 | 3 |
| 8 | 2.40 | 2 |
| 9 | N/A | ? |
| 10 | 2.40 | 2 |

<details>
<summary>Rubric breakdown (trial 1)</summary>

| Criterion | Score | Rationale |
|---|---|---|
| completeness | 3 | The triage captured that the crash happens on manual save, is related to CSV import, involves ~200 tasks, started after  |
| accuracy | 4 | The root cause hypothesis is close — it correctly identifies encoding issues in the CSV import path and the save seria |
| efficiency | 4 | The conversation took only 2 agent turns to reach a resolution, which is quite efficient. The first question effectively |
| question_quality | 4 | The first question was well-structured with specific prompts that successfully drew out the CSV import connection and th |
| actionability | 4 | The summary is well-structured with clear reproduction steps, a reasonable root cause hypothesis, and a concrete recomme |

**Strengths:**
- Extracted the CSV import connection and encoding error from a very vague initial report in just one question
- Well-structured final summary with reproduction steps, root cause hypothesis, and recommended fix
- Honest enumeration of information gaps, including actionable next steps like checking crash logs
- Efficient two-turn conversation that gathered most of the critical information

**Weaknesses:**
- Failed to distinguish between manual save and auto-save behavior — a key diagnostic signal
- Did not probe whether the crash is size-dependent (smaller lists with same data work fine)
- Root cause hypothesis misses the payload size threshold, focusing only on encoding

**Most insightful question:** The first question asking for exact steps before the crash, which successfully drew out the CSV import connection, the encoding error flash, and that it happens without any edits — none of which were in the original report.

</details>

---

#### Strategy: superpowers-brainstorming

**Weighted total (mean):** 3.80 +/- 0.69 over 10 trials

| Trial | Score | Turns |
|---|---|---|
| 1 | 4.35 | 2 |
| 2 | 4.6 | 2 |
| 3 | 2.55 | 1 |
| 4 | 3.25 | 1 |
| 5 | 2.80 | 1 |
| 6 | 4.2 | 2 |
| 7 | 4.0 | 1 |
| 8 | 4.0 | 2 |
| 9 | 4.15 | 1 |
| 10 | 4.15 | 2 |

<details>
<summary>Rubric breakdown (trial 1)</summary>

| Criterion | Score | Rationale |
|---|---|---|
| completeness | 4 | Extracted 5 of 6 expected items: manual save vs auto-save distinction, CSV import with special characters, relationship  |
| accuracy | 4 | The root cause hypothesis is close — correctly identifies encoding issues with special characters on the manual save p |
| efficiency | 5 | Only 2 agent questions were asked, and both were essential and well-targeted. The first question identified the save act |
| question_quality | 5 | Both questions were excellently structured with numbered options that guided the reporter without leading them. The seco |
| actionability | 4 | The summary provides clear reproduction steps, a reasonable root cause hypothesis, and a specific recommended fix pointi |

**Strengths:**
- Exceptional efficiency — extracted nearly all critical information in just 2 well-crafted questions
- The multiple-choice format with follow-up prompts made it easy for the reporter to provide detailed answers
- The second question about unusual content was perfectly targeted and drew out the CSV import connection
- Summary includes actionable reproduction steps and a specific fix recommendation

**Weaknesses:**
- Never asked about platform, OS version, or app version — missing basic environment information
- Did not explore the size-dependent aspect (works with small lists, fails with ~200 tasks)
- Root cause hypothesis attributes the difference to manual vs auto-save encoding paths, missing the payload size threshold as a contributing factor

**Most insightful question:** Does the affected task list contain anything unusual — like emoji, accented characters, pasted content from another app, or very long text?

</details>

---

### Scenario: slow-search

#### Strategy: omc-deep-interview

**Weighted total (mean):** 4.82 +/- 0.15 over 10 trials

| Trial | Score | Turns |
|---|---|---|
| 1 | 4.85 | 2 |
| 2 | 4.80 | 2 |
| 3 | 5.0 | 2 |
| 4 | 4.80 | 2 |
| 5 | 4.55 | 2 |
| 6 | 4.60 | 2 |
| 7 | 4.85 | 2 |
| 8 | 4.85 | 1 |
| 9 | 5.0 | 2 |
| 10 | 4.85 | 2 |

<details>
<summary>Rubric breakdown (trial 1)</summary>

| Criterion | Score | Rationale |
|---|---|---|
| completeness | 4 | Extracted ~5,000 tasks, description vs title search distinction, v2.2→v2.3 regression, ~2 weeks timing, 10-15s latency |
| accuracy | 5 | Root cause hypothesis is remarkably close to the actual cause — correctly identified a dropped/altered full-text index |
| efficiency | 5 | Only 2 agent questions to extract nearly all critical information. First question efficiently combined timing and scale. |
| question_quality | 5 | Both questions demonstrated genuine diagnostic reasoning. The second question — asking whether it depends on what you' |
| actionability | 5 | The summary provides a clear root cause hypothesis, specific reproduction steps with realistic parameters, a concrete de |

**Strengths:**
- Exceptional efficiency — only 2 questions to reach a highly accurate diagnosis
- Root cause hypothesis nearly exactly matches the actual cause (FTS index dropped, sequential scan on descriptions)
- The title/description distinction was surfaced through a well-crafted diagnostic question rather than luck
- Actionable summary with concrete debugging steps and a proposed regression test

**Weaknesses:**
- Did not capture CPU spike information (though this is a secondary detail)
- Did not ask about OS/hardware despite the reporter mentioning 'work laptop' — noted as info gap but could have been quickly clarified
- Listed database backend as an information gap but didn't ask about it despite it being relevant to the index hypothesis

**Most insightful question:** Is it *every* search that's slow, or does it depend on what you're searching for? For example, does a simple one-word search like 'meeting' take just as long as a more complex search with filters?

</details>

---

#### Strategy: omo-prometheus

**Weighted total (mean):** 4.81 +/- 0.20 over 10 trials

| Trial | Score | Turns |
|---|---|---|
| 1 | 5.00 | 2 |
| 2 | 4.35 | 1 |
| 3 | 5.00 | 1 |
| 4 | 4.75 | 1 |
| 5 | 4.85 | 1 |
| 6 | 4.85 | 1 |
| 7 | 4.85 | 1 |
| 8 | 5.0 | 2 |
| 9 | 4.6 | 1 |
| 10 | 4.85 | 1 |

<details>
<summary>Rubric breakdown (trial 1)</summary>

| Criterion | Score | Rationale |
|---|---|---|
| completeness | 5 | All 7 expected information items were extracted: ~5,000 tasks, slow on description/fast on title, v2.2→v2.3 regression |
| accuracy | 5 | The root cause hypothesis is remarkably close to the actual cause. The agent correctly identified that description searc |
| efficiency | 5 | Only 2 agent turns to reach a comprehensive, accurate triage. The first question efficiently combined timing/trigger and |
| question_quality | 5 | Both questions demonstrated genuine diagnostic reasoning. The first combined timeline with potential triggers and task c |
| actionability | 5 | The summary provides everything a developer needs: clear reproduction steps, a precise root cause hypothesis pointing to |

**Strengths:**
- Exceptionally efficient — reached a comprehensive diagnosis in only 2 turns
- The second question was perfectly crafted to surface the title-vs-description distinction, which was the key diagnostic insight
- Root cause hypothesis nearly perfectly matches the actual cause without access to source code
- Recommended fix is specific and actionable, pointing to exactly the right areas to investigate
- Proposed test case includes both regression validation and performance threshold

**Weaknesses:**
- Did not capture the exact OS (Ubuntu 22.04) or hardware specs, though the agent correctly noted this in information gaps as unlikely to change the fix approach
- Could have asked about cache clearing or reindexing attempts, though this wasn't critical for the triage

**Most insightful question:** Is every search slow, or only certain kinds? For example, does searching for a single common word like 'meeting' take just as long as searching for something rare or specific?

</details>

---

#### Strategy: socratic-refinement

**Weighted total (mean):** 4.31 +/- 0.75 over 10 trials

| Trial | Score | Turns |
|---|---|---|
| 1 | 3.30 | 1 |
| 2 | 4.80 | 2 |
| 3 | 4.85 | 2 |
| 4 | 4.10 | 2 |
| 5 | 3.20 | 1 |
| 6 | 5.00 | 2 |
| 7 | 4.85 | 2 |
| 8 | 4.85 | 2 |
| 9 | 3.35 | 1 |
| 10 | 4.85 | 2 |

<details>
<summary>Rubric breakdown (trial 1)</summary>

| Criterion | Score | Rationale |
|---|---|---|
| completeness | 3 | Extracted ~5,000 tasks, v2.2→v2.3 upgrade timing, ~2 weeks ago, and 10-15 second latency. However, missed critical dis |
| accuracy | 4 | The root cause hypothesis is on the right track — mentions missing/dropped database index, switching from indexed look |
| efficiency | 3 | Only 1 question was asked before resolving, which kept turn count low. However, the agent resolved too early — it shou |
| question_quality | 4 | The single question asked was well-crafted — it combined two important threads (timing/trigger and task volume) into o |
| actionability | 3 | A developer could start investigating by diffing v2.2 and v2.3 search code, which is reasonable guidance. However, witho |

**Strengths:**
- Excellent first question that efficiently combined two important lines of inquiry (timing and scale)
- Root cause hypothesis correctly identifies index/query regression as likely cause
- Information gaps section honestly acknowledges what wasn't gathered
- Well-structured and detailed triage summary with reproduction steps and test case

**Weaknesses:**
- Resolved after only one exchange — premature closure left critical diagnostic information ungathered
- Failed to discover the title-vs-description search distinction, which is the key diagnostic clue
- Did not gather environment details (OS, hardware) despite the reporter mentioning 'work laptop'
- Did not ask about CPU/resource behavior during slow searches

**Most insightful question:** Did it happen after a specific event — like a TaskFlow update, importing a batch of tasks, or a change in your setup — or did it seem to creep in gradually over time?

</details>

---

#### Strategy: structured-triage

**Weighted total (mean):** 4.45 +/- 0.20 over 10 trials

| Trial | Score | Turns |
|---|---|---|
| 1 | 4.60 | 2 |
| 2 | 4.60 | 2 |
| 3 | 4.60 | 2 |
| 4 | 4.15 | 2 |
| 5 | 4.45 | 2 |
| 6 | 4.60 | 2 |
| 7 | 4.15 | 2 |
| 8 | 4.45 | 2 |
| 9 | 4.20 | 2 |
| 10 | 4.65 | 2 |

<details>
<summary>Rubric breakdown (trial 1)</summary>

| Criterion | Score | Rationale |
|---|---|---|
| completeness | 4 | Captured ~5,000 tasks, slow description search vs fast title search, v2.2→v2.3 regression, ~2 weeks ago, 10-15 second  |
| accuracy | 5 | The root cause hypothesis is remarkably close to the actual cause — it specifically mentions 'a change from indexed fu |
| efficiency | 5 | Only 2 rounds of questions were needed to extract the key diagnostic information. The first question efficiently identif |
| question_quality | 4 | The first question was well-structured and immediately zeroed in on search type differentiation, which was the critical  |
| actionability | 5 | The summary provides clear reproduction steps, a highly accurate root cause hypothesis pointing to FTS→LIKE regression |

**Strengths:**
- Excellent diagnostic efficiency — only 2 turns to extract the core issue
- Root cause hypothesis almost exactly matches the actual cause (FTS5 → LIKE query)
- Well-structured summary with clear reproduction steps and actionable fix recommendations
- First question smartly asked about search type differentiation, which was the key diagnostic signal

**Weaknesses:**
- Did not probe for system resource behavior (CPU spikes) which would have strengthened the diagnosis
- Did not ask about description content characteristics (long descriptions) which is a contributing factor
- Did not ask whether the reporter had tried any workarounds or troubleshooting steps

**Most insightful question:** What search query or type of search are you running when it's slow? Does it happen with every search, or only with certain queries?

</details>

---

#### Strategy: superpowers-brainstorming

**Weighted total (mean):** 3.26 +/- 0.72 over 10 trials

| Trial | Score | Turns |
|---|---|---|
| 1 | 3.15 | 1 |
| 2 | 3.15 | 1 |
| 3 | 2.85 | 1 |
| 4 | 4.35 | 1 |
| 5 | 2.55 | 1 |
| 6 | 2.55 | 1 |
| 7 | 3.15 | 1 |
| 8 | 3.15 | 1 |
| 9 | 4.75 | 1 |
| 10 | 2.95 | 1 |

<details>
<summary>Rubric breakdown (trial 1)</summary>

| Criterion | Score | Rationale |
|---|---|---|
| completeness | 3 | The triage captured ~5,000 tasks, the v2.2→v2.3 regression, the ~2 week timeline, and the 10-15 second latency. Howeve |
| accuracy | 4 | The information captured is accurate and consistent with ground truth. The root cause hypothesis is impressively close � |
| efficiency | 2 | The agent asked only one compound question and then immediately resolved the issue. While getting key info in one turn i |
| question_quality | 4 | The single question asked was well-structured: using numbered options for task count (reducing ambiguity) and probing fo |
| actionability | 3 | A developer could start investigating by diffing v2.2 and v2.3 search code, which is a reasonable starting point. Howeve |

**Strengths:**
- Excellent root cause hypothesis that closely matches the actual cause despite limited information gathered
- Well-structured initial question with numbered options that efficiently extracted two key facts at once
- Good severity assessment and impact analysis recognizing search as a core workflow feature
- Correctly identified 'exact search queries that are slow' as an information gap

**Weaknesses:**
- Premature resolution after only one exchange — closed with known information gaps
- Failed to ask about which types of searches are slow (title vs. description), which is the most diagnostic distinction
- Did not ask about system resource behavior (CPU spikes) which would have confirmed a full-scan hypothesis
- Noted information gaps but chose not to pursue them before closing

**Most insightful question:** The compound question asking both about task count (with numbered ranges) and temporal correlation with updates was well-designed and efficiently extracted two key facts that established the regression narrative.

</details>

---

## Strategy Rankings (by mean score across scenarios)

| Rank | Strategy | Mean Score |
|---|---|---|
| 1 | omo-prometheus | 4.08 |
| 2 | omc-deep-interview | 4.04 |
| 3 | socratic-refinement | 3.74 |
| 4 | structured-triage | 3.49 |
| 5 | superpowers-brainstorming | 3.45 |

## Interpretation Guide

- **Scores are 1-5** where 3 = adequate, 4 = good, 5 = excellent
- **Weighted total** combines: completeness (25%), accuracy (25%),
  efficiency (20%), question quality (15%), actionability (15%)
- **Mean +/- stddev** shows the average and variation across repeated trials
- High stddev indicates the strategy behaves inconsistently
- **Question quality** measures insightfulness — did the triage agent
  ask questions that a human triager would be impressed by?
- **Efficiency** penalizes wasted turns and rewards early resolution
  when the issue is clear enough
