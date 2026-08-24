package repos

import (
	"context"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestRefResolver(t *testing.T) {
	fc := forge.NewFakeClient()

	t.Run("resolves tag to SHA", func(t *testing.T) {
		fc.Refs["fullsend-ai/fullsend/tags/v0.35.0"] = "abc123def0000000000000000000000000000000"
		r := NewRefResolver(fc)
		got := r.Resolve(context.Background(), "v0.35.0")
		if got != "abc123def0000000000000000000000000000000" {
			t.Errorf("Resolve(v0.35.0) = %q, want SHA", got)
		}
	})

	t.Run("resolves branch when tag not found", func(t *testing.T) {
		fc.Refs["fullsend-ai/fullsend/heads/main"] = "mainsha00000000000000000000000000000000"
		r := NewRefResolver(fc)
		got := r.Resolve(context.Background(), "main")
		if got != "mainsha00000000000000000000000000000000" {
			t.Errorf("Resolve(main) = %q, want SHA", got)
		}
	})

	t.Run("returns SHA unchanged", func(t *testing.T) {
		r := NewRefResolver(fc)
		sha := "abc123def0000000000000000000000000000000"
		got := r.Resolve(context.Background(), sha)
		if got != sha {
			t.Errorf("Resolve(SHA) = %q, want unchanged", got)
		}
	})

	t.Run("returns ref on error", func(t *testing.T) {
		r := NewRefResolver(fc)
		got := r.Resolve(context.Background(), "nonexistent-tag")
		if got != "nonexistent-tag" {
			t.Errorf("Resolve(nonexistent) = %q, want original", got)
		}
	})

	t.Run("caches result", func(t *testing.T) {
		fc2 := forge.NewFakeClient()
		fc2.Refs["fullsend-ai/fullsend/tags/v1.0.0"] = "cached000000000000000000000000000000000"
		r := NewRefResolver(fc2)
		got1 := r.Resolve(context.Background(), "v1.0.0")
		delete(fc2.Refs, "fullsend-ai/fullsend/tags/v1.0.0")
		got2 := r.Resolve(context.Background(), "v1.0.0")
		if got1 != got2 {
			t.Errorf("second resolve should return cached result: got1=%q got2=%q", got1, got2)
		}
	})
}
