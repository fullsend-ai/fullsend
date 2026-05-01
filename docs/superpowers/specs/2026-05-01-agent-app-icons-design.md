# Agent App Icons

Upload role-specific icons to GitHub Apps created during `fullsend admin install`.

## Problem

GitHub Apps created via the manifest flow have no logo. All four agent apps (fullsend, triage, coder, review) look identical in the GitHub UI — generic gray octocat avatars. Custom icons make it easy to distinguish agents at a glance in PR timelines, commit lists, and org settings.

## Design

### Embedded icons

Store four PNG icons in `internal/forge/github/icons/` and embed them into the binary with `//go:embed`. A lookup function returns the icon bytes for a given role.

| Role | Source file |
|------|-------------|
| fullsend | bootstrap.png |
| triage | triage.png |
| coder | coder.png |
| review | review.png |

Icons are 330x330 circular PNGs with transparent backgrounds, extracted from the project's icon sheet.

### Role-to-icon lookup

New file `internal/forge/github/icons.go`:

```go
func IconForRole(role string) ([]byte, bool)
```

Returns the embedded PNG bytes and true if the role has an icon, or nil and false otherwise. Unknown roles (including future ones like prioritize, scribe, retro) return false until their icons are added.

### JWT generation

New file `internal/appsetup/jwt.go`:

```go
func generateAppJWT(appID int64, pemKey []byte) (string, error)
```

Generates an RS256 JWT with `iss` (app ID), `iat` (now - 60s), and `exp` (now + 5min). Uses only stdlib (`crypto/rsa`, `crypto/x509`, `encoding/pem`, `encoding/json`, `encoding/base64`). No external JWT library needed — GitHub App JWTs have a fixed structure.

### Logo upload

New file `internal/appsetup/logo.go`:

```go
func uploadAppLogo(ctx context.Context, jwtToken string, logo []byte) error
```

Sends `PATCH https://api.github.com/app` with:
- `Authorization: Bearer <jwt>`
- `Content-Type: multipart/form-data`
- Form field `logo` containing the PNG bytes

### Integration

In `appsetup.go`, after `exchangeManifestCode` returns credentials:

1. Look up the icon for the role via `IconForRole(role)`
2. If found, generate a JWT from the returned `AppID` and `PEM`
3. Call `uploadAppLogo` with the JWT and icon bytes
4. Log success or warning on failure

Logo upload failure logs a warning but does not fail the install. The icon is cosmetic.

### Testing

- `icons.go`: table test that all four roles return non-empty bytes, unknown roles return false
- `jwt.go`: test that output has three base64url segments and correct header/claims structure
- `logo.go`: test against an `httptest` server that verifies multipart form structure and auth header
- Integration in `appsetup_test.go`: verify `runManifestFlow` attempts logo upload after exchange (mock server)

## Files changed

| File | Change |
|------|--------|
| `internal/forge/github/icons/bootstrap.png` | New — embedded icon |
| `internal/forge/github/icons/triage.png` | New — embedded icon |
| `internal/forge/github/icons/coder.png` | New — embedded icon |
| `internal/forge/github/icons/review.png` | New — embedded icon |
| `internal/forge/github/icons.go` | New — embed directive + `IconForRole` |
| `internal/forge/github/icons_test.go` | New — tests for icon lookup |
| `internal/appsetup/jwt.go` | New — JWT generation |
| `internal/appsetup/jwt_test.go` | New — JWT structure tests |
| `internal/appsetup/logo.go` | New — logo upload |
| `internal/appsetup/logo_test.go` | New — logo upload tests |
| `internal/appsetup/appsetup.go` | Modified — call logo upload after exchange |

## Out of scope

- Icons for future agents (prioritize, scribe, retro) — added when those agents ship
- Updating icons on existing apps — only applies during initial creation
- Resizing or format conversion — icons are pre-sized PNGs
