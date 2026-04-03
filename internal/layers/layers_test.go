package layers

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLayer implements Layer and records calls for test verification.
type mockLayer struct {
	name         string
	installErr   error
	uninstallErr error
	analyzeErr   error
	report       *LayerReport
	scopes       map[Operation][]string

	installCalled   bool
	uninstallCalled bool
	analyzeCalled   bool
	callOrder       *[]string // shared slice to track ordering
}

func (m *mockLayer) Name() string { return m.name }

func (m *mockLayer) RequiredScopes(op Operation) []string {
	if m.scopes != nil {
		return m.scopes[op]
	}
	return nil
}

func (m *mockLayer) Install(_ context.Context) error {
	m.installCalled = true
	if m.callOrder != nil {
		*m.callOrder = append(*m.callOrder, m.name)
	}
	return m.installErr
}

func (m *mockLayer) Uninstall(_ context.Context) error {
	m.uninstallCalled = true
	if m.callOrder != nil {
		*m.callOrder = append(*m.callOrder, m.name)
	}
	return m.uninstallErr
}

func (m *mockLayer) Analyze(_ context.Context) (*LayerReport, error) {
	m.analyzeCalled = true
	if m.analyzeErr != nil {
		return nil, m.analyzeErr
	}
	return m.report, nil
}

func TestLayerStatus_String(t *testing.T) {
	tests := []struct {
		status LayerStatus
		want   string
	}{
		{StatusNotInstalled, "not installed"},
		{StatusInstalled, "installed"},
		{StatusDegraded, "degraded"},
		{StatusUnknown, "unknown"},
		{LayerStatus(99), "LayerStatus(99)"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.status.String())
		})
	}
}

func TestStack_InstallAll_Success(t *testing.T) {
	var order []string
	l1 := &mockLayer{name: "first", callOrder: &order}
	l2 := &mockLayer{name: "second", callOrder: &order}
	l3 := &mockLayer{name: "third", callOrder: &order}

	stack := NewStack(l1, l2, l3)
	err := stack.InstallAll(context.Background())

	require.NoError(t, err)
	assert.True(t, l1.installCalled)
	assert.True(t, l2.installCalled)
	assert.True(t, l3.installCalled)
	assert.Equal(t, []string{"first", "second", "third"}, order)
}

func TestStack_InstallAll_StopsOnError(t *testing.T) {
	var order []string
	l1 := &mockLayer{name: "first", callOrder: &order}
	l2 := &mockLayer{name: "second", callOrder: &order, installErr: errors.New("boom")}
	l3 := &mockLayer{name: "third", callOrder: &order}

	stack := NewStack(l1, l2, l3)
	err := stack.InstallAll(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "layer second")
	assert.Contains(t, err.Error(), "boom")
	assert.True(t, l1.installCalled)
	assert.True(t, l2.installCalled)
	assert.False(t, l3.installCalled, "third layer should not be called after second fails")
	assert.Equal(t, []string{"first", "second"}, order)
}

func TestStack_InstallAll_Cancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	l1 := &mockLayer{name: "first"}
	stack := NewStack(l1)
	err := stack.InstallAll(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "cancelled before layer first")
	assert.False(t, l1.installCalled, "layer should not be called on cancelled context")
}

func TestStack_UninstallAll_ReverseOrder(t *testing.T) {
	var order []string
	l1 := &mockLayer{name: "first", callOrder: &order}
	l2 := &mockLayer{name: "second", callOrder: &order}
	l3 := &mockLayer{name: "third", callOrder: &order}

	stack := NewStack(l1, l2, l3)
	errs := stack.UninstallAll(context.Background())

	assert.Empty(t, errs)
	assert.True(t, l1.uninstallCalled)
	assert.True(t, l2.uninstallCalled)
	assert.True(t, l3.uninstallCalled)
	assert.Equal(t, []string{"third", "second", "first"}, order)
}

func TestStack_UninstallAll_CollectsErrors(t *testing.T) {
	l1 := &mockLayer{name: "first", uninstallErr: errors.New("err1")}
	l2 := &mockLayer{name: "second"}
	l3 := &mockLayer{name: "third", uninstallErr: errors.New("err3")}

	stack := NewStack(l1, l2, l3)
	errs := stack.UninstallAll(context.Background())

	require.Len(t, errs, 2)
	// Reverse order: third fails first, then first fails
	assert.Contains(t, errs[0].Error(), "layer third")
	assert.Contains(t, errs[1].Error(), "layer first")
	// All layers attempted despite errors
	assert.True(t, l1.uninstallCalled)
	assert.True(t, l2.uninstallCalled)
	assert.True(t, l3.uninstallCalled)
}

func TestStack_AnalyzeAll_Success(t *testing.T) {
	l1 := &mockLayer{
		name: "first",
		report: &LayerReport{
			Name:   "first",
			Status: StatusInstalled,
		},
	}
	l2 := &mockLayer{
		name: "second",
		report: &LayerReport{
			Name:         "second",
			Status:       StatusDegraded,
			Details:      []string{"missing config"},
			WouldFix:     []string{"recreate config file"},
			WouldInstall: []string{},
		},
	}
	l3 := &mockLayer{
		name: "third",
		report: &LayerReport{
			Name:         "third",
			Status:       StatusNotInstalled,
			WouldInstall: []string{"create workflow file"},
		},
	}

	stack := NewStack(l1, l2, l3)
	reports, err := stack.AnalyzeAll(context.Background())

	require.NoError(t, err)
	require.Len(t, reports, 3)
	assert.Equal(t, "first", reports[0].Name)
	assert.Equal(t, StatusInstalled, reports[0].Status)
	assert.Equal(t, "second", reports[1].Name)
	assert.Equal(t, StatusDegraded, reports[1].Status)
	assert.Equal(t, "third", reports[2].Name)
	assert.Equal(t, StatusNotInstalled, reports[2].Status)
}

func TestStack_AnalyzeAll_Error(t *testing.T) {
	l1 := &mockLayer{
		name: "first",
		report: &LayerReport{
			Name:   "first",
			Status: StatusInstalled,
		},
	}
	l2 := &mockLayer{
		name:       "second",
		analyzeErr: fmt.Errorf("cannot reach API"),
	}
	l3 := &mockLayer{
		name: "third",
		report: &LayerReport{
			Name:   "third",
			Status: StatusInstalled,
		},
	}

	stack := NewStack(l1, l2, l3)
	reports, err := stack.AnalyzeAll(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "analyzing layer second")
	assert.Contains(t, err.Error(), "cannot reach API")
	// Should have the first report collected before the error
	require.Len(t, reports, 1)
	assert.Equal(t, "first", reports[0].Name)
	// Third layer should not have been called
	assert.False(t, l3.analyzeCalled)
}

func TestStack_Empty(t *testing.T) {
	stack := NewStack()

	err := stack.InstallAll(context.Background())
	assert.NoError(t, err)

	errs := stack.UninstallAll(context.Background())
	assert.Empty(t, errs)

	reports, err := stack.AnalyzeAll(context.Background())
	assert.NoError(t, err)
	assert.Empty(t, reports)
}

func TestStack_Layers(t *testing.T) {
	l1 := &mockLayer{name: "a"}
	l2 := &mockLayer{name: "b"}
	stack := NewStack(l1, l2)

	got := stack.Layers()
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].Name())
	assert.Equal(t, "b", got[1].Name())
}
