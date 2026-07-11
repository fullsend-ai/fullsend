//go:build behaviour

package emulate

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	defaultToken = "e2e-emulate-token" //nolint:gosec // local-only emulator credential, not a real secret
	defaultOrg   = "emu-org"
	defaultRepo  = "emu-repo"
	botLogin     = "e2e-bot"
)

// SeedOptions describes the minimal fixture an emulate instance needs for
// scm.Driver scenarios. Extend with an "apps" block when a test needs
// GitHub App JWT / installation-token flows (e.g. token-mint regression
// cases) — emulate supports it, this struct just doesn't expose it yet.
type SeedOptions struct {
	Org   string
	Repo  string
	Token string
}

func (o SeedOptions) withDefaults() SeedOptions {
	if o.Org == "" {
		o.Org = defaultOrg
	}
	if o.Repo == "" {
		o.Repo = defaultRepo
	}
	if o.Token == "" {
		o.Token = defaultToken
	}
	return o
}

// writeSeedFile renders opts into an emulate seed YAML file in a temp
// location and returns its path and the token to authenticate with.
func writeSeedFile(opts SeedOptions) (path, token string, err error) {
	opts = opts.withDefaults()

	cfg := map[string]any{
		"tokens": map[string]any{
			opts.Token: map[string]any{
				"login":  botLogin,
				"scopes": []string{"repo"},
			},
		},
		"github": map[string]any{
			// A token's login must resolve to a real seeded user: the
			// emulator's assertAuthenticatedUser looks up the actor by
			// login in the users store separately from the token map, so
			// an unseeded login 401s with the same "Requires
			// authentication" message as a missing token entirely.
			"users": []map[string]any{
				{"login": botLogin, "name": "E2E Bot", "email": botLogin + "@example.com"},
			},
			"orgs": []map[string]any{
				{"login": opts.Org, "name": opts.Org},
			},
			"repos": []map[string]any{
				{"owner": opts.Org, "name": opts.Repo, "auto_init": true},
			},
		},
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return "", "", err
	}

	f, err := os.CreateTemp("", "emulate-seed-*.yaml")
	if err != nil {
		return "", "", err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", "", err
	}
	return filepath.Clean(f.Name()), opts.Token, nil
}
