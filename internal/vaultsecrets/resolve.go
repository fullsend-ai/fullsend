package vaultsecrets

import (
	"context"
	"fmt"
	"net/http"
	"sort"
)

// Source is one runner secret selected for Vault: the secret name (also
// the data.data field read), its connection, and its ordered KV v2 path
// templates.
type Source struct {
	Name           string
	ConnectionName string
	Connection     Connection
	Paths          []string
}

// TokenSource returns the job's OIDC token for audience. The token is a
// bearer credential; implementations must not log it.
type TokenSource func(ctx context.Context, audience string) (string, error)

// Resolver resolves Vault-selected runner secrets.
type Resolver struct {
	// Repo is the trusted target-repository context for path templates.
	Repo RepoContext
	// Token fetches the job OIDC token.
	Token TokenSource
	// HTTPClient overrides the Vault HTTP client (tests only).
	HTTPClient *http.Client
}

// expanded is a Source with its templates expanded.
type expanded struct {
	Source
	paths []string
}

// Resolve returns name → value for every source. Every path is expanded
// and validated before any OIDC token is requested. Candidates are tried
// in order: a missing or denied path, or one without a non-empty string
// field named for the secret, advances to the next; any other Vault
// failure aborts. A source with no candidate value is an error; there is
// no fallback to another credential source.
func (r Resolver) Resolve(ctx context.Context, sources []Source) (map[string]string, error) {
	if len(sources) == 0 {
		return nil, nil
	}
	if r.Token == nil {
		return nil, fmt.Errorf("no job OIDC token source is available")
	}
	byConn := map[string][]expanded{}
	conns := map[string]Connection{}
	for _, s := range sources {
		if len(s.Paths) == 0 {
			return nil, fmt.Errorf("secret %q: vault_secrets is empty", s.Name)
		}
		e := expanded{Source: s}
		for i, tmpl := range s.Paths {
			p, err := ExpandPath(tmpl, r.Repo)
			if err != nil {
				return nil, fmt.Errorf("secret %q: vault_secrets[%d]: %w", s.Name, i, err)
			}
			e.paths = append(e.paths, p)
		}
		byConn[s.ConnectionName] = append(byConn[s.ConnectionName], e)
		conns[s.ConnectionName] = s.Connection
	}
	names := make([]string, 0, len(byConn))
	for n := range byConn {
		names = append(names, n)
	}
	sort.Strings(names)

	clients := make(map[string]*Client, len(names))
	for _, n := range names {
		c, err := NewClient(conns[n], r.HTTPClient)
		if err != nil {
			return nil, fmt.Errorf("connection %q: %w", n, err)
		}
		clients[n] = c
	}

	values := make(map[string]string, len(sources))
	for _, n := range names {
		if err := r.resolveConnection(ctx, n, clients[n], byConn[n], values); err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (r Resolver) resolveConnection(ctx context.Context, name string, c *Client, sources []expanded, values map[string]string) error {
	jwt, err := r.Token(ctx, c.conn.Audience)
	if err != nil {
		return fmt.Errorf("connection %q: requesting job OIDC token: %w", name, err)
	}
	token, err := c.Login(ctx, jwt)
	if err != nil {
		return fmt.Errorf("connection %q: %w", name, err)
	}
	defer func() { _ = c.Revoke(context.WithoutCancel(ctx), token) }()
	for _, s := range sources {
		found := false
		for _, p := range s.paths {
			v, ok, err := c.ReadField(ctx, token, p, s.Name)
			if err != nil {
				return fmt.Errorf("secret %q (connection %q): %w", s.Name, name, err)
			}
			if ok {
				values[s.Name] = v
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("secret %q (connection %q): no vault_secrets candidate holds a non-empty %q field; not falling back to the native source", s.Name, name, s.Name)
		}
	}
	return nil
}
