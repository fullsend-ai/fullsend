// Package legacy implements an install.MintDriver that uses a pre-configured
// mint URL. This is the original install path retained as a separate driver
// so other test configurations can select it.
//
// The driver only manages the mint URL lifecycle. It does not run github
// setup, post-install validation, or per-repo teardown on any target
// repository — that responsibility belongs to the composed install.Driver
// which handles leased pool repos on demand via AllocateRepo.
package legacy

import (
	"context"
	"fmt"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install"
)

// driver uses a pre-configured mint URL.
type driver struct {
	client       forge.Client
	token        string
	binary       string
	mintURL      string
	gcpProjectID string
	logf         func(string, ...any)
}

// NewDriver creates a legacy mint driver that uses the provided
// mintURL. The driver only holds the mint URL; per-repo install is
// handled by the composed install.Driver for each leased pool repo.
func NewDriver(
	client forge.Client,
	token, binary, mintURL, gcpProjectID string,
	logf func(string, ...any),
) (install.MintDriver, error) {
	if mintURL == "" {
		return nil, fmt.Errorf("legacy: mintURL is required")
	}
	return &driver{
		client:       client,
		token:        token,
		binary:       binary,
		mintURL:      mintURL,
		gcpProjectID: gcpProjectID,
		logf:         logf,
	}, nil
}

func (d *driver) Install(_ context.Context, org string) (install.State, error) {
	// The driver only provides the mint URL. Per-repo github setup and
	// post-install validation are handled by the composed install.Driver
	// for each leased pool repo.
	return install.NewPerRepoState(org, "", d.mintURL), nil
}

func (d *driver) Teardown(_ context.Context, _ string, _ install.State) error {
	// The legacy driver has no mint infrastructure to tear down.
	return nil
}
