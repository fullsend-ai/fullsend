package runtime

import "github.com/fullsend-ai/fullsend/internal/sandbox"

// jiraTokenPlaceholderEnv is the env var the fullsend-jira provider's
// placeholder reaches the sandbox in.
const jiraTokenPlaceholderEnv = "JIRA_TOKEN"

// jiraSeedExit is the shell `exit` statement of a failed Jira token seed.
const jiraSeedExit = "exit 1"

// JiraTokenSeed returns the credential seed for the sandbox's Jira token
// file (sandbox.SandboxJiraTokenFile). The runner runs it through `sandbox
// exec` once the sandbox is up and again after every refresh of a minted
// Jira access token (fullsend#9220). Unlike the inference routes it does
// not depend on the runtime: the Jira skills read the file themselves.
func JiraTokenSeed() CredentialSeed {
	return CredentialSeed{
		PlaceholderEnv: jiraTokenPlaceholderEnv,
		Seed:           jiraTokenSeedFragment(sandbox.SandboxJiraDir, sandbox.SandboxJiraTokenFile),
		File:           sandbox.SandboxJiraTokenFile,
	}
}

// jiraTokenSeedFragment is the POSIX sh fragment that writes the
// placeholder the sandbox environment carries for JIRA_TOKEN, followed by
// a newline, into file. A value that is not a gateway placeholder fails
// the seed: a real token in the sandbox environment would mean the
// provider path was bypassed, and copying it into a file would spread it.
// The write goes through orderedSeedWrite, so a seed holding an older
// generation never replaces a newer one.
func jiraTokenSeedFragment(dir, file string) string {
	return `case "${JIRA_TOKEN:-}" in ` + piPlaceholderPrefix + `*JIRA_TOKEN) ;; *) echo 'fullsend: JIRA_TOKEN in the sandbox is not a gateway placeholder (jira provider not attached, or a real token reached the sandbox); refusing to seed the Jira token file' >&2; ` + jiraSeedExit + ` ;; esac` +
		` && case "$JIRA_TOKEN" in *[!A-Za-z0-9_:]*) echo 'fullsend: JIRA_TOKEN placeholder has unexpected characters; refusing to seed the Jira token file' >&2; ` + jiraSeedExit + ` ;; esac` +
		` && ` + orderedSeedWrite("JIRA_TOKEN", dir, file,
		`printf '%s\n' "$JIRA_TOKEN"`,
		`fullsend: writing the Jira token file failed`, jiraSeedExit)
}
