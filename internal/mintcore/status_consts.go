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
// audience). Stamped into the deployed binary at build/deploy time.
// When non-empty together with StatusCFAccessTeam, the Cloudflare
// Access status validator is active and validates JWTs from the
// Cf-Access-Jwt-Assertion header against the configured application.
var StatusCFAccessAud string

// StatusCFAccessTeam is the Cloudflare Zero Trust team subdomain
// (e.g. "acme" for acme.cloudflareaccess.com). Stamped into the
// deployed binary at build/deploy time. Together with StatusCFAccessAud,
// this determines the issuer and JWKS endpoint for CF Access JWT
// validation.
var StatusCFAccessTeam string
