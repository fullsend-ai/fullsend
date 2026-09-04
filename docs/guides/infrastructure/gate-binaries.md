# Creating secure gate binaries

This guide covers building gate binaries — small, statically linked programs that enforce application-level constraints on endpoint access from inside the sandbox. Gate binaries sit between the agent and an external service, validating requests before forwarding them.

## When you need a gate binary

OpenShell's network policy enforces access control at both L4 (TCP endpoint allowlisting) and L7 (HTTP method and path restrictions). L4 enforcement is kernel-backed via network namespace isolation. L7 enforcement is performed by the OpenShell proxy, which inspects HTTP traffic in userspace. Both are per-binary and not bypassable from inside the sandbox. For many access patterns, L4/L7 rules are sufficient — use them when your constraints can be expressed as endpoint allowlists or method + path combinations.

Gate binaries are needed when your access constraints go **beyond** what L4/L7 rules can express:

- **Body/payload filtering** — validate or restrict fields in request bodies before forwarding (e.g., allow `git push` only to specific branches, where the branch ref is encoded in the request body)
- **Query validation** — parse and restrict database queries, rejecting forbidden tables, PII columns, or expensive operations
- **Query parameter enforcement** — restrict which URL query parameters are allowed
- **Schema enforcement** — enforce schemas or size limits on outbound requests
- **Multi-step authorization** — check a condition before allowing an operation (e.g., verify CI status, sign commits or tags before pushing)

If L4/L7 rules cover your case, prefer those — they are proxy-enforced and immune to code injection.

**Why a gate binary?** Gate binaries sit in the request path and provide a feedback loop — the agent gets an immediate error, can reformulate, and retry. That's the key distinction from enforcement mechanisms that run outside the agent's control flow.

## Threat model

The sandbox workspace must be writable and executable for code agents to function. This creates two threats against gate binaries:

1. **LD_PRELOAD injection** — a dynamically linked binary is vulnerable: an agent can write a `.so` to the workspace and use `LD_PRELOAD` to override the binary's validation logic, while the proxy's process-tree ancestry check still sees the legitimate binary. **Mitigation:** build with static linking — the dynamic linker does not process `LD_PRELOAD` for statically linked executables.

2. **Whole-binary replacement** — a gate binary on a writable path can be overwritten by a replacement that skips validation. The proxy's ancestry check still passes because the path matches. **Mitigation:** bake the binary into the container image at a Landlock-protected read-only path (e.g., `/usr/local/bin/`).

