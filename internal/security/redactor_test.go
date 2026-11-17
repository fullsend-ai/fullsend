package security

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSecretRedactor_SchemePrefixedCredentials covers a credential behind a
// Basic/Bearer/Token scheme word in env assignments, headers and keys
// (#9487). The value is base64 of a fake user:password pair.
func TestSecretRedactor_SchemePrefixedCredentials(t *testing.T) {
	fake := base64.StdEncoding.EncodeToString([]byte("fakefakefakefake:fakefakefakefakefake"))
	masked := fake[:4] + "..."
	r := NewSecretRedactor()

	cases := []struct {
		name    string
		input   string
		pattern string
		kept    string // must survive in the sanitized output, mask included
	}{
		{"env basic lowercase", `export SVC_TOKEN="basic ` + fake + `"`, "env_assignment", `export SVC_TOKEN="basic ` + masked + `"`},
		{"env bearer", `export SVC_TOKEN="Bearer ` + fake + `"`, "env_assignment", `export SVC_TOKEN="Bearer ` + masked + `"`},
		{"env auth basic", `SVC_AUTH="Basic ` + fake + `"`, "env_assignment", `SVC_AUTH="Basic ` + masked + `"`},
		{"header basic", "Authorization: Basic " + fake, "auth_header", "Authorization: Basic " + masked},
		{"header token", "Authorization: Token " + fake, "auth_header", "Authorization: Token " + masked},
		{"json authorization key", `{"Authorization": "Bearer ` + fake + `"}`, "json_field", `{"Authorization": "`},
		{"nested json authorization key", `{"headers": {"authorization": "token ` + fake + `"}}`, "json_field", `{"headers": {"authorization": "`},
		{"python dict authorization key", `{'Authorization': 'Basic ` + fake + `'}`, "json_field", `{'Authorization': '`},
		{"yaml authorization", `authorization: "Bearer ` + fake + `"`, "auth_header", `authorization: "Bearer ` + masked + `"`},
		{"env unprefixed", `export SVC_TOKEN="` + fake + `"`, "env_assignment", `export SVC_TOKEN="` + masked + `"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := r.Scan(tc.input)
			assert.False(t, res.Safe)
			assert.True(t, hasFinding(res, tc.pattern), "want finding %s, got %+v", tc.pattern, res.Findings)
			assert.NotContains(t, res.Sanitized, fake)
			assert.Contains(t, res.Sanitized, tc.kept)
		})
	}
}

func TestSecretRedactor_SchemeWithoutCredentialUntouched(t *testing.T) {
	r := NewSecretRedactor()
	for _, input := range []string{
		`token = "Bearer " + tok`,
		`AUTH_SCHEME="Bearer"`,
		`headers["Authorization"] = "Bearer " + token`,
		// A bare value still ends at the first space.
		"SVC_TOKEN=Bearer short",
	} {
		t.Run(input, func(t *testing.T) {
			res := r.Scan(input)
			assert.True(t, res.Safe, "unexpected findings: %+v", res.Findings)
			assert.Empty(t, res.Sanitized)
		})
	}
}
