# E2E organization pool

This directory holds the code that configures the GitHub organizations used
for e2e testing: `halfsend-01` … `halfsend-12` for DEV, and `halfsend` for
STAGE. See
[Pool org provisioning](../../docs/guides/dev/e2e-testing.md#pool-org-provisioning)
for the full requirements.

Setup is split into three scripts, run in this order:

1. `setup-manual.sh` — interactive operator steps. Run once per org.
2. `enroll.sh` — one-time per-repo mint enrollment. Run once per org by a
   GCP admin.
3. `setup-org.sh` — non-interactive configuration. Safe to run in CI.

Both source `lib.sh`, which holds the shared helpers. `setup-org_test.sh`
tests the helpers against a stubbed `gh` (run via `make test`).

## Manual steps (`setup-manual.sh`)

These steps cannot be automated and must be done by an operator with an
org-admin `gh` session. The script walks through them, pausing for each:

1. **Create the org.** The script opens
   `https://github.com/organizations/plan` and waits. Create the org with
   the name `halfsend-NN` (or `halfsend` for STAGE) and the contact email
   from `ORG_CONTACT_EMAIL` (required; the script fails without it).
2. **Make `botsend` an owner.** If `botsend` is not a member, the script
   sends an admin invitation. Accept it and promote `botsend` to owner.
3. **Invite the test actors.** The script invites `fstest-write` and
   `fstest-triage` as org members. The invitations are accepted later by
   `setup-org.sh` in CI, using the actor PATs stored as repository secrets
   (`TEST_ACTOR_WRITE_PAT`, `TEST_ACTOR_TRIAGE_PAT`).
4. **Install the GitHub Apps.** The script opens the install page for each
   missing app: the six `fullsend-test-*` role apps and `fullsend-ai-e2e`.
   Grant access to "All repositories" and click Install.

```bash
ORG_CONTACT_EMAIL=<contact-email> e2e/org-pool/setup-manual.sh halfsend-NN
```

## Mint enrollment (`enroll.sh`)

Per-repo mint enrollment is a one-time operation per repo name: it updates
the mint allowlist (`PER_REPO_WIF_REPOS`) and the WIF provider bindings on
the hosted mint GCP project. CI does **not** do it — the test runner lacks
the GCP admin IAM it needs — so it stays an operator step, scripted here:

```bash
GCP_PROJECT=<mint-project> e2e/org-pool/enroll.sh NN
```

Set `REGION` to override the mint region (default `us-central1`).

The script enrolls `test-repo` and the behaviour pool slots `test-repo-01`
… `test-repo-12`. It is idempotent, and the GitHub repositories need not
exist — enroll is an allowlist/WIF update only. Do not enroll `*-fork`
names or the `*-mint-host` / `*-mint-unapproved` probe hosts. Re-run when
adding a new pool org or after mint infrastructure changes that drop
`PER_REPO_WIF_REPOS` entries. See
[mint administration](../../docs/guides/infrastructure/mint-administration.md)
for required operator IAM.

## Execution (`setup-org.sh`)

Once the manual steps are done, `setup-org.sh` completes the configuration.
It never pauses and never opens a browser, so it can run unattended. CI
execution is planned as a follow-up once the scripts land on `main`
(#8886); until then an org owner runs it per org.
It is idempotent: it checks each prerequisite and only acts on what is
missing. It verifies the manual prerequisites and fails with a message to
run `setup-manual.sh` when one is missing.

It creates `test-repo`, accepts pending test actor invitations with the
actor PATs (`TEST_ACTOR_WRITE_PAT`, `TEST_ACTOR_TRIAGE_PAT`), assigns the
all-repository write and triage roles to the test actors, verifies the
outsider model (`fstest-outsider` has no org membership and no
all-repository role — this fails closed), creates the `mint-status` team
with `fstest-write` as its only member, and sets `FULLSEND_FOREIGN_E2E_REPOS`
to authorize `fullsend-ai/fullsend`.

```bash
e2e/org-pool/setup-org.sh halfsend-NN
```

After the run, verify the foreign mint authorization:

```bash
go run ./cmd/fullsend admin foreign list --org halfsend-NN
# expect e2e → fullsend-ai/fullsend
```

Then add the org to `orgPool` in `internal/e2etest/testutil.go`
(and update `DevPoolOrgCount`/`DevPoolLayout` and their tests) and run
`make behaviour-test`.
