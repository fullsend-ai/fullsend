# Cost of Bad Decisions Framework

**Quantifying the Financial Impact of Gut-Feel vs. Evidence-Based Strategic Decisions**

---

## Executive Summary

Strategic decisions made without rigorous experimentation often scale bad ideas org-wide, creating multi-million dollar losses. This framework quantifies the financial impact of:

1. **Bad decisions that scale** (gut-feel approach without agent)
2. **Bad decisions killed early** (evidence-based approach with agent)

**Bottom Line:** The Experiment Agent's ROI isn't just time savings—it's **preventing million-dollar mistakes**.

---

## Framework Structure

### Formula: Cost of a Bad Decision

```
Cost of Bad Decision = 
  (Number of people affected) 
  × (Time wasted or value lost per person per day)
  × (Work days per year)
  × (Average hourly cost)
  + (Tool/infrastructure costs)
  + (Change management costs to fix)
```

---

## Real Examples from Simulations

### Example 1: Scaling Hourly Standups (V2)

**Scenario:** Team tests hourly standups, uses gut-feel approach, loves the coordination, misses the overhead costs.

**V2 (With Agent) Found:**
- Meeting overhead: 7.2 hours/person over 2 days
- Unsustainable long-term
- **Recommendation:** Don't scale, iterate to daily instead

**If Gut-Feel Approach Had Scaled It:**

```
Assumptions:
- 20 teams adopt hourly standups
- Average team size: 5 people
- Meeting overhead: 18 hours/person/week (7.2 hrs × 2.5 days)
- Work weeks per year: 50
- Average hourly rate: $100/hr

Calculation:
- 20 teams × 5 people = 100 people
- 100 people × 18 hours/week × 50 weeks = 90,000 hours/year
- 90,000 hours × $100/hr = $9,000,000/year in meeting overhead

Net cost: $9M/year wasted in excessive meetings
```

**Agent prevented:** $9M/year disaster by recommending daily standups instead (83% less overhead)

---

### Example 2: Scaling Async Standup Tool (V6)

**Scenario:** Team tests Geekbot (async standup tool), loves avoiding meetings, misses notification overhead and coordination delays.

**V6B (Without Agent) Projected:**
- "Saves 6 min/day meeting time"
- Projected value: $220K/year if scaled to 100 people
- **Recommendation:** Scale it org-wide

**V6A (With Agent) Found:**
- Saves 60 min meeting time
- Costs 1,660 min in notification overhead + context switching + delays
- Net cost: 27x more expensive than benefit
- **Recommendation:** ABANDON

**If V6B Gut-Feel Approach Had Scaled:**

```
Assumptions:
- 100 people adopt Geekbot
- Hidden cost per person per day: 40 min wasted
- Work days per year: 220
- Average hourly rate: $100/hr

Calculation:
- 100 people × 40 min/day = 4,000 min/day = 66.7 hours/day
- 66.7 hours/day × 220 days = 14,674 hours/year
- 14,674 hours × $100/hr = $1,467,400/year

Plus tool costs: $3/person/month × 100 × 12 = $3,600/year

Total cost: $1,471,000/year

Projected benefit: $220,000/year
Actual cost: $1,471,000/year
Net impact: -$1,251,000/year (disaster)
```

**Agent prevented:** $1.47M/year disaster by killing it in 2 weeks

---

### Example 3: Generic Bad Tool Adoption

**Template for calculating cost of scaling a bad tool:**

```
Inputs:
- Number of teams: ___
- People per team: ___
- Total people affected: ___ (teams × people)
- Hidden cost per person per day: ___ minutes
- Work days per year: 220
- Average hourly cost: $___/hr
- Tool cost per person per month: $___ (if applicable)

Calculation:
1. Daily waste: [people] × [min/day] ÷ 60 = ___ hours/day
2. Annual waste: [hours/day] × 220 = ___ hours/year  
3. Dollar cost: [hours/year] × $[hourly rate] = $___/year
4. Tool cost: [people] × $[monthly cost] × 12 = $___/year
5. Total annual cost: $___ (3) + $___ (4) = $___/year

Prevention value: Amount NOT spent because agent killed it early
```

