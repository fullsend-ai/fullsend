# V3.0 Discovery Mode - Test Guide

**Date:** April 15, 2026
**Purpose:** Test Discovery Mode with real fullsend repo and strategy docs

---

## Review + Approval Flow (The Key UX Pattern)

```
┌─────────────────────────────────────┐
│ Agent shows pre-populated canvas    │
│ "Does this look right or edit?"     │
└─────────────────────────────────────┘
                │
         ┌──────┴───────┐
         │              │
      APPROVE         EDIT
         │              │
         │              ▼
         │   ┌──────────────────────┐
         │   │ User: "Change X to Y"│
         │   └──────────────────────┘
         │              │
         │              ▼
         │   ┌──────────────────────┐
         │   │ Agent updates canvas │
         │   │ Shows changes        │
         │   └──────────────────────┘
         │              │
         │              ▼
         │   ┌──────────────────────┐
         │   │ "Ready to approve or │
         │   │  edit more?"         │
         │   └──────────────────────┘
         │              │
         └──────────────┘
                │
                ▼
    ┌───────────────────────────┐
    │ Ask for missing info      │
    │ (team, timeline, etc.)    │
    └───────────────────────────┘
                │
                ▼
    ┌───────────────────────────┐
    │ Show COMPLETE design      │
    │ "Does everything look     │
    │  correct? Save or edit?"  │
    └───────────────────────────┘
                │
         ┌──────┴───────┐
         │              │
       SAVE          EDIT
         │              │
         ▼              │
    ┌────────┐         │
    │ Saved! │◄────────┘
    └────────┘
```

**Key:** User gets TWO approval gates (pre-populated + final complete)

---

## How to Test

### **Step 1: Start Fresh Conversation**

Open a new conversation with Claude Code (no active experiment).

---

### **Step 2: Load V3.0 Agent**

```
"Load the Experiment Agent v3.0"
```

Or just say:
```
"Let's run an experiment"
```

(Auto-memory will load v3.0 automatically)

---

### **Step 3: Choose Discovery Mode**

When agent asks:
```
"Do you already have an experiment in mind, or would you like me to
suggest experiments?"
```

Respond:
```
"B - Suggest experiments"
```

---

### **Step 4: Provide Context**

When agent asks what to analyze, say:

**Option A (GitHub repo):**
```
"Analyze the fullsend-ai/fullsend GitHub repository"
```

**Option B (Local docs):**
```
"Analyze ~/Desktop/RH Claude Code/Agentic SDLC Strategy Proposal/"
```

**Option C (Both):**
```
"Analyze the fullsend-ai/fullsend repo AND my Agentic SDLC Strategy
docs at ~/Desktop/RH Claude Code/Agentic SDLC Strategy Proposal/"
```

---

### **Step 5: Review Suggestions**

Agent should present 5 experiments like:

```
### #1: [Experiment Name]
**Priority:** HIGH/MEDIUM/LOW
**Justification:** [Why this matters]
**Impact:** High/Medium/Low
**Risk:** High/Medium/Low
**Feasibility:** Easy/Medium/Hard
**ROI Potential:** $X estimate
**Target Outcome(s):**
- Outcome 1
- Outcome 2
```

---

### **Step 6: Pick One**

```
"1"
```

or

```
"I want to run #3"
```

---

### **Step 7: Review Pre-Populated Canvas**

Agent should show pre-populated content and ask:
```
"Does this look correct?
A) Approve - looks good
B) Edit - I want to change something
C) Start over"
```

**If you approve:**
- Agent asks for missing info (team, timeline)

**If you edit:**
```
"Change the time target from 30% to 20%"
```

Agent should:
- Update the canvas
- Show you the changes
- Ask again: "Ready to approve or edit more?"

---

### **Step 8: Provide Missing Info (After Approval)**

After you approve the pre-populated canvas:

```
"Team: Jerry Becker (PM)
Timeline: April 20 - May 3 (2 weeks)
Focus: Discovery Agent pilot"
```

---

### **Step 9: Final Review**

Agent should show the **complete experiment design** one more time:
```
"Here's your complete experiment design:
[Shows everything: hypothesis, metrics, team, timeline, etc.]

Does everything look correct?
A) Yes - save it
B) No - edit something"
```

**You should:**
```
"A"
```

---

### **Step 10: Confirm Save**

