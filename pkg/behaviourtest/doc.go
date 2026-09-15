// Package behaviourtest is the public entry point for running Gherkin
// behaviour tests against fullsend.
//
// Call RunSuite from a test file built with the `behaviour` tag. The
// in-repo runner (e2e/behaviour/suite_test.go) and external consumers
// such as fullsend-ai/agents share this API. Driver selection, org
// acquisition, concurrency, and step registration are handled
// internally from the same environment variables as the in-repo suite.
package behaviourtest
