package mintcore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// FilesystemPEMAccessor reads agent PEMs from a local directory.
// PEM files are named {role}.pem (e.g. coder.pem, triage.pem).
type FilesystemPEMAccessor struct {
	pemDir string
}

// NewFilesystemPEMAccessor creates a PEM accessor that reads from a local directory.
// The directory must exist at construction time.
func NewFilesystemPEMAccessor(pemDir string) (*FilesystemPEMAccessor, error) {
	info, err := os.Stat(pemDir)
	if err != nil {
		return nil, fmt.Errorf("PEM directory %q: %w", pemDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("PEM path %q is not a directory", pemDir)
	}
	return &FilesystemPEMAccessor{pemDir: pemDir}, nil
}

func (f *FilesystemPEMAccessor) AccessPEM(_ context.Context, role string) ([]byte, error) {
	secretRole := PemSecretRole(role)
	if err := ValidateRoleName(secretRole); err != nil {
		return nil, err
	}
	path := filepath.Join(f.pemDir, secretRole+".pem")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading PEM for role %q: %w", role, err)
	}
	WarnWorkersPEMSize(secretRole, data)
	return data, nil
}
