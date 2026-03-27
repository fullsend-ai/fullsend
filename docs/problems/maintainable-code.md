# Maintainable Code and Cognitive Debt

How do we ensure that developers can continue to understand the code that agents have contributed?

## The Problem: Cognitive Debt

The problem documents that have been created in fullsend thus far repeatedly mention the value of implicit and institutional knowledge to engineers. Less focus has been given to the impacts of the loss of institutional knowledge as the percentage of code contributions made by AI agents increases. There is a wealth of anecdotes from companies that have adopted agentic workflows about the increase in cognitive debt caused by the loss of this knowledge. This makes it harder for humans to contribute or debug when a problem occurs. This is primarily a risk in the middle period of fully-agentic workfllow adoption: the period in which agents are contributing a lot of code but nowhere near all of it. Ensuring that agents contribute quality code can limit knowledge loss. Some of the guardrails described below can be implemented at review-time or implementation-time. By implementing solutions at review-time, we can also improve the quality of human contributions. These are problems that engineering teams have tried to solve since long before the availability of AI tools. While agents give us the ability to create or improve guardrails that we never could before, agentic code also increases the consequences of not having these guardrails.

A handful of areas in which code-quality and anti-cognitive debt guardrails can be implemented have been described below. These can largely be thought of as linting problems, although they are generally too complex or amorphous to be handled by traditional linting tools.

### Idiomatic naming

Variable names that are specific and relevant to their use improve code readability. Because of the code on which AI agents are trained, they often use generic variable names. For example:

```
for i, ref := range(applications) {
    // Do something
}
```

```
for i, application := range(applications) {
    // Do something
}
```

The variable `ref` in the code block above doesn't tell the user anything about what the variable is. By renaming the variable `application` the user can immediately tell at any point during the `for` loop what the variable represents. This makes it considerably easier for humans to review, understand, and contribute to code. PRs submitted by AI agents need to be checked for variable and function names that are clear and idiomatic. In order to support this we will have to define clear rules regarding what makes informative variable and function names. Existing linters may be able to do some or all of this already.

### Proactive refactoring

When AI coding tools like Claude implemnt code, they often struggle to recognize when hard-coded string or small code block is repeated and should be abstracted into its own constant or function. This problem gets worse when the shared code block exists in existing code and new code. Agents also often fail to move code into other files or packages when refactoring. Small, repeatable blocks are a foundation of maintainable code. We should investigate whether we can train an agent to make these sorts of decisions. Claude is clearly capable of refactoring when prompted to. This indicates that implementing a review-time AI agent that recommends sections to refactor could be fairly straightforward. Less straightforward might be to ensure that AI agents contributing code make refactoring decisions without external prompting.

Of all the issues described in this document, this one is likely the most important. 'Spaghettification' of codebases is a common complaint for projects with lots of AI contributions. Proactive refactoring should be one of our first lines of defense against this.

### Intentional comments

Software engineers often say that good code is self-documenting.  However, code comments and documentation serve to provide important context that can be difficult to glean from simply reading the code. This can include clarifying something was implemented a certain way, noting technical debt, or summarizing a complex code block. Documentation such as what a package is meant for or how an API works can also streamline both agentic and human understanding of a codebase and its structure. We should train a review agent to make recommendations about when something needs additional documentation. By doing so we will hopefully make it easier for engineers to understand code changes and decrease the accumulation of cognitive debt.

### Thorough testing

Writing unit tests is something that agents already excel at. Running unit tests and gating on their results does not require AI agents at all. The [Repo Readiness doc](repo-readiness.md) already addresses existing unit test coverage and testing goals to ensure that agentic contributions can be trusted.

The Repo Readiness document does not address the related need for test effectiveness. Unit tests for a given function should attempt to reasonable cover the possible input space for the function. This is something that both agents and engineers often miss in their unit tests. However, since engineers understand the context of the function they're implementing they are better equipped to make sure that the unit t ests not only test all code, but cover a variety of use cases for the code being tested. We should have guardrails in place to ensure that agents are contributing thorough, relevant unit tests. As an additional benefit, these checks will improve the quality of human contributions.


## Pitfalls

Some of the problems described above nebulous. It's difficult to define when a comment is necessary or what makes a variable name useful. This is exactly what's made these problems difficult to solve to begin with. Implementing guardrails for the above problems may require that we opinionate the AI agents used for those guardrails. This would require giving humans the power to disagree with the agents and help along other agents making contributions that may be stuck due to an overly strict review agent.  We will likely need to refine the agents over time as they develop a better understanding of the nuance of these issues. Some of these agents may be relegated to making recommendations rather than gating PRs. By breaking the problem spaces down and determining what is and what is not well defined and/or deterministic, we can hopefully maximize the usefulness of agentic linting guardrails.

## Open questions
- Who decides what makes code idiomatic?
- How strict should review agents be about idiomatic code?
- To what extend does this actually decrease cognitive debt and skill drift? We may need one or more experiments to gain insight into this
- How many of the guardrails described above can be fully or partially implemented using traditional tooling rather than via agentic tooling? See [Agent Infrastructure doc](agent-infrastructure.md) for considerations
- Are there advanced and/or AI-enabled linting tools that are already solving some or all of the problems described above.
