// Package install provides pool-repo lifecycle management for behaviour
// tests: lazy repo creation, fullsend installation, pool leasing, and
// teardown.
//
// # Credential context separation
//
// Two distinct credential contexts operate on pool repos. Understanding
// this separation is essential when working on repo-readiness checks
// after pool repo recreation.
//
// The test suite holds an e2e GitHub App installation token, minted via
// OIDC at CI job start. This token drives all suite-side API calls:
// repo creation/deletion, label application, PR management, and the
// readiness-polling functions in this package (awaitCreation,
// awaitDeletion, awaitWorkflowReady).
//
// Dispatch workflows (harness-dispatch, harness-run) operate with the
// pool repo's own GITHUB_TOKEN — a per-workflow-run token scoped to the
// repository where the workflow executes. This token is used by
// harness-dispatch to call GetCollaboratorPermission when authorizing
// the triggering actor.
//
// These are independent credential contexts with independent permission
// propagation graphs. When a pool repo is deleted and recreated
// (resetRepo), the suite's e2e token can observe the new repo via
// GetRepo (awaitCreation) as soon as the repo object propagates through
// GitHub's eventual-consistency layer. However, the dispatch-side
// GITHUB_TOKEN's view of collaborator permissions on the new repo ID
// propagates on a separate, slower schedule that the suite cannot
// observe or predict.
//
// Consequence: the suite CANNOT reliably probe or wait for dispatch-side
// permission readiness by calling GetCollaboratorPermission from the
// suite side. The suite's e2e installation token and dispatch's
// GITHUB_TOKEN resolve permissions through different GitHub subsystems.
// Polling GetCollaboratorPermission with the suite's token tells you
// nothing about whether dispatch's token will see the correct
// permissions. This was empirically validated in issue #6701: both a
// human developer (PR #6703) and an autonomous agent (PR #6709)
// independently attempted suite-side permission polling with
// exponential backoff, and both failed — 0 of 12 repos resolved over
// 63 seconds.
//
// Any fix for dispatch-side permission propagation delays on freshly
// recreated repos must operate on the dispatch side (e.g., retry within
// the dispatch workflow itself) rather than from the suite side.
package install
