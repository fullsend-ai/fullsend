package world

import "context"

type worldContextKey struct{}

// WithWorld returns a new context that carries w.
func WithWorld(ctx context.Context, w *World) context.Context {
	return context.WithValue(ctx, worldContextKey{}, w)
}

// FromContext extracts the per-scenario World stored by WithWorld.
// Returns nil if no World is present.
func FromContext(ctx context.Context) *World {
	w, _ := ctx.Value(worldContextKey{}).(*World)
	return w
}
