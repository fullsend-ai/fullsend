# Experiment Agent Setup Guide

This guide helps you set up the Experiment Agent so it automatically loads when you say "run an experiment" in any Claude Code conversation.

---

## Who This Is For

- **Full Send team members:** Use this to enable the agent everywhere (not just in the fullsend repo)
- **External users:** Use this to set up the agent for your own work

---

## Setup Options

### Option 1: Auto-Load Everywhere (Recommended) ⭐

**What you get:**
- Say "run an experiment" in ANY directory → Agent loads automatically
- Same seamless UX in all conversations
- No need to remember file paths

**Setup time:** 5 minutes

#### Step-by-Step Instructions

**1. Get the Experiment Agent files**

**If you're on the Full Send team:**
```bash
# Already in fullsend repo? You're good!
cd ~/fullsend/experiments/experiment-agent/
```

**If you're external:**
```bash
# Option A: Clone full repo
git clone https://github.com/fullsend-ai/fullsend.git
cd fullsend/experiments/experiment-agent/

# Option B: Download just the experiment-agent folder
# Go to https://github.com/fullsend-ai/fullsend
# Navigate to experiments/experiment-agent/
# Download: experiment_agent_v3.0.md, V3.0_RELEASE_NOTES.md, V3_DISCOVERY_MODE_TEST_GUIDE.md
```

**2. Find your Claude memory directory**

```bash
# Check if it exists (replace [username] with your actual username)
ls ~/.claude/projects/-Users-[username]/memory/

# If it doesn't exist, Claude Code will create it when you first use memory
# Or create it manually:
mkdir -p ~/.claude/projects/-Users-[username]/memory/
```

**3. Create the memory reference file**

```bash
# Navigate to your memory directory
cd ~/.claude/projects/-Users-[username]/memory/

# Create the reference file
nano reference_experiment_agent.md
```

**4. Paste this content:**

```markdown
---
name: Experiment Agent Tool
description: AI-powered strategic experiment tracker with bias-corrected methodology - load when user wants to run experiments
type: reference
---
When I ask to run an experiment, track a strategic test, or validate a hypothesis through experimentation, load the Experiment Agent definition.

**Location:** `~/fullsend/experiments/experiment-agent/experiment_agent_v3.0.md`

**Capabilities:**
- **Discovery Mode:** Analyzes repos/docs, suggests experiments, prioritizes by value
- **Execution Mode:** Bias-corrected experiment design (strict metrics, devil's advocate)
- Progressive disclosure UX (1-3 questions at a time)
- Persistent memory for multi-week experiments
- Cost-benefit tracking (surfaces hidden costs that gut-feel approaches miss)
- Balanced reporting (benefits + costs + contradictions)
- Pre-populated experiment canvas (saves 10-15 min setup)

**Proven value:**
- Prevents million-dollar mistakes ($10.47M disasters caught in testing)
- 5x better stakeholder defense (9/10 vs 4/10 without agent)
- 70-80% time savings vs manual tracking

**When to use:**
- Testing process changes (meetings, workflows, standups)
- Evaluating tools (should we adopt X?)
- Product experiments (does feature Y improve outcomes?)
- Design experiments (does UI change Z help users?)
- Any strategic decision that needs evidence vs gut-feel

**Invocation examples:**
- "Let's run an experiment"
- "I want to test if daily standups work"
- "Help me design an experiment for [idea]"
- "Can you load the experiment agent?"

**How to apply:** When I mention running experiments, immediately offer to load the Experiment Agent and guide me through the bias-corrected methodology.
```

**Note:** Update the `Location:` path if you put the files somewhere other than `~/fullsend/experiments/experiment-agent/`

Save and exit (Ctrl+X, then Y, then Enter in nano)

**5. Add to your memory index**

```bash
# Open or create MEMORY.md in the same directory
nano MEMORY.md
```

Add this line:
```markdown
- [Experiment Agent Tool](reference_experiment_agent.md) — Load when user wants to run experiments - bias-corrected methodology
```

Save and exit.

**6. Test it!**

Open a new Claude Code conversation and say:
```
"Let's run an experiment"
```

The agent should automatically load and ask if you have an experiment in mind or want suggestions!

---

### Option 2: Repo-Specific (Full Send Team Only)

**What you get:**
- Auto-loads when working in the fullsend directory
- No personal setup needed

**Already configured!** Just be in the fullsend repo and say "run an experiment"

**Limitation:** Only works in fullsend directory, not in other projects

---

### Option 3: Manual Load (No Setup)

**What you get:**
- Works immediately, no configuration needed
- Full control over when to load

**How to use:**
```
"Load the Experiment Agent at ~/fullsend/experiments/experiment-agent/experiment_agent_v3.0.md"
```

**Limitation:** You have to type the full path each time

---

## Troubleshooting

### "Agent didn't auto-load when I said 'run an experiment'"

**Check:**
1. Is the reference file in the right location?
   ```bash
   ls ~/.claude/projects/-Users-[username]/memory/reference_experiment_agent.md
   ```
2. Is the path in the reference file correct?
   ```bash
   cat ~/.claude/projects/-Users-[username]/memory/reference_experiment_agent.md | grep Location
   ```
3. Does the agent definition file exist at that path?
   ```bash
   ls ~/fullsend/experiments/experiment-agent/experiment_agent_v3.0.md
   ```

### "File not found" error

Update the `Location:` path in your reference file to match where you actually put the agent files.

### "Memory directory doesn't exist"

Create it:
```bash
mkdir -p ~/.claude/projects/-Users-$(whoami)/memory/
```

---

## What's Next?

Once set up, try Discovery Mode:
1. Say "Let's run an experiment"
2. Choose "B - Suggest experiments"
3. Provide a GitHub repo or strategy doc to analyze
4. Agent will suggest 5 prioritized experiments
5. Pick one, review pre-populated design, approve, and start tracking!

See [V3_DISCOVERY_MODE_TEST_GUIDE.md](./V3_DISCOVERY_MODE_TEST_GUIDE.md) for detailed testing instructions.

---

## Questions?

**For Full Send team:** Ask in #fullsend Slack channel

**For external users:** Open an issue at https://github.com/fullsend-ai/fullsend/issues

---

**Prepared by:** Jerry Becker (Innovation Manager, Red Hat)
**Date:** April 15, 2026