Agent should:
- Save to `/Users/jbecker/.claude/projects/-Users-jbecker/experiments/current/`
- Create `experiment_design.md`
- Create `metadata.json`
- Confirm everything is saved

---

## What to Look For (Success Criteria)

### ✅ **Good Suggestions**
- [ ] Agent identifies 3-5 relevant experiments
- [ ] Suggestions align with fullsend strategy
- [ ] Prioritization makes sense (HIGH = high impact + low risk + strategic fit)
- [ ] ROI estimates are reasonable
- [ ] Justifications reference actual docs/issues from repo

### ✅ **Good Pre-Population**
- [ ] Hypothesis is well-formed and specific
- [ ] Success metrics are quantified (not vague)
- [ ] Baseline behavior is clearly described
- [ ] Expected benefits AND costs both listed
- [ ] Strategic alignment matches actual strategy docs

### ✅ **Good Review/Approval UX** (CRITICAL)
- [ ] Agent shows pre-populated canvas BEFORE asking for missing info
- [ ] Agent explicitly asks: "Does this look right or want to edit?"
- [ ] If you edit something, agent updates and shows changes
- [ ] Agent asks for approval again after edits
- [ ] Only after approval does agent ask for team/timeline
- [ ] Final review shown before saving (all details together)
- [ ] **User feels in control, not prescribed to**

### ✅ **Time Savings**
- [ ] Takes <5 minutes to get suggestions
- [ ] Pre-population saves 10-15 min vs designing from scratch
- [ ] Less mental load (agent did the thinking)

### ✅ **Quality**
- [ ] Agent catches "seems good but might be bad" scenarios (like V6 async tool)
- [ ] Recommendations are actionable (not too big or vague)
- [ ] Target outcomes are realistic

---

## Edge Cases to Test

### **Test 1: No Active Experiment**
```
"Let's run an experiment"
→ Should offer Discovery vs Execution choice
```

### **Test 2: Request More Details**
```
"More details on #3"
→ Should show deeper analysis with evidence from docs
```

### **Test 3: Pick Different Suggestion**
```
"Actually, I want #2 instead"
→ Should switch and pre-populate #2 canvas
```

### **Test 4: Reject All Suggestions**
```
"None of these fit. I have my own idea."
→ Should switch to Execution Mode
```

---

## Example Test Prompts

### **Full Test Flow (Copy-Paste)**

```
Load the Experiment Agent v3.0

[Wait for agent response]

B - Suggest experiments

[Wait for agent to ask what to analyze]

Analyze the fullsend-ai/fullsend GitHub repository and my Agentic SDLC
Strategy docs at ~/Desktop/RH Claude Code/Agentic SDLC Strategy Proposal/

[Wait for agent to present 5 experiments]

1

[Wait for agent to show pre-populated canvas]

Team: Jerry Becker (Innovation Manager)
Timeline: April 20 - May 3, 2026 (2 weeks)
Additional metrics: None
Focus: First strategic pilot
```

---

## Expected Analysis Sources

**From fullsend-ai/fullsend repo:**
- Vision doc (docs/vision.md)
- Roadmap (docs/roadmap.md)
- Problem documents (docs/problems/*.md)
- Open issues
- Recent discussions

**From Agentic SDLC Strategy:**
- Strategic priorities
- Pain points mentioned
- Timeline and phases
- Team structure
- Success criteria

**Agent should synthesize these to find:**
- What's important (strategic goals)
- What's painful (problems to solve)
- What's proposed (hypotheses to test)
- What's uncertain (decisions needing data)

---

## Debugging

### **If agent suggests 0 experiments:**
- Check that it actually read the files (ask "what did you find?")
- Provide more explicit context ("Look for pain points and proposed solutions")

### **If suggestions don't make sense:**
- Ask "Why did you prioritize #1 as HIGH?"
- Request evidence: "Show me where you found that in the docs"

### **If pre-population is too vague:**
- Ask agent to be more specific: "Can you quantify the baseline?"
- Provide additional context: "Here's the current state..."

---

## After Testing

### **Document:**
- Which suggestions were most valuable?
- Did pre-population save time?
- Were there any missing experiments you expected to see?
- Any suggestions that seemed off-base?

### **Iterate:**
- If certain types of experiments are consistently missed, update analysis logic
- If prioritization feels wrong, adjust scoring criteria
- If pre-population has gaps, enhance inference logic

---

**Ready to test!** 🧪🔍

Start a new conversation and try the flow above. Report back what works and what needs tuning!
