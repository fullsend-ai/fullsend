package mintcore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNewFilesystemPEMAccessor(t *testing.T) {
	t.Run("valid directory", func(t *testing.T) {
		dir := t.TempDir()
		acc, err := NewFilesystemPEMAccessor(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if acc == nil {
			t.Fatal("expected non-nil accessor")
		}
	})

	t.Run("non-existent directory", func(t *testing.T) {
		_, err := NewFilesystemPEMAccessor("/nonexistent/path")
		if err == nil {
			t.Fatal("expected error for non-existent directory")
		}
	})

	t.Run("path is a file", func(t *testing.T) {
		dir := t.TempDir()
		f := filepath.Join(dir, "notadir")
		os.WriteFile(f, []byte("x"), 0600)
		_, err := NewFilesystemPEMAccessor(f)
		if err == nil {
			t.Fatal("expected error for file path")
		}
	})
}

func TestFilesystemPEMAccessor_AccessPEM(t *testing.T) {
	dir := t.TempDir()
	coderPEM := []byte("not-a-real-key-coder\n")
	triagePEM := []byte("not-a-real-key-triage\n")
	os.WriteFile(filepath.Join(dir, "coder.pem"), coderPEM, 0600)
	os.WriteFile(filepath.Join(dir, "triage.pem"), triagePEM, 0600)

	acc, err := NewFilesystemPEMAccessor(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ctx := context.Background()

	t.Run("valid role", func(t *testing.T) {
		data, err := acc.AccessPEM(ctx, "coder")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(data) != string(coderPEM) {
			t.Fatalf("got %q, want %q", data, coderPEM)
		}
	})

	t.Run("fix aliases to coder", func(t *testing.T) {
		data, err := acc.AccessPEM(ctx, "fix")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(data) != string(coderPEM) {
			t.Fatalf("fix should read coder PEM; got %q", data)
		}
	})

	t.Run("different role", func(t *testing.T) {
		data, err := acc.AccessPEM(ctx, "triage")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(data) != string(triagePEM) {
			t.Fatalf("got %q, want %q", data, triagePEM)
		}
	})

	t.Run("missing PEM file", func(t *testing.T) {
		_, err := acc.AccessPEM(ctx, "review")
		if err == nil {
			t.Fatal("expected error for missing PEM")
		}
	})

	t.Run("invalid role name", func(t *testing.T) {
		_, err := acc.AccessPEM(ctx, "INVALID")
		if err == nil {
			t.Fatal("expected error for invalid role name")
		}
	})

	t.Run("empty role name", func(t *testing.T) {
		_, err := acc.AccessPEM(ctx, "")
		if err == nil {
			t.Fatal("expected error for empty role name")
		}
	})
}