For a detailed treatment of the sandbox threat model, see the [binary identity enforcement analysis](https://fullsend.sh/docs/experiments/0013-openshell-policy-bypass) and the [sandbox evaluation experiment](https://fullsend.sh/docs/experiments/0014-openshell-sandbox-evaluation).

## How to build a gate binary

### 1. Write the binary in Go with static linking

Go produces statically linked binaries by default when CGO is disabled. Build with:

```bash
CGO_ENABLED=0 go build -o gate-query ./cmd/gate-query/
```

Verify the binary is statically linked:

```bash
ldd gate-query
# Expected output: "not a dynamic executable"
```

If `ldd` reports any shared libraries, the binary is dynamically linked and vulnerable.

### 2. Structure the binary

A gate binary typically:

1. Parses the agent's request (command-line arguments, stdin, or an HTTP request)
2. Validates the request against its rules
3. Executes the real operation if validation passes
4. Returns the result to the agent

Keep the binary focused — it enforces one set of constraints for one operation. Complex multi-operation gate binaries are harder to audit.

### Example: staging database query gate

This example gate binary wraps `psql`, allowing agents to query a staging database while rejecting forbidden tables, PII columns, and expensive queries:

```go
// cmd/gate-query/main.go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// forbiddenTables lists tables that agents must never query.
var forbiddenTables = []string{
	"users_pii",
	"payment_cards",
	"auth_tokens",
	"audit_credentials",
}

// piiColumns lists column names that must not appear in SELECT clauses.
var piiColumns = []string{
	"ssn",
	"date_of_birth",
	"phone_number",
	"email_address",
	"credit_card",
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: gate-query <sql>\n")
		os.Exit(1)
	}

	query := strings.Join(os.Args[1:], " ")
	upper := strings.ToUpper(query)

	// Reject multi-statement queries (semicolons). psql's -c flag
	// executes semicolon-separated statements, so "SELECT 1; DROP TABLE
	// orders" would bypass the SELECT-only check below.
	if strings.Contains(query, ";") {
		fmt.Fprintf(os.Stderr, "gate-query: rejected — multi-statement queries are not allowed\n")
		os.Exit(1)
	}

	// Reject non-SELECT statements.
	trimmed := strings.TrimSpace(upper)
	if !strings.HasPrefix(trimmed, "SELECT") {
		fmt.Fprintf(os.Stderr, "gate-query: rejected — only SELECT queries are allowed\n")
		os.Exit(1)
	}

	// Reject forbidden tables.
	for _, table := range forbiddenTables {
		if strings.Contains(upper, strings.ToUpper(table)) {
			fmt.Fprintf(os.Stderr, "gate-query: rejected — table %q is forbidden\n", table)
			os.Exit(1)
		}
	}

	// Reject PII columns.
	for _, col := range piiColumns {
		if strings.Contains(upper, strings.ToUpper(col)) {
			fmt.Fprintf(os.Stderr, "gate-query: rejected — column %q contains PII\n", col)
			os.Exit(1)
		}
	}

	// Require LIMIT clause to prevent unbounded result sets.
	if !strings.Contains(upper, "LIMIT") {
		fmt.Fprintf(os.Stderr, "gate-query: rejected — query must include a LIMIT clause\n")
		os.Exit(1)
	}

	cmd := exec.Command("psql",
		"-h", "staging-db.internal",
		"-p", "5432",
		"-U", "agent_ro",
		"-d", "staging",
		"--no-password",
		"-c", query,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.Exit(1)
	}
}
```

Build it:

```bash
CGO_ENABLED=0 go build -o gate-query ./cmd/gate-query/
```

The agent invokes the binary with a SQL query:

```bash
gate-query "SELECT id, status, created_at FROM orders WHERE region = 'us-east-1' LIMIT 50"
```

If the query is rejected, the binary prints the reason to stderr and exits non-zero. The agent reads the error and reformulates:

```
gate-query: rejected — query must include a LIMIT clause
```

> **Production note:** The string-matching validation above is intentionally simple for illustration. It blocks obvious cases — including multi-statement injection via semicolons — but has two categories of limitation. **False positives (safe direction):** the semicolon check rejects semicolons inside SQL string literals (e.g., `SELECT name FROM t WHERE bio LIKE '%;%' LIMIT 10`), which are safe but rejected. **False negatives (bypass vectors):** it cannot catch `dblink()` calls that query forbidden tables on a remote server, `pg_read_file()` to read arbitrary files, CTEs, or subqueries that reference forbidden tables indirectly. A production gate binary should use a SQL parser (e.g., `github.com/pingcap/tidb/parser` or `github.com/sqlc-dev/sqlc`) to reliably detect table references, column selections, JOINs on non-indexed columns, and ORDER BY clauses targeting unindexed columns.

## Configuring the sandbox policy

The gate binary must be the **only** binary allowed to reach the endpoint it guards. If another binary (or a wildcard pattern) can also reach the same endpoint, the agent can bypass the gate by using that binary directly.

### Provider profile for the gate binary

Create a provider profile that grants the gate binary access to the guarded endpoint. The profile's top-level `binaries` field restricts which executables can use its endpoints — only binaries matching the glob patterns are allowed to connect:

```yaml
# profiles/gate-query.yaml
---
id: gate-query
display_name: Gate Query
description: Restricted staging database access for the gate-query binary
category: data
endpoints:
  - host: staging-db.internal
    port: 5432
    protocol: tcp
    access: read-only
    enforcement: enforce
binaries:
  - "/usr/local/bin/gate-query"
```

Use `protocol: tcp` for non-HTTP endpoints like database connections — it creates an L4 tunnel without L7 HTTP inspection. Use `protocol: rest` only for HTTP endpoints where the proxy should inspect method and path.

The `binaries` path points to a read-only location inside the container image — not the writable workspace. See [Threat model](#threat-model) for why this matters.

### Provider definition

Create a provider definition that references the profile by its `id`:

```yaml
# providers/gate-query.yaml
name: gate-query
type: gate-query
# The gate binary handles its own authentication or needs no credentials.
# This placeholder credential exists only to satisfy OpenShell's requirement
# that every provider declares at least one credential key. The provider
# definition's sole purpose is to attach the network policy from the profile.
# See: https://github.com/NVIDIA/OpenShell/issues/1978
credentials:
  _PLACEHOLDER_GATE_QUERY: ""
```

### Harness configuration

Reference the provider in your harness file. The gate binary is baked into the container image — it is **not** delivered via `host_files`:

```yaml
# harness/code.yaml
providers:
  - providers/gate-query.yaml
openshell:
  profiles:
    - profiles/gate-query.yaml
```

The binary must be added to the container image's Dockerfile at build time:

```dockerfile
COPY gate-query /usr/local/bin/gate-query
```

This places it on a Landlock-protected read-only path that the agent cannot overwrite.

### Key policy rules

1. **No overlapping access.** Ensure no other provider profile or inline network policy grants access to the guarded endpoint with a different `binaries` pattern or a wildcard. If `psql` can also reach `staging-db.internal:5432`, the gate binary is bypassed.

2. **Binary path must be read-only.** The `binaries` path in the profile must point to a Landlock-protected location (e.g., `/usr/local/bin/`). Do not deliver gate binaries via `host_files` to the writable workspace — an agent can overwrite the binary and bypass validation. See [Threat model](#threat-model).

3. **Binary path must be exact.** The `binaries` glob patterns in the profile are matched against the binary path via process-tree ancestry tracking. Use the absolute path where the binary is installed in the container image.

### Credential collision limitation

If both the gate binary and a general-purpose binary need the same credential environment variable, OpenShell rejects the sandbox at creation time: `credential env key is provided by both provider '...' and provider '...'`. For example, a hypothetical `gate-gh` binary (restricting which repositories can be forked) and the standard `gh` CLI both need `GH_TOKEN` to reach github.com — two providers cannot both declare the same credential key ([NVIDIA/OpenShell#2330](https://github.com/NVIDIA/OpenShell/issues/2330) tracks composable provider profiles that would solve this).

Until OpenShell#2330 lands, use one of these workarounds:

- **Credential-less provider with a placeholder key.** When the gate binary handles its own authentication — or needs no credentials at all — the provider definition exists only to attach the network policy from the profile. OpenShell requires every provider to declare at least one credential key, so you supply a placeholder (as in the `_PLACEHOLDER_GATE_QUERY` example above) that carries no real secret. This avoids colliding with credential keys declared by other providers.
- **Single provider for both binaries.** If both binaries must share the same credential, use a single provider with a `binaries` list that includes both. This means the `binaries` field cannot differentiate between them — the gate binary's validation logic is the only enforcement.

## What the proxy enforces vs. what the binary enforces

Understanding the enforcement boundary is critical for correct policy design:

| Layer | Enforced by | Bypassable from sandbox? | Examples |
|-------|-------------|--------------------------|----------|
| Endpoint allowlist | Kernel (network namespace) | No — kernel-backed | Which hosts and ports are reachable |
| Method/path rules | OpenShell proxy (L7) | No — proxy-enforced | `GET /api/v3/repos/*` only |
| Binary identity | OpenShell proxy (process-tree ancestry) | No — proxy-enforced via unforgeable `/proc` data | Only `gate-query` and its child processes can reach `staging-db.internal:5432` |
| Binary integrity | Landlock read-only path | No — kernel-backed | Agent cannot overwrite `/usr/local/bin/gate-query` |
| Application logic | Gate binary code | Only if dynamically linked (`LD_PRELOAD`) | Query validation, table filtering, PII column blocking |

The first four rows are proxy- or kernel-enforced and hold regardless of what runs inside the sandbox. Binary identity uses process-tree ancestry — OpenShell walks `/proc/pid/` ancestry to verify that the connecting process (e.g., `psql` spawned by the gate binary) descends from an allowed binary. This is why child processes spawned by the gate binary inherit network access. The fifth row — the gate binary's own validation logic — is the binary's responsibility. Static linking protects it from `LD_PRELOAD` injection, making the application logic tamper-resistant.

## Checklist

Before deploying a gate binary:

- [ ] Binary is built with `CGO_ENABLED=0` (Go) or equivalent static linking
- [ ] `ldd <binary>` reports "not a dynamic executable"
- [ ] Binary is baked into the container image at a read-only path (e.g., `/usr/local/bin/`)
- [ ] Binary is **not** delivered via `host_files` to the writable workspace
- [ ] Provider profile `binaries` path matches the read-only install path in the container image
- [ ] Provider profile restricts the guarded endpoint to the gate binary only
- [ ] No other profile or network policy grants overlapping access to the same endpoint
- [ ] No credential collision with other providers (see [Credential collision limitation](#credential-collision-limitation))
- [ ] Gate binary validates all relevant request fields (not just the obvious ones)

## See also

- [Runtime implementation](../../contributing/runtime-implementation.md) — Binary identity enforcement details, sandbox hook contract, and workspace layout
- [Bring Your Own Agent](../user/bring-your-own-agent.md) — Harness configuration, providers, and profiles
- [ADR 0065](../../ADRs/0065-provider-backed-policy-composition.md) — Provider-backed policy composition
