# Experiment Agent: Product Roadmap

**Version:** 2.0 → 2.2 → 2.5-Lite → 3.0 → 3.1 (planned) → 4.0 (planned)
**Owner:** Jerry Becker (Product) + Scotty (Engineering)
**Last Updated:** April 16, 2026 (v3.0 shipped, v3.1/v4.0 planned)

---

## Current State (v3.0) ✅ LATEST

**What works today:**
- ✅ Experiment Design via conversation (renamed from "Canvas")
- ✅ Metric tracking with bias-corrected definitions
- ✅ Comparative report generation (9-section format)
- ✅ Devil's advocate mode for contradiction-seeking
- ✅ Confidence level calibration
- ✅ Progressive disclosure (1-3 questions at a time)
- ✅ Context-first welcome flow (ask about docs before questions)
- ✅ Image/screenshot support (read and describe visual evidence)
- ✅ Multi-person collaboration (file-based observation system)
- ✅ Editable observations (edit any file, auto-incorporated in reports)
- ✅ Experiment template library (save/load experiment structures)
- ✅ Report format preview (show 9-section structure during setup)
- ✅ Persistent memory across sessions (multi-week experiments)
- ✅ Auto-load context when user returns (zero re-explaining)
- ✅ Mid-experiment document uploads (add context anytime)
- ✅ Archive completed experiments (clean separation)
- ✅ **NEW (v3.0):** Discovery Mode - AI analyzes repos/docs and suggests experiments
- ✅ **NEW (v3.0):** Review + Approve UX - two-gate approval system for pre-populated content
- ✅ **NEW (v3.0):** Pre-population of experiment canvas (saves 10-15 min setup time)
- ✅ **NEW (v3.0):** Evaluation framework (Impact/Risk/Feasibility/Strategic Alignment/ROI)

**Document handling (v2.5-Lite):**
- ✅ Paste text directly into chat
- ✅ Read local file paths (markdown, txt, PDF, docx via conversion)
- ✅ Fetch public URLs (Google Docs with proper sharing, web pages)
- ✅ Read images/screenshots (Miro exports, diagrams, whiteboard photos, design mockups)
- ✅ Multi-doc synthesis with source attribution
- ✅ Persistent document library per experiment

**Check-In Workflow (v3.0):**
- ⚠️ **Current state:** Manual check-ins (you must remember to come back and say "continue experiment")
- 💡 **Recommended TODAY:** Use Slack `/remind` to set up check-in reminders
  - Run in any Slack channel or DM: `/remind me [message] [when]`
  - Example: `/remind me to "Update experiment: open Claude and say 'continue experiment'" every Monday at 10am starting April 22 until May 29`
  - No integration needed - it's a built-in Slack feature
  - Reminder will appear in your Slackbot DM at scheduled times
- 🔮 **Future (v3.1):** Agent will generate .ics calendar files and ready-to-use Slack commands
- 🔮 **Future (v4.0):** Agent will proactively check in with you and auto-collect metrics from Jira/GitHub

**Remaining Limitations:**
- ⚠️ Single experiment at a time (multi-experiment tracking in v3.1+)
- ⚠️ No drag-and-drop file upload UI (CLI file paths work fine)
- ⚠️ Can't access private Google Docs without download (graceful fallback to PDF)
- ⚠️ Can't display images inline in markdown reports (can describe them)
- ⚠️ No real-time collaboration dashboard (file-based works but not live)
- ⚠️ Check-in reminders require manual setup (use Slack `/remind` - see "Check-In Workflow" section)
- ⚠️ No auto-collection from external systems (manual logging only - see v4.0)

---

## Phase 1: Polish Current Capabilities (v2.1 → v2.2) ✅ COMPLETE

**Timeline:** Completed April 10, 2026
**Goal:** Make what exists work smoothly for Jerry's real experiments

### Features Completed in v2.2:

**1. ✅ Progressive Disclosure (UX Fix - Critical)**
- Ask 1-3 questions at a time, never more
- 1 question if complex, up to 3 if simple/related
- Confirm information logged before moving to next question
- **User feedback:** "too many questions" → "LOVING THIS!!!"
- **Impact:** Eliminated cognitive overload, dramatically improved UX

**2. ✅ Context-First Welcome Flow**
- Ask "Do you have context to share?" before jumping into questions
- Options: documents, links, images, text paste, or design from scratch
- If docs provided: extract info → pre-fill Experiment Design → user reviews
- **Impact:** Can save 5-10 questions if context already exists

**3. ✅ Image/Screenshot Support**
- Accept file paths to images during design or daily logging
- Read images with Read tool and describe observations
- Include visual evidence in observations and final reports
- Reference images with paths + descriptions in markdown
- **Use case:** Essential for design/UX experiments
- **Limitation:** Can describe images but can't display inline (user views in file system)

