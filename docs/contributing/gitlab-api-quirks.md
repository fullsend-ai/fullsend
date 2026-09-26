---
title: GitLab API Quirks
---

# GitLab API Quirks

GitLab's rule-based access APIs (protected branches, protected tags,
protected environments, and similar) do not behave like a single lookup
by resource name. Read this before adding or changing GitLab
access-control logic in `internal/forge/gitlab/`.

## Rule-based access APIs (protected branches/tags/environments)

Three properties of these APIs have already caused silent failures and
permission-expansion bugs
([#7665](https://github.com/fullsend-ai/fullsend/issues/7665),
[PR #7671](https://github.com/fullsend-ai/fullsend/pull/7671)). Treat
them as invariants for any new GitLab rule-based access work.

### Exact-name and wildcard rules both apply

A resource can be covered by:

1. An **exact-name** rule (the rule's `name` equals the branch, tag, or
   environment), and/or
2. One or more **wildcard-pattern** rules (for example `*`, `main*`)

GitLab's `*` matches any run of characters, including `/`. A 404 from
the exact-name endpoint (`GET .../protected_branches/:branch`) does
**not** mean the resource is unprotected — a wildcard rule may still
match.

### Effective access is the union of every matching rule

GitLab unions access across **every** matching rule. Do not take the
first match. A Maintainer-only exact rule for `main` can coexist with a
Developer-allowed `main*` wildcard; the effective access is the more
permissive combination of both.

### Never PATCH a wildcard rule to grant one resource

When the intent is to grant access to a **single** exact resource (for
example adding the poller to `allowed_to_merge` on the default branch),
PATCH only an exact-name rule. PATCHing a wildcard widens access to
every name that wildcard matches.

If no exact-name rule exists, **fail closed**: do not PATCH the
wildcard, and tell the operator to add an exact-name rule (or grant the
needed access another way). Creating a new exact-name rule from code is
a product decision, not a silent fallback.

### Canonical worked example

[`internal/forge/gitlab/ci.go`](../../internal/forge/gitlab/ci.go)
implements this for protected branches:

- `GetProtectedBranch` — exact-name lookup plus every matching
  wildcard, then merge
- `mergeProtectedBranchRules` — unions access levels; when there is no
  exact-name rule, keeps a wildcard `Name` so callers can tell PATCH is
  unsafe
- `listMatchingWildcardProtectedBranches` — returns **every** matching
  wildcard, not the first
- `GrantProtectedBranchMergeUser` — PATCHes only the exact-name rule;
  fails closed when the branch is wildcard-only

**When reviewing PRs:** Flag GitLab rule-based access code that treats
an exact-name 404 as unprotected, that takes the first matching
wildcard, or that PATCHes a wildcard rule to grant access to one
resource. These are the same silent-failure / permission-expansion class
as #7665 / #7671.
