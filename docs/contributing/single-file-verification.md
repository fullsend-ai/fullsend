---
title: Single-File Verification
---

# Single-File Verification

Use these commands to lint or type-check one changed file without a full-suite
run. They are for fast iteration while editing. They do not replace
`make lint` (stage first) or `make go-test` before commit.

Target under five seconds per file. Do not pass `./...` or `.` — those are
full-tree invocations, not single-file checks.

## Go

Prefer the package-scoped form. It matches `make go-vet` / `go vet ./...`
semantics and works whether or not siblings share the package:

```bash
go vet ./path/to/pkg/
golangci-lint run ./path/to/pkg/
```

The file-path form is only reliable for single-file packages (for example
`cmd/fullsend/main.go`). In multi-file packages — most of `internal/` and
`cmd/` — passing just the changed file makes Go synthesize a package from
that file alone, omitting siblings, which spuriously reports
undefined-symbol errors:

```bash
go vet ./path/to/file.go
golangci-lint run ./path/to/file.go
```

## Python

```bash
uvx ruff check path/to/file.py
uvx ty check --ignore unresolved-import --ignore unresolved-attribute path/to/file.py
```

`ruff check path/to/file.py` is equivalent when `ruff` is already on `PATH`.
The `ty` flags match the pre-commit hook in `.pre-commit-config.yaml`.

## TypeScript

```bash
npx tsc --noEmit path/to/file.ts
```

Passing a path to `tsc` does not load the nearest `tsconfig.json`. For the
Cloudflare Worker adapter, run `npm run typecheck` from
`internal/dispatch/cf/workersrc/` instead.