**4. ✅ Multi-Person Collaboration**
- File-based observation system for team experiments
- Each person creates `[name]_day[X].md` files
- Agent reads all perspectives and synthesizes
- Activity log shows who contributed what
- **Use case:** Most design experiments involve 2-5 people
- **Limitation:** Not real-time, no live dashboard

**5. ✅ Editable Observations**
- Users can edit any observation file anytime
- Agent re-reads all files when generating final report
- Changes automatically incorporated
- **Impact:** Quality of life - can fix typos/add forgotten details

**6. ✅ Experiment Template Library**
- Save completed experiment designs as templates
- Load templates when starting new experiments
- Clone structure, customize specifics
- **Use case:** Run similar experiments repeatedly
- **Impact:** Faster setup for common experiment patterns

**7. ✅ Report Format Preview**
- Show 9-section final report structure during setup
- Sets user expectations about output format
- **User feedback:** "didn't know what to expect" → now they do
- **Impact:** No surprises at end of experiment

**8. ✅ Stakeholder Communication Plan**
- During setup, ask about stakeholders and communication preferences
- Offer suggested plan (frequency, medium, depth)
- User accepts or edits
- Auto-generate materials at checkpoints
- **Impact:** Clear expectations, reduces update burden

**9. ✅ Data Source Integration Question**
- Ask if Jira/GitHub/etc. already captures metrics
- Document sources, plan integration or exports
- **Phase 1:** Manual exports, **Phase 2+:** Automated connections

**10. ✅ Structured Weekly Check-Ins**
- Don't ask "any updates?" - ask specific questions
- Based on experiment design (participants, metrics)
- Offer conversation OR file drop options
- **Impact:** Reduces cognitive load, ensures complete data

### Features Still Planned for Later Phases:

**1. Improved Welcome Flow**
- ✅ Ask "do you have docs?" before jumping into questions
- ✅ Support: paste text, file path, URL, or "let's design from scratch"
- ✅ Extract info from docs → pre-fill Experiment Canvas → user reviews
- **Status:** Designed, ready to implement

**2. Graceful Google Docs Handling**
```
User: "Here's my doc: [private Google Doc link]"
Agent: "I can't access that due to permissions. No problem!
        Can you either:
        • Change sharing to 'Anyone with link can view'
        • Download as PDF and share the file path
        Which is easier for you?"
```
- Clear error message with actionable next steps
- Don't make user guess why it failed

**3. Miro Board Image Interpretation**
- Already works (multimodal) but optimize prompts for:
  - Extracting experiment structure from Miro boards
  - Reading sticky notes and flowcharts
  - Interpreting visual hierarchy
- Test: Can agent extract Experiment Canvas from Miro export?

**4. Multi-Document Intake** (single session)
```
User: "I have 3 docs: proposal, meeting notes, stakeholder email"
Agent: "Great! Share them one at a time or all at once"
User: [shares 3 links]
Agent: "I've read all 3. Here's what I synthesized:
        • From proposal: Your hypothesis is...
        • From meeting notes: Team identified these metrics...
        • From email: Stakeholder wants to see..."
```
- Read multiple sources
- Cite which source each fact came from
- Surface conflicts between docs

**5. User Testing & Iteration**
- Jerry runs 1-2 real experiments with v2.1
- Collect feedback on what's clunky
- Iterate based on real usage

**Success Criteria:**
- ✅ Jerry can start an experiment in <10 min (vs. 1-2 hours)
- ✅ 80%+ of Experiment Canvas pre-filled from docs
- ✅ Graceful handling of permission errors
- ✅ Multi-doc synthesis works smoothly

---

## Phase 2-Lite: Persistent Memory (Single Experiment) ✅ COMPLETE

**Timeline:** Completed April 10, 2026 (built in ~60 minutes)
**Goal:** Agent remembers context across sessions for multi-week experiments
**Approach:** LLM agent definition (not custom software infrastructure)

### Features Completed in v2.5-Lite:

**1. ✅ Persistent Storage Structure**
- Organized file system for experiment data
- /experiments/current/ for active experiment
- /experiments/archive/ for completed experiments
- metadata.json tracks experiment state
- **Impact:** Zero data loss between sessions

**2. ✅ Auto-Load Context When User Returns**
- Agent checks /current/ on startup
- Loads experiment design, metrics, participants automatically
- Shows progress (Week X of Y, current phase)
- **Impact:** Zero re-explaining context

**3. ✅ Mid-Experiment Document Uploads**
- Add docs anytime during experiment
- Auto-associated with current experiment
- Saved to /docs/ directory
- **Impact:** Add context without friction

