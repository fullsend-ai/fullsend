# Tightening command scanning for a repository

Every shell command an agent runs in the sandbox is checked first by
[tirith](https://github.com/sheeki03/tirith), using its built-in rules. A
repository can make that check stricter for agent runs on it by committing a
tirith policy file. The file can only add restrictions. It cannot allow a
command that the built-in rules block.

## Prerequisites

- Sandbox security hooks enabled for the agent. They are on by default; see
  `security.sandbox_hooks.tirith` in the
  [Harness Field Reference](../../reference/harness-reference.md).
- Write access to the repository's trusted branch (usually the default branch).

## Add a policy

1. Create `.tirith/policy.yaml` at the repository root. This example makes
   tirith ask for approval before a package install it cannot verify offline:

   ```yaml
   approval_rules:
     - rule_ids: [package_lookup_incomplete]
       timeout_secs: 1
   ```

   See tirith's documentation for the fields a policy accepts.

2. Commit the file to the trusted branch and merge it.

3. Start an agent run. The run log shows:

   ```text
   Staging repository tirith policy .tirith/policy.yaml from HEAD
   ```

## What a blocked command looks like

The agent has no one to ask for approval, so an approval rule acts as a block.
With the example above, `pip install requests` is refused with a reason like:

```text
Tirith requires approval (...) (exit code 0) — blocked; this hook cannot collect an approval
```

The agent sees this reason and can try another approach. The block is also
recorded as a `tirith_requires_approval` finding in the run's security
findings.

## What the policy cannot do

- **Loosen scanning.** tirith ignores allowlists, bypass switches and other
  loosening settings in a repository policy. A command the built-in rules block
  stays blocked.
- **Change during a run.** fullsend reads the file from git before the agent
  starts. Edits to the file during the run, by the agent or anyone else, have
  no effect.
- **Come from a pull request.** On pull request and merge request runs (review
  and fix), fullsend uses the copy on the base commit, never the copy in the
  request. On GitHub the base commit is the pull request's `base.sha`. On
  GitLab it is the merge request's merge base, read from the GitLab API. When
  the runner's checkout does not have the base commit, as in a shallow
  checkout, fullsend fetches that one commit from the checkout's `origin`
  remote.

## When the policy is not applied

If a pull or merge request run cannot get its base commit, fullsend applies no
repository policy to that run. This happens when the base commit is not known,
the GitLab API call fails, or the fetch fails. The run still uses tirith's
built-in rules. fullsend reports it in three places:

- A warning in the run log that starts with
  `Repository tirith policy not staged` and gives the reason.
- On GitHub Actions, the same warning as an annotation on the workflow run's
  summary page.
- A line starting with ⚠️ in the run's completion status comment, when your
  status notification settings post one.

To fix a failed fetch, make sure the checkout's `origin` remote can still be
fetched when `fullsend run` starts. For example, keep the checkout's
credentials.

For how fullsend stages and checks the file, see
[Tirith: staged repository policy](../../contributing/runtime-implementation.md#tirith-staged-repository-policy).
