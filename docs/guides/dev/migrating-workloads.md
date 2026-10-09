# Migrating agentic workloads to Fullsend

Fullsend is a software that allow users to automate their software lifecycle
with the help of agent runtimes and Large Language Models. By default it ships
a series of agents that take the common steps of the lifecycle and automate them:
triage, code, review... These agents cover just a portion of the lifecycle and
while it can be enough for most projects, it may not be for others.

This document is the counterpart for [Bring your own agent](../user/bring-your-own-agent.md)
and explains in more detail part of the process of bringing your own agent to Fullsend,
therefore to migrate your current agentic process and work to Fullsend.

## Identify Your Workload

First you need to identify and decide what unit you want to migrate. Normally
this means a skill or a group of skills.

Fullsend has the flexibility to ship all your workloads into a single agent with
a large number of skills, however that means that the prompt of the agent will
need to asses which one to use and then load it. If the list is large the agent
can get its context filed pretty quickly. We recommend you to group skills
per object they work on, for example an issue, a pull request, etc.

## Understanding Execution

After identifying the different workloads you have, you need to
understand how Fullsend is meant to work and decide which event makes sense
for your skills.

Fullsend uses CEL expressions to decide when an agent should be executed,
and those rely on a normalized event that refers to an entity (issue,
change_proposal, conversation...). This flexibility allows to configure
agents to execute automatically when the proper event is received or when
a comment is left in an entity.

**Note**: this focus on a single entity makes migrating workloads that work on a
series of a bit more challenging, but it can be done. Search our docs for examples
of different uses and workloads.

For example let's consider a set of skills that should be applied to new issues.

## Creating
