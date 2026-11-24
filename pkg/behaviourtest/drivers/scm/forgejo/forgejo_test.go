package forgejo

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/scm"
)

// TestEveryOperationIsNotImplemented calls every scm.Driver method on
// the stub and requires a not-implemented error naming that method, so
// a method added to the interface cannot silently succeed or fall back.
func TestEveryOperationIsNotImplemented(t *testing.T) {
	t.Parallel()

	d := reflect.ValueOf(New(forge.NewFakeClient()))
	iface := reflect.TypeFor[scm.Driver]()
	require.Positive(t, iface.NumMethod())
	ctxType := reflect.TypeFor[context.Context]()
	errType := reflect.TypeFor[error]()

	for method := range iface.Methods() {
		name := method.Name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m := d.MethodByName(name)
			mt := m.Type()
			numIn := mt.NumIn()
			if mt.IsVariadic() {
				numIn--
			}
			args := make([]reflect.Value, numIn)
			for j := range numIn {
				if mt.In(j) == ctxType {
					args[j] = reflect.ValueOf(context.Background())
					continue
				}
				args[j] = reflect.Zero(mt.In(j))
			}
			out := m.Call(args)
			last := out[len(out)-1]
			require.Equal(t, errType, last.Type())
			err, _ := last.Interface().(error)
			require.ErrorIs(t, err, drivers.ErrNotImplemented)

			var nie *drivers.NotImplementedError
			require.True(t, errors.As(err, &nie))
			assert.Equal(t, "forgejo SCM", nie.Driver)
			assert.Equal(t, name, nie.Operation)
			for _, v := range out[:len(out)-1] {
				assert.True(t, v.IsZero(), "%s returned a non-zero value with its error", name)
			}
		})
	}
}

func TestNewKeepsClient(t *testing.T) {
	t.Parallel()

	client := forge.NewFakeClient()
	d, ok := New(client).(*Driver)
	require.True(t, ok)
	assert.Same(t, client, d.Client)
}