---

## Decision Type Cost Categories

### Type 1: Process Overhead (Meetings, Tools, Workflows)

**Examples:**
- Excessive meetings (V2: hourly standups)
- Async tools with hidden overhead (V6: Geekbot)
- Approval workflows that create bottlenecks

**Cost drivers:**
- Time wasted per person per day
- Number of people affected
- Compounding effect (every day for a year)

**Prevention strategy:** Measure overhead explicitly, not just stated benefits

---

### Type 2: Bad Product Features

**Examples:**
- Shipping a feature users don't want
- Feature that slows down core workflow
- Feature that creates support burden

**Cost drivers:**
- Engineering time to build
- Opportunity cost (could've built something valuable)
- Customer churn from bad experience
- Support overhead to handle complaints

**Example calculation:**

```
Bad Feature Costs:
- Engineering time: 3 engineers × 6 weeks × $100/hr × 40 hrs/week = $72,000
- Support overhead: 20 tickets/week × 52 weeks × 30 min × $50/hr = $26,000
- Customer churn: 5 customers × $50K ARR = $250,000
Total cost: $348,000

If caught in experiment phase (1 week test):
- Experiment cost: 1 engineer × 1 week × $100/hr × 40 hrs = $4,000
- Savings: $344,000 (prevented bad build)
```

---

### Type 3: Bad Tooling/Infrastructure

**Examples:**
- Adopting CI/CD tool that slows builds
- Monitoring tool that creates noise instead of signal
- Collaboration tool that fragments communication

**Cost drivers:**
- Time lost to slow/broken tooling
- Cognitive overhead managing multiple tools
- License costs for unused seats
- Migration costs to fix later

**Example calculation:**

```
Bad CI/CD Tool:
- Slows builds by 10 min/build × 50 builds/day × 20 teams = 167 hours/day
- 167 hours/day × 220 work days = 36,740 hours/year
- 36,740 hours × $100/hr = $3,674,000/year
Plus tool license: $50/user/month × 100 users × 12 = $60,000/year
Total cost: $3,734,000/year

Agent prevents by measuring build time in experiment phase (1 week)
```

---

## The ROI of Killing Bad Ideas

### Traditional Approach (No Agent)

```
Process:
1. Someone proposes idea
2. Team tries it informally
3. "Feels good" → scale it
4. Hidden costs emerge after 6-12 months
5. Leadership realizes it's hurting productivity
6. Painful removal process
7. Trust in decision-making damaged

Total cost: 
- Bad decision scaled for 6-12 months: $500K-$4M depending on scope
- Change management to remove: $50K-$200K
- Reputation damage: Hard to quantify
```

### Agent Approach

```
Process:
1. Someone proposes idea
2. Design rigorous experiment (18 min with agent)
3. Track benefits AND costs (2 weeks)
4. Data shows hidden costs → kill it early
5. Total investment: ~50 min + 2 weeks of team time
6. Disaster prevented

Total cost:
- Experiment time: 4 people × 50 min = 200 min = 3.3 hours = $333
- Savings: $500K-$4M disaster prevented

ROI: 1,500x - 12,000x
```

---

## Real-World Red Hat Scenarios

### Scenario 1: AI Code Review Tool

**Hypothesis:** AI tool speeds up code review

**Gut-feel approach:** Team likes it, scales to 200 engineers

**Hidden costs:**
- False positive rate: 30% → wastes reviewer time
- Slows review cycle: +2 days average
- Erodes trust in review process

**Cost if scaled:**
- 200 engineers × 2 days delay × 50 PRs/year = 20,000 days delayed
- Opportunity cost of delayed features: $2M-5M

**Agent would catch:** Measure false positive rate and cycle time in experiment

---

### Scenario 2: Detailed Time Tracking

**Hypothesis:** Time tracking improves accountability

**Gut-feel approach:** Managers like visibility, scales to 10 teams

**Hidden costs:**
- Administrative overhead: 15 min/day per person
- Erodes trust between managers/reports
- Gaming the system (people optimize for metrics, not outcomes)

**Cost if scaled:**
- 10 teams × 5 people × 15 min/day × 220 days = 2,750 hours/year
- 2,750 hours × $100/hr = $275,000/year in overhead
- Trust damage: Harder to quantify, but real

**Agent would catch:** Track administrative burden and team morale

---

### Scenario 3: Excessive Documentation Requirements

**Hypothesis:** More docs improve knowledge transfer

**Gut-feel approach:** Seems responsible, becomes policy

**Hidden costs:**
- Writing overhead: 30 min/person/week
- Maintenance burden: Docs go stale, nobody updates
- False sense of security: People assume docs are accurate when they're not

**Cost if scaled:**
- 100 people × 30 min/week × 50 weeks = 2,500 hours/year
- 2,500 hours × $100/hr = $250,000/year
- Plus cost of acting on stale docs: Incidents, rework, etc.

**Agent would catch:** Measure doc creation time AND doc usage rate

---

## Using This Framework

### Step 1: Identify the Decision Type

- Process/workflow change?
- Product feature?
- Tool/infrastructure?
- Policy/requirement?

### Step 2: Define Scope

- How many people would be affected if scaled?
- How long would they use it? (temporary vs permanent)
- What are the obvious benefits?

### Step 3: Identify Hidden Cost Categories

**Common hidden costs to check:**
- Time overhead (per person per day)
- Context switching / notification fatigue
- Coordination delays
- Administrative burden
- Tool/license fees
- Support/maintenance costs
- Change management costs (if it fails later)
- Trust/morale impact

### Step 4: Run the Calculation

Use formula:
```
Cost of Bad Decision = 
  [People] × [Time lost per day] × [Days/year] × [$/hour]
  + [Tool costs]
  + [Change management if failed]
```

### Step 5: Compare Agent Cost vs Disaster Cost

```
Agent approach cost: ~50 min experiment design + 2 week test
Disaster prevented: $[calculated cost from Step 4]

ROI = [Disaster cost] ÷ [Experiment cost]
```

Typical ROI: **1,000x - 10,000x**

---

## Summary Table: Real Costs from Simulations

| Scenario | Gut-Feel Result | Agent Result | Disaster Prevented |
|----------|----------------|--------------|-------------------|
| **Hourly Standups (V2)** | "Great coordination!" → Scale it | "7.2 hrs overhead" → Don't scale | **$9M/year** |
| **Async Tool (V6)** | "Saves meetings!" → Scale it | "27x cost" → Abandon | **$1.47M/year** |
| **Daily Standups (V3)** | "Seems good" → Maybe keep? | "83% less overhead" → VALIDATED | Prevented inferior alternative |

**Total value from 3 experiments:** $10.47M+ in prevented disasters

**Total agent cost:** ~3 hours experiment design time

**ROI:** ~3,490,000% (34,900x)

---

## The Bottom Line for Executives

**Without Experiment Agent:**
- Gut-feel decisions scale bad ideas
- Hidden costs emerge 6-12 months later
- Multi-million dollar mistakes compound
- Change management pain to fix
- Trust in decision-making erodes

**With Experiment Agent:**
- Rigorous testing surfaces hidden costs
- Bad ideas killed in 2 weeks (not 12 months)
- Disasters prevented before scaling
- Decision confidence: 9/10
- Trust in decision-making strengthens

**The agent's value:** Preventing million-dollar mistakes for the cost of a few hours of structured thinking.

---

**Prepared by:** Jerry Becker  
**Date:** April 12, 2026  
**Purpose:** Quantify financial impact of evidence-based vs gut-feel strategic decisions
