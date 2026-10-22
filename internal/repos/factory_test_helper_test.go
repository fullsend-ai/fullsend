package repos

import (
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

// testClientFactory wraps a single forge client as a ForgeClientFactory
// for use in tests. It returns the same client for any forge name, matching
// the single-client test pattern that existed before ForgeClientFactory.
type testClientFactory struct {
	client forge.Client
}

func newTestClientFactory(fc forge.Client) ForgeClientFactory {
	return &testClientFactory{client: fc}
}

func (f *testClientFactory) ConfigFor(forgeName string) (ForgeConfig, error) {
	if forgeName == "" {
		forgeName = ForgeGitHub
	}
	cfg := ForgeConfigFor(forgeName)
	cfg.Client = f.client
	return cfg, nil
}

// perForgeClientFactory returns a distinct client or error per forge name,
// so tests can prove a forge's client was never constructed (e.g. that a
// filtered operation never needed that forge's credentials).
type perForgeClientFactory struct {
	clients map[string]forge.Client
	errs    map[string]error
}

func (f *perForgeClientFactory) ConfigFor(forgeName string) (ForgeConfig, error) {
	if forgeName == "" {
		forgeName = ForgeGitHub
	}
	if err := f.errs[forgeName]; err != nil {
		return ForgeConfig{}, err
	}
	client, ok := f.clients[forgeName]
	if !ok {
		return ForgeConfig{}, fmt.Errorf("no test client for forge %q", forgeName)
	}
	cfg := ForgeConfigFor(forgeName)
	cfg.Client = client
	return cfg, nil
}
