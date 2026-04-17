# Seed App Generation Prompt

You are setting up a minimal fictional application called "TaskFlow" for use in
an automated experiment. TaskFlow is a command-line task management application.

## What to create

Create a minimal but realistic Node.js application in the `seed-app/` directory
with the following structure:

```
seed-app/
  package.json
  src/
    index.js          # CLI entry point
    task-store.js     # In-memory + file-based task storage
    search.js         # Search functionality (title + description)
    import-csv.js     # CSV import for tasks
    auth.js           # SSO authentication stub
  test/
    task-store.test.js
    search.test.js
```

## Requirements

The app should have these real (but minimal) features:

1. **Task storage**: CRUD operations on tasks (id, title, description, status).
   Stores in a JSON file. Has a save function that serializes to disk.

2. **Search**: Full-text search across task titles and descriptions. Uses a
   simple in-memory approach (no real FTS index — this is intentional, as the
   "slow search" bug is about this).

3. **CSV import**: Import tasks from a CSV file. Does NOT sanitize special
   characters (this is intentional — the "crash on save" bug involves non-ASCII
   characters from CSV import).

4. **Auth stub**: A minimal SSO authentication flow that validates tokens and
   checks email claims. Has a session cookie mechanism with SameSite=Strict
   (this is intentional — the "auth redirect loop" bug involves this).

## Important

- Keep it minimal. This is a prop for an experiment, not production software.
- Each feature should be 20-50 lines of code.
- Include actual bugs that match the experiment scenarios (see above) — the code
  should have the exact failure modes described in the scenarios.
- Include basic tests that pass (the bugs should only manifest under the specific
  conditions described in the scenarios).
- Use Node.js with no external dependencies (just built-in modules).