**4. ✅ Archive Completed Experiments**
- Move /current/ to /archive/[experiment_name]_[date]/
- Clean separation between active and completed
- Retrieve archived experiments later
- **Impact:** Experiment library over time

**5. ✅ Cross-Session Consistency**
- Metric definitions persist across all sessions
- Applied consistently in observations and reports
- Reference when definition was set
- **Impact:** No metric drift, trustworthy data

**Intentional Limitation:**
- ⚠️ Single experiment only (one active at a time)
- **Why:** Validates persistent memory value before building multi-experiment complexity
- **When to add multi-experiment:** After validating v2.5-Lite with real usage

**Success Criteria:**
- ✅ User can run 6-week experiment, updating weekly without re-explaining
- ✅ User can add docs mid-experiment seamlessly
- ✅ User can archive completed experiment and start fresh
- ✅ Metric definitions consistent across all sessions

**Build Time:** ~60 minutes (agent definition, not custom software)

---

## Phase 3.0: Discovery Mode + Strategic Prioritization ✅ COMPLETE

**Timeline:** Completed April 16, 2026
**Goal:** Lower discovery barrier - AI suggests experiments based on codebase/strategy analysis
**Build Time:** 2 days (agent definition + testing)

### Features Completed in v3.0:

**1. ✅ Discovery Mode**
- Agent analyzes GitHub repos, strategy docs, roadmaps
- Identifies pain points, strategic opportunities, proposed changes
- Evaluates experiments (Impact, Risk, Feasibility, ROI, Strategic Alignment)
- Prioritizes top 5 recommendations
- Pre-populates experiment canvas based on analysis

**2. ✅ Review + Approve UX Pattern**
- Two-gate approval system (prevents "prescribed to" feeling)
- **Gate 1:** Review pre-populated canvas → Approve or Edit → Loop until approved
- **Gate 2:** Review complete design → Save or Edit
- User feels in control throughout the process

**3. ✅ Strategic Prioritization**
- Evaluation framework: Impact/Risk/Feasibility/Strategic Alignment/ROI
- Prioritization logic: HIGH = High Impact + (Low Risk OR Easy Feasibility) + Strategic Fit
- Export evaluation reports to shareable documents

**4. ✅ Setup Documentation**
- SETUP_GUIDE.md for Full Send team and external users
- Three setup options: auto-load (memory), repo-specific (CLAUDE.md), manual load
- Updated README with "Using the Experiment Agent" section

**Success Criteria:**
- ✅ Agent identifies 3-5 high-value experiments from docs/repos
- ✅ Prioritization aligns with user's strategic goals
- ✅ Pre-populated canvas saves user 10-15 minutes
- ✅ User picks and runs a suggested experiment
- ✅ Agent catches "seems good but is bad" experiments (V6-style disaster prevention)

**Testing:**
- ✅ Tested with real P&D Agentic SDLC Strategy document (19-page PDF)
- ✅ Successfully identified 5 strategic experiments with proper justifications
- ✅ Successfully evaluated 3 architecture documents and prioritized based on strategic value
- ✅ Review + Approve UX validated with user edits and re-approval

