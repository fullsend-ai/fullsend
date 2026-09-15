//go:build js

package mintcore

import (
	"context"
	"fmt"
	"syscall/js"
)

// awaitPromise blocks until a JS Promise resolves or rejects.
func awaitPromise(promise js.Value) (js.Value, error) {
	ch := make(chan js.Value, 1)
	errCh := make(chan error, 1)

	thenFn := js.FuncOf(func(_ js.Value, args []js.Value) interface{} {
		ch <- args[0]
		return nil
	})
	defer thenFn.Release()

	catchFn := js.FuncOf(func(_ js.Value, args []js.Value) interface{} {
		errCh <- fmt.Errorf("%s", args[0].String())
		return nil
	})
	defer catchFn.Release()

	promise.Call("then", thenFn).Call("catch", catchFn)

	select {
	case v := <-ch:
		return v, nil
	case err := <-errCh:
		return js.Value{}, err
	}
}

// awaitPromiseWithContext blocks until a JS Promise resolves, rejects,
// or the context is canceled. On context cancellation, it returns
// ctx.Err() immediately. The underlying JS Promise remains in-flight;
// a background goroutine releases the callback resources once it
// settles.
func awaitPromiseWithContext(ctx context.Context, promise js.Value) (js.Value, error) {
	type promiseResult struct {
		val js.Value
		err error
	}

	done := make(chan promiseResult, 1)

	thenFn := js.FuncOf(func(_ js.Value, args []js.Value) interface{} {
		done <- promiseResult{val: args[0]}
		return nil
	})

	catchFn := js.FuncOf(func(_ js.Value, args []js.Value) interface{} {
		done <- promiseResult{err: fmt.Errorf("%s", args[0].String())}
		return nil
	})

	promise.Call("then", thenFn).Call("catch", catchFn)

	select {
	case r := <-done:
		thenFn.Release()
		catchFn.Release()
		return r.val, r.err
	case <-ctx.Done():
		// The JS Promise is still in-flight — we cannot cancel it from
		// Go. Spawn a goroutine to release callback resources once the
		// Promise eventually settles. The goroutine is bounded: it
		// completes when the underlying fetch/PEM lookup finishes (or
		// the isolate is recycled).
		go func() {
			<-done
			thenFn.Release()
			catchFn.Release()
		}()
		return js.Value{}, ctx.Err()
	}
}
