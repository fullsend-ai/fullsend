package dispatch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDispatcherInterface(t *testing.T) {
	// Verify the Dispatcher interface exists and has the expected methods.
	// This is a compile-time check exercised at test time.
	var _ Dispatcher = (*stubDispatcher)(nil)
}

// stubDispatcher is a minimal implementation to verify the interface compiles.
type stubDispatcher struct{}

func (s *stubDispatcher) Name() string { return "stub" }
func (s *stubDispatcher) Provision(_ context.Context) (map[string]string, error) {
	return nil, nil
}
func (s *stubDispatcher) StoreAgentPEM(_ context.Context, _ string, _ []byte) error {
	return nil
}
func (s *stubDispatcher) OrgSecretNames() []string   { return nil }
func (s *stubDispatcher) OrgVariableNames() []string { return nil }

func TestStubDispatcher(t *testing.T) {
	d := &stubDispatcher{}
	assert.Equal(t, "stub", d.Name())
	assert.Nil(t, d.OrgSecretNames())
	assert.Nil(t, d.OrgVariableNames())
}