**Impact:**
- Solves "blank canvas problem" (users don't know what to test)
- Lowers discovery barrier (no need to design experiment from scratch)
- Surfaces experiments users might miss (AI finds patterns in docs)
- Data-driven prioritization (not gut-feel)

---

## Phase 3.1: Smart Check-Ins & Data Collection Guidance (Planned)

**Timeline:** TBD (4-6 weeks)
**Goal:** Make multi-week experiments easier to maintain with better check-in UX and data collection guidance
**Build Time:** 1-2 weeks (agent definition + documentation updates)

### Features to Build:

**1. Check-In Reminder Generation**
```
Agent: "✅ Experiment saved!

       To stay on track, I recommend setting up check-in reminders.

       📅 Download calendar events (.ics files):
       - Week 1-2: Daily check-ins (April 17-30)
       - Week 3-6: Weekly check-ins (May 1-29)

       Or use Slack reminders:
       /remind me to 'Update experiment: open Claude and say continue experiment'
       every Monday at 10am starting May 6 until May 29"
```
- Agent generates .ics files for calendar import (Google Calendar, Outlook, Apple)
- Agent provides Slack `/remind` commands ready to copy/paste
- Customizable check-in frequency based on experiment timeline
- **Impact:** Reduces reliance on human memory for multi-week experiments

**2. Smart Data Collection Guidance**
```
Agent: "I've identified these success metrics:
       1. Response time reduced from 5 days to 3.5 days
       2. Reopen rate stays below 10%
       3. AI draft usage >70%

       I can guide you on collecting this data:

       Auto-collectable (v4.0):
       ✅ Jira: ticket created/resolved dates, status transitions
       ✅ Database: AI draft counts, feedback distribution

       Manual-collect (qualitative):
       ❌ Team sentiment (you'll log at check-ins)
       ❌ Hidden costs discovered (you'll log as observed)

       Sound good?"
```
- Agent analyzes success metrics and determines what CAN be auto-collected
- Agent maps metrics to data sources (Jira, GitHub, database, etc.)
- Agent creates collection plan (what to auto-collect vs manual-collect)
- Agent provides guidance on manual collection at check-ins
- **Impact:** Reduces noise (only collect relevant data), sets user expectations

**3. Hybrid Collection Approach Documentation**
- Update experiment design workflow to ask: "What data sources should I monitor?"
- Guide user to provide: Jira project, GitHub repo, database access, etc.
- Document what's auto-collectable vs manual (set expectations)
- **Impact:** Clarity on data collection approach before experiment starts

**4. Check-In Workflow Improvements**
```
[At check-in time]
User: "continue experiment"

Agent: "Week 2 check-in for AI Helpdesk Agent pilot.

       Auto-collected metrics (guidance - not yet implemented):
       - Response time: [instructions to query Jira]
       - AI draft usage: [instructions to query database]

       Manual observations needed:
       - Team sentiment: How do technicians feel about AI drafts?
       - Hidden costs: Any unexpected overhead discovered?
       - Quality issues: Any customer complaints or errors?

       What observations do you have for Week 2?"
```
- Agent prompts for specific observations (not vague "any updates?")
- Agent separates quantitative (future auto-collect) from qualitative (manual)
- Agent guides user on what to look for
- **Impact:** More complete data, less cognitive load at check-ins

**Success Criteria:**
- ✅ User sets up check-in reminders in <5 minutes (calendar or Slack)
- ✅ User understands what data will be auto-collected (future) vs manual
- ✅ User receives specific prompts at check-ins (not vague "any updates?")
- ✅ 90%+ of users set up some form of check-in reminder

**Build Time:** 1-2 weeks (mostly documentation + .ics generation logic)

---

## Phase 4.0: Automated Data Collection & Proactive Check-Ins (Planned)

**Timeline:** TBD (8-16 weeks)
**Goal:** Agent proactively collects data and initiates check-ins (minimal user effort)
**Build Time:** 4-8 weeks (MCP integrations + agent enhancements)

### Features to Build:

**1. Smart Auto-Collection from External Systems**
```
[During experiment setup, after user approves canvas]

Agent: "I've identified these success metrics:
       1. Response time reduced from 5 days to 3.5 days
       2. Reopen rate stays below 10%
       3. AI draft usage >70%

       I can auto-collect some of this data from Jira and your database.

       What data sources should I monitor?
       A) Jira project JIRACONFSD
       B) ai-helpdesk-agent database (ai_draft_feedback table)
       C) GitHub repo (if relevant)
       D) None - I'll log everything manually
       E) Custom (specify)

       Choose all that apply:"

User: "A and B"

Agent: "Got it! I'll auto-collect from Jira and the database.

       At each check-in, I'll fetch:
       - Jira: ticket created/resolved dates, status transitions
       - Database: AI draft counts, feedback emoji distribution

       You'll still need to provide qualitative observations
       (team sentiment, hidden costs, etc.) at check-ins.

       Sound good?"
```
- **Relevance-first collection:** Agent maps metrics → data sources (not grab-everything)
- **Only collect what's needed:** Jira fields, GitHub metrics, database queries aligned to success criteria
- **MCP integrations:** Jira MCP, GitHub MCP, database connectors
- **Hybrid approach:** Auto-collect quantitative, manual-collect qualitative

**2. Collection Plan Generation**
```
Agent builds collection plan internally:

Success Metric 1: "Response time reduced from 5 days to 3.5 days"
  └─ Needs: time_to_resolve calculation
  └─ Data source: Jira API
      └─ Fields needed: created, resolutiondate
      └─ API call: GET /rest/api/3/search?jql=project=JIRACONFSD&fields=created,resolutiondate
  └─ Calculation: resolutiondate - created = time_to_resolve

Success Metric 2: "AI draft usage >70%"
  └─ Needs: count of AI drafts / total eligible tickets
  └─ Data source: ai-helpdesk-agent database
      └─ Query: SELECT COUNT(*) FROM ai_draft_feedback WHERE timestamp BETWEEN start AND now()

Collection plan:
  ✅ Collect: Jira created/resolutiondate, database draft counts
  ❌ Skip: Jira priority, labels, custom fields (not in success metrics)
```
- Agent parses success metrics to identify data needs
- Agent maps metrics to available data sources (Jira, GitHub, database)
- Agent builds minimal collection queries (only relevant fields)
- **Impact:** Reduces noise, saves API calls, focuses on signal

**3. Automated Check-In Execution**
```
[At check-in time - weekly, Monday 10am]

Agent auto-executes collection plan:
  1. Jira API call (only relevant fields)
  2. Database query (only relevant tables)
  3. Calculate metrics vs baseline/target

Agent presents update:
  "Week 3 update for AI Helpdesk Agent pilot:

   Auto-collected metrics:
   - Response time: 4.2 days (baseline: 5, target: 3.5) - improving but not at target
   - Reopen rate: 8% (target: <10%) ✅ within target
   - AI draft usage: 62% (target: >70%) ❌ below target - investigate why

   What qualitative observations do you have?
   - Team sentiment about AI drafts?
   - Any hidden costs discovered?
   - Quality issues or customer feedback?"
```
- Agent fetches data automatically at check-in intervals
- Agent calculates metrics and compares to baseline/target
- Agent prompts user for qualitative observations (sentiment, hidden costs, etc.)
- **Impact:** User only logs qualitative observations, quantitative is automatic

**4. Proactive Check-In Initiation**
```
[Monday 10am - check-in time]

Agent initiates (via Slack or notification):
  "🔔 Time for your Week 3 check-in on AI Helpdesk Agent pilot!

  I've collected the latest metrics (see below). Ready to log observations?

  [Link to continue experiment in Claude]"

User clicks link, agent shows auto-collected metrics + asks for qualitative updates
```
- Integration with Google Calendar API or Slack to send notifications
- Agent initiates check-in (not just reminder for user to remember)
- User clicks link, agent loads experiment context + latest metrics
- **Impact:** True "push" check-ins, not just passive reminders

**5. Google Calendar API Integration**
```
[When experiment is saved]

Agent: "I'll add check-in events to your Google Calendar automatically.

       Authorize Google Calendar access? [Yes/No]"

User: "Yes"

Agent: [OAuth flow]
       "✅ Calendar events created:
       - Week 1-2: Daily 10am check-ins (April 17-30)
       - Week 3-6: Weekly Monday 10am check-ins (May 1-29)

       I'll send you a notification at each check-in time."
```
- OAuth integration with Google Calendar
- Agent creates recurring events automatically (no manual .ics import)
- Agent sends notifications at check-in time
- **Impact:** Zero manual setup, fully automated

**6. MCP Integration Requirements**
- **Jira MCP:** Read tickets, fields, changelogs (GitHub MCP server already exists)
- **GitHub MCP:** Read PRs, commits, build status (GitHub MCP server already exists)
- **Database connector MCP:** Query PostgreSQL, MySQL, SQLite for custom metrics
- **Google Calendar MCP:** Create/update events, send notifications
- **Slack MCP:** Send DMs, post check-in reminders (Slack MCP server already exists)

**Success Criteria:**
- ✅ Agent auto-collects 70%+ of quantitative metrics from external systems
- ✅ User only logs qualitative observations (sentiment, hidden costs)
- ✅ Check-ins take <10 minutes (vs 20-30 minutes with manual data collection)
- ✅ 90%+ of metrics are accurate (auto-collection doesn't miss important data)
- ✅ Agent catches data drift (missing data, API failures, access issues)

**Build Time:** 4-8 weeks (MCP integrations + OAuth flows + agent logic)

---

## Phase 2.5: Multi-Experiment Tracking (Future)

**Timeline:** TBD (after validating v2.5-Lite with real usage)
**Goal:** Track multiple experiments in parallel
**Estimated Build Time:** 3-5 days (agent definition) or 2 weeks (custom software - Option B)

### Features to Build:

**1. List/Switch Between Experiments**
```
User: "What experiments am I tracking?"
Agent: "You have 2 active experiments:
        1. Process Improvement Agent pilot (Week 2 of 4)
        2. Discovery Agent validation (Week 1 of 6)

        Which one would you like to discuss?"
```
- List all active experiments with progress
- Quick context switching between experiments
- Disambiguate when user provides update

**2. Context Switching Logic**
```
User: "Here's my Week 3 update"
Agent: "I see you're tracking 2 experiments. Which one is this for?
        1. Process Improvement (currently Week 2)
        2. Discovery Agent (currently Week 1)"
```
- Infer from conversation context when possible
- Ask explicitly when ambiguous
- Remember last-discussed experiment

**3. Comparison Across Experiments**
```
User: "How did Process Improvement compare to Discovery Agent?"
Agent: "Comparing final results:

        Process Improvement:
        • Result: VALIDATED (80% time reduction)
        • Confidence: High (85%)
        • Duration: 4 weeks

        Discovery Agent:
        • Result: INCONCLUSIVE (mixed signals)
        • Confidence: Medium (60%)
        • Duration: 6 weeks

        Key difference: Process Improvement had clearer metrics..."
```
- Generate comparison reports across experiments
- Identify patterns across multiple experiments
- Meta-learnings about what makes experiments succeed

**Success Criteria:**
- ✅ Agent remembers all context from previous sessions
- ✅ User can add docs at any time during experiment
- ✅ Agent tracks multiple experiments in parallel
- ✅ No need to re-explain context each session

---

## Phase 5: Advanced Collaboration & Team Features (v5.0)

**Timeline:** TBD (future - 16+ weeks)
**Goal:** Team collaboration, shared experiment library, organizational learning

### Features to Build:

**1. Team Collaboration**
```
User: "Add Sarah (PM) as a collaborator on this experiment"
Agent: [creates shared access]

Sarah: "What's the current status of Process Improvement pilot?"
Agent: "Hi Sarah! Jerry added you as collaborator. Here's the status..."
```
- Multi-user access to same experiment
- Role-based permissions (owner, collaborator, viewer)
- Activity log (who added what docs, when)

**2. Automated Weekly Summaries**
```
[Every Friday 5 PM]
Agent: "Weekly summary for Process Improvement experiment:
        • Team A: 7.5 hours process work this week (baseline avg: 8)
        • Team B: 4.2 hours process work (using agent)
        • Delta: 3.3 hours saved with agent

        No action needed - I'll include this in final report.
        Questions? Reply anytime."
```
- Scheduled check-ins
- Proactive summaries
- Async updates (user doesn't have to ask)

**3. Slack / Email Integration**
```
[In Slack]
User: "@experiment-agent status on Discovery pilot"
Agent: "Discovery Agent pilot - Week 3 of 6:
        • 2 duplicate work incidents this week (baseline had 0)
        • Confidence: Low (need more weeks of data)
        Full report: [link]"
```
- Meet users where they work
- Quick status checks via Slack/email
- Deep dive via full interface

**4. Experiment Templates & Cloning**
```
User: "I want to run the same experiment structure as Process Improvement
       pilot, but test Discovery Agent instead"
Agent: "Got it. Cloning experiment structure:
        • Same 4-week timeline
        • Same metrics (time saved, satisfaction, adoption)
        • Same baseline vs. treatment design

        What's different? Just the intervention (Discovery Agent vs.
        Process Improvement Agent)?"
```
- Save experiment designs as templates
- Clone and modify for similar tests
- Build experiment library over time

**Success Criteria:**
- ✅ Agent proactively updates (not just reactive)
- ✅ Minimal user effort (docs auto-ingested)
- ✅ Team collaboration seamless
- ✅ Integrated into existing workflows (Slack, Drive)

---

**5. Automated Document Watching**
```
User: "Watch this Google Drive folder for experiment updates"
Agent: [monitors folder]

[New doc appears: "Team A Week 2 standup notes.pdf"]
Agent: "I noticed new standup notes from Team A. I've read them and
        updated Week 2 observations. Want to see what I found?"
```
- Connect to Google Drive / Notion
- Monitor tagged folders/pages
- Auto-ingest new docs
- Proactive notifications

**6. Automated Metric Extraction**
```
[Reads standup notes: "Spent 3 hours fixing broken CI pipeline this week"]
Agent: "I detected a process work incident (3 hours on CI).
        Should I count this toward Team A's baseline metrics?"
```
- NLP extraction of metric-relevant data from docs
- Flag for human confirmation before counting
- Reduces manual "here's the number" updates

**Success Criteria:**
- ✅ Agent proactively updates (not just reactive)
- ✅ Minimal user effort (docs auto-ingested)
- ✅ Team collaboration seamless
- ✅ Integrated into existing workflows (Slack, Drive)

---

## Phase 6: AI-Powered Insights & Organizational Learning (v6.0+)

**Timeline:** TBD (visionary - 20+ weeks)
**Goal:** Agent doesn't just track — it provides strategic insights across experiment portfolio

### Moonshot Features:

**1. Cross-Experiment Pattern Recognition**
```
Agent: "I've now tracked 8 experiments for you. I noticed a pattern:

        Experiments with clear baseline metrics (5/8) validated their
        hypothesis 80% of the time.

        Experiments with vague metrics (3/8) were all inconclusive.

        Recommendation: Spend more time defining metrics upfront - it
        predicts experiment success."
```
- Learn from portfolio of experiments
- Identify what makes experiments succeed/fail
- Meta-insights about experimentation itself

**2. Predictive Confidence**
```
[Week 2 of 6-week experiment]
Agent: "Based on Week 1-2 data, I'm projecting:
        • 65% chance of validating hypothesis
        • 20% chance of inconclusive
        • 15% chance of invalidating

        Early indicator: Team B adoption is strong (80%), which correlates
        with success in past experiments.

        Should we continue or adjust course?"
```
- Predict experiment outcomes before completion
- Early warning system for failing experiments
- Suggest course corrections mid-flight

**3. Automated Experiment Design**
```
User: "I think our new code review agent might help quality"
Agent: "Let me design an experiment for you:

        Based on similar experiments (AI tool adoption), I suggest:
        • Metric 1: Defect rate (bugs found in production)
        • Metric 2: Review time (hours from PR to merge)
        • Metric 3: Developer satisfaction (weekly survey)
        • Timeline: 6 weeks (4 weeks baseline, 2 weeks treatment)

        Sound good or want to adjust?"
```
- Learn from past experiments
- Suggest metrics based on intervention type
- Design experiments automatically

**4. Meta-Analysis Across Organization**
```
Agent: "Red Hat has now run 25 experiments using me:
        • 15 validated (60%)
        • 5 invalidated (20%)
        • 5 inconclusive (20%)

        Average confidence: 72% (up from 65% in first 10 experiments)

        Top learning: Teams that run 4-week experiments have 2x validation
        rate vs. 2-week experiments. Consider extending future pilots."
```
- Aggregate learnings across all experiments
- Organizational learning system
- Improve experimentation practice over time

---

## Option B: Custom Software Infrastructure (Future Alternative)

**Timeline:** TBD (if agent-based approach hits limitations)
**Approach:** Build dedicated experiment tracking system (custom code, not LLM agent)
**Build Time:** 8-12 weeks (full dev team)
**Status:** Idea/backup plan - only pursue if agent-based approach proves insufficient

### Why Option B Might Be Needed

**Agent-based approach (current):**
- ✅ Fast to build (~1 hour per major version)
- ✅ Leverages existing Claude Code infrastructure
- ✅ Works immediately, no separate software to maintain
- ⚠️ Relies on LLM following instructions correctly
- ⚠️ Less robust than purpose-built software
- ⚠️ File management is manual (not enforced by code)

**Custom software approach (Option B):**
- ✅ More robust (enforced by code, not LLM instructions)
- ✅ Better error handling and edge case coverage
- ✅ Can integrate with external systems (Jira, GitHub APIs)
- ✅ Scalable to many users/teams
- ⚠️ Takes 8-12 weeks to build (vs. hours for agent)
- ⚠️ Requires separate software to maintain
- ⚠️ More infrastructure complexity

### What Option B Would Include

**1. Dedicated Experiment Management System**
- Database for experiment data (not just files)
- API for creating, updating, retrieving experiments
- Enforced schema validation
- Automated backups

**2. Web/CLI Interface**
- UI for browsing experiments
- Dashboard showing active experiments
- Visual timeline of experiment progress
- Export to various formats (PDF, CSV, JSON)

**3. External Integrations**
- Jira: Auto-pull story points, cycle time, completion %
- GitHub: Auto-pull PR velocity, review time, commit frequency
- Google Drive: Automated document watching and ingestion
- Slack: Proactive notifications and status updates

**4. Multi-User Support**
- User authentication and permissions
- Role-based access control (owner, contributor, viewer)
- Real-time collaboration dashboard
- Activity logs and audit trails

**5. Advanced Analytics**
- Cross-experiment comparison reports
- Pattern recognition across experiment portfolio
- Predictive modeling (early outcome signals)
- Statistical significance testing

**6. Enterprise Features**
- SSO integration
- Data retention policies
- Compliance (GDPR, SOC2)
- White-label branding for Red Hat

### When to Build Option B

**Build it IF:**
- Agent-based approach consistently fails (LLM doesn't follow instructions)
- Need to scale to 50+ teams using simultaneously
- Require strict compliance/audit trails
- Want automated integrations with external systems
- Need real-time collaboration dashboard

**Don't build it IF:**
- Agent-based approach works well for Jerry's use case
- Only 1-5 teams using Experiment Agent
- Manual file management is acceptable
- No immediate need for external integrations

### Estimated Build Plan (If Pursued)

**Week 1-2: Architecture & Design**
- Database schema design
- API specification
- UI/UX mockups
- Integration planning

**Week 3-6: Core Backend**
- Experiment CRUD operations
- File storage and retrieval
- User authentication
- Basic API endpoints

**Week 7-10: Frontend & Integrations**
- Web dashboard
- CLI tool
- Jira/GitHub integrations
- Slack notifications

**Week 11-12: Testing & Polish**
- End-to-end testing
- Performance optimization
- Documentation
- Deployment

**Total: 12 weeks**

### Hybrid Approach (Recommended)

**Phase 1-2:** Use agent-based approach (current)
- Validate product-market fit
- Understand user workflows
- Identify what features are actually used

**Phase 3:** Evaluate
- Is agent-based approach sufficient?
- What limitations are we hitting?
- What would custom software enable?

**Phase 4 (Optional):** Build Option B
- Use learnings from agent-based approach
- Build custom software with validated requirements
- Migrate users gradually (both systems coexist)

**Key Insight:** Don't build Option B until you've validated demand and requirements with agent-based approach. Building custom software first = risk of building wrong thing.

---

## Implementation Priorities (Jerry's Workflow)

Based on Jerry's stated preferences:

### **High Priority (Build First):**

1. **Google Docs link handling** with graceful permission errors
   - Primary workflow: share link
   - Fallback: download PDF if permissions fail

2. **Miro board image interpretation**
   - Extract Experiment Canvas from visual exports
   - Read sticky notes and diagrams

3. **Persistent memory across sessions**
   - Remember experiment context
   - Add docs mid-experiment
   - No re-explaining each week

4. **Multi-doc synthesis** (3-5 docs typical)
   - Read multiple sources
   - Cite which doc facts came from
   - Surface conflicts

### **Medium Priority (Build Next):**

5. **Automated weekly summaries**
   - Reduce burden of "here's my update"
   - Proactive check-ins

6. **Experiment library** (multiple parallel experiments)
   - Track Process Improvement AND Discovery Agent simultaneously
   - Easy context switching

### **Low Priority (Future):**

7. **Email thread parsing** (Jerry said least used)
8. **Slack integration** (nice to have, not critical)
9. **Team collaboration** (Jerry is primary user for now)

---

## Success Metrics for Roadmap

**Phase 1 (v2.1) Success:**
- Jerry starts experiments in <10 min (vs. 1-2 hours)
- 80%+ Experiment Canvas pre-filled from docs
- Jerry runs 2 real experiments successfully

**Phase 2 (v2.5) Success:**
- Agent remembers 100% of context across sessions
- Jerry manages 3+ parallel experiments comfortably
- Weekly updates take <15 min (vs. 30+ min manual)

**Phase 3 (v3.0) Success:**
- Jerry spends <5 min/week on experiment tracking
- Agent proactively flags issues/insights
- 3+ Red Hat teams using Experiment Agent

**Phase 4 (v4.0) Success:**
- Red Hat has 25+ completed experiments tracked
- Meta-insights improve experimentation practice
- Experiment Agent becomes standard practice for strategic decisions

---

## Design Decisions to Revisit

**1. How much pre-filling is too much?**
- Current: Agent pre-fills, user reviews and edits
- Risk: User blindly accepts bad pre-fills
- Mitigation: Highlight uncertainties, require confirmation on key metrics

**2. When to ask for human confirmation?**
- Current: Agent auto-counts metrics, reports in weekly summary
- Alternative: Flag each incident for human confirmation before counting
- Tradeoff: Accuracy vs. user effort

**3. How to handle contradictory docs?**
- Example: Proposal says "4 weeks", meeting notes say "6 weeks"
- Current: Agent surfaces conflict, asks user to resolve
- Alternative: Agent picks most recent, flags the conflict in report

**4. Privacy & access control**
- Who can see experiment data?
- How to handle sensitive experiments (layoffs, reorgs)?
- Enterprise deployment considerations

---

## Open Questions (To Test with Jerry)

1. **Experiment Canvas pre-filling:** 80% good enough, or aim for 95%+?
2. **Weekly updates:** Prefer structured form or free-form narrative?
3. **Metric auto-extraction:** Trust agent to count, or always confirm first?
4. **Report customization:** Standard 9-section format, or let user customize?
5. **Notification preferences:** Proactive weekly summaries, or only when asked?
6. **Multi-user access:** Solo tool for now, or build collaboration early?

---

## Next Steps

**Immediate (Week of April 16, 2026):**
- ✅ v3.0 shipped (Discovery Mode + Review/Approve UX)
- ✅ Roadmap updated with v3.1/v4.0 features
- ⏭️ Test v3.0 Discovery Mode with real experiments
- ⏭️ Validate smart data collection guidance approach

**Near-Term (Next 4-6 Weeks):**
- Plan v3.1 features (check-in reminders, .ics generation, collection guidance)
- Build v3.1 features (1-2 weeks development)
- Test with Jerry on 6-week Experiment Agent pilot

**Mid-Term (Next 2-3 Months):**
- Jerry runs 2-3 real experiments with v3.0/v3.1
- Validate: Does Discovery Mode surface valuable experiments?
- Validate: Do check-in reminders improve adherence?
- Plan v4.0 features (MCP integrations, auto-collection)

---

**This roadmap is a living document. Update as we learn from real usage.**

---

**Maintained by:** Scotty (Engineering) + Jerry (Product)
**Last Review:** April 16, 2026
**Next Review:** After Jerry's Experiment Agent pilot completes (May 29, 2026)
