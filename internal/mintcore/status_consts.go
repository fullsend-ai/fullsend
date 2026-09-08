package mintcore

// StatusGitHubGroup is stamped into the deployed binary at
// build/deploy time, the same mechanism used for Version and Commit.
// In development and tests it defaults to the empty string.
//
// StatusGitHubGroup is an ORG/TEAM slug. When the github build tag
// is active, the GitHub status validator checks that the caller is a
// member of this team. When the tag is absent (stub), the value is
// unused.
var StatusGitHubGroup string

// StatusCFAccessAud is the Cloudflare Access application AUD (JWT
// audience). Stamped into the deployed binary at build/deploy time
// via --status-auth=cfaccess --status-cfaccess-aud=<aud>.
// When the cfaccess build tag is active, the Cloudflare Access status
// validator uses this value to authenticate requests via Cloudflare
// Access Managed OAuth. When the tag is absent (stub), the value is
// unused.
var StatusCFAccessAud string

// StatusCFAccessTeam is the Cloudflare Zero Trust team subdomain
// (e.g. "acme" for acme.cloudflareaccess.com). Stamped into the
// deployed binary at build/deploy time via
// --status-auth=cfaccess --status-cfaccess-team=<team>.
// When the cfaccess build tag is active, together with
// StatusCFAccessAud, this determines the issuer and JWKS endpoint
// for Cloudflare Access JWT validation. When the tag is absent
// (stub), the value is unused.
var StatusCFAccessTeam string
