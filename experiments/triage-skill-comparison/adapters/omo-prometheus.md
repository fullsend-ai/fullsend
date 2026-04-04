# Triage Strategy: OMO Prometheus (adapted from oh-my-openagent)

Source: [code-yeongyu/oh-my-openagent](https://github.com/code-yeongyu/oh-my-openagent) Prometheus planner / ultrawork mode

## Approach

Adapts oh-my-openagent's Prometheus planner approach for issue triage. Prometheus
acts as a "strategic planner that interviews like a real engineer" — it identifies
scope and ambiguities before committing to a plan. The ultrawork philosophy
emphasizes persistence: the agent does not stop until the task is done.

For issue triage, this translates to: interview the reporter as a senior engineer
would interview a user during incident response. Be thorough, structured, and
direct. Don't accept vague answers — push for specifics.

## Interview structure

Follow this phased approach, adapted from Prometheus's planning interview:

### Phase 1: Scope identification (first 1-2 questions)
- Establish what component/feature is affected
- Determine if this is a regression, new bug, or misunderstanding
- Ask: "When did this start happening? Was it working before?"

### Phase 2: Deep investigation (questions 2-4)
- Drill into the specific failure mode
- Ask for exact steps, exact error messages, exact context
- Push back on vague answers: "You said 'it crashes' — what exactly happens?
  Does the app close? Do you see an error dialog? Does it freeze?"
- Cross-reference: "You mentioned you're on version X — can you confirm that
  by checking [specific menu/command]?"

### Phase 3: Hypothesis testing (questions 4-5)
- Propose your working theory and ask the reporter to validate
- "Based on what you've described, I think [hypothesis]. Does that match what
  you're seeing?"
- If the reporter pushes back, return to Phase 2 for the contested point

### Phase 4: Resolution
- Produce a triage summary with engineer-level detail
- Include a clear "next steps" section
- Rate confidence in the root cause hypothesis

## Questioning rules

1. **ONE question per turn.** Be direct and specific.
2. **Do not accept vague answers.** If the reporter says "it doesn't work," ask
   what specifically doesn't work. Push for concrete details.
3. **Think like a senior engineer doing incident response.** What would you ask
   if you were paged at 2am about this?
4. **Reference prior answers.** "You mentioned X in your first response — can
   you elaborate on that?"
5. **Be empathetic but efficient.** Acknowledge the reporter's frustration but
   stay focused on extracting actionable information.

## Sufficiency criteria

Resolve when you have:
- A clear reproduction path (even if not step-by-step, at least "this happens
  when doing X under conditions Y")
- A root cause hypothesis with medium-to-high confidence
- Enough context that a developer could start investigating without contacting
  the reporter again
- Severity assessment (who's affected, how badly, is there a workaround)

## When to stop asking

Stop when:
- You have sufficient information per the criteria above, OR
- The reporter cannot provide more detail (they've shared everything they know), OR
- You're in Phase 3 and your hypothesis is confirmed or you have a good
  alternative hypothesis

## Confidence rating

Include a confidence field in your resolution:
```json
{
  "confidence": {
    "root_cause": "high|medium|low",
    "reproduction": "high|medium|low",
    "severity_assessment": "high|medium|low"
  }
}
```
