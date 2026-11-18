package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"github.com/theupdateframework/go-tuf/v2/metadata/fetcher"
)

// The identity that signs fullsend releases: keyless cosign signing in the
// release workflow.
const (
	githubOIDCIssuer = "https://token.actions.githubusercontent.com"
	releaseRepoURI   = "https://github.com/fullsend-ai/fullsend"
	releaseWorkflow  = releaseRepoURI + "/.github/workflows/release.yml"
)

// signerIdentity returns the certificate identity that must sign the
// release tagged tag. Pinning the tag stops one release's signature from
// vouching for another.
func signerIdentity(tag string) (verify.CertificateIdentity, error) {
	ref := "refs/tags/" + tag
	san, err := verify.NewSANMatcher(releaseWorkflow+"@"+ref, "")
	if err != nil {
		return verify.CertificateIdentity{}, err
	}
	issuer, err := verify.NewIssuerMatcher(githubOIDCIssuer, "")
	if err != nil {
		return verify.CertificateIdentity{}, err
	}
	return verify.NewCertificateIdentity(san, issuer, certificate.Extensions{
		SourceRepositoryURI: releaseRepoURI,
		SourceRepositoryRef: ref,
		RunnerEnvironment:   "github-hosted",
	})
}

// verifyChecksums checks that bundleJSON is a valid Sigstore signature over
// checksums, made by the release workflow for tag.
func verifyChecksums(trusted root.TrustedMaterial, bundleJSON, checksums []byte, tag string) error {
	var b bundle.Bundle
	if err := b.UnmarshalJSON(bundleJSON); err != nil {
		return fmt.Errorf("parsing signature bundle: %w", err)
	}
	verifier, err := verify.NewVerifier(trusted,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
	if err != nil {
		return fmt.Errorf("creating signature verifier: %w", err)
	}
	identity, err := signerIdentity(tag)
	if err != nil {
		return fmt.Errorf("building signer identity: %w", err)
	}
	policy := verify.NewPolicy(
		verify.WithArtifact(bytes.NewReader(checksums)),
		verify.WithCertificateIdentity(identity),
	)
	if _, err := verifier.Verify(&b, policy); err != nil {
		return err
	}
	return nil
}

// verifyReleaseSignature verifies a release signature against the Sigstore
// public-good trusted root.
func verifyReleaseSignature(ctx context.Context, bundleJSON, checksums []byte, tag string) error {
	trusted, err := loadTrustedRootFn(ctx)
	if err != nil {
		return err
	}
	return verifyChecksums(trusted, bundleJSON, checksums, tag)
}

// loadTrustedRootFn loads the Sigstore trusted root. Override in tests to
// supply a fixed root without the network.
var loadTrustedRootFn = fetchTrustedRoot

// tufOptionsFn returns the TUF client options. Override in tests to point
// the client at a local mirror.
var tufOptionsFn = tuf.DefaultOptions

// tufHTTPTimeout bounds each TUF request. Tests may lower it.
var tufHTTPTimeout = 60 * time.Second

// contextHTTPClient binds every request to ctx, because the TUF fetcher
// builds its requests without one.
type contextHTTPClient struct {
	ctx    context.Context
	client *http.Client
}

func (c contextHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return c.client.Do(req.WithContext(c.ctx))
}

// trustedRootOptions returns the TUF options with an HTTP client that times
// out and follows ctx. go-tuf's default client has no timeout.
func trustedRootOptions(ctx context.Context) *tuf.Options {
	opts := tufOptionsFn()
	f, ok := opts.Fetcher.(*fetcher.DefaultFetcher)
	if !ok {
		f = fetcher.NewDefaultFetcher()
	}
	f.SetHTTPClient(contextHTTPClient{ctx: ctx, client: &http.Client{Timeout: tufHTTPTimeout}})
	return opts.WithFetcher(f)
}

// fetchTrustedRoot loads the Sigstore public-good trusted root through TUF,
// cached under ~/.sigstore. If the cache is unusable, for example in a
// read-only home directory, it retries without the cache. Other failures,
// such as a timeout, are not retried, because the retry would fail the
// same way.
func fetchTrustedRoot(ctx context.Context) (root.TrustedMaterial, error) {
	tr, err := root.FetchTrustedRootWithOptions(trustedRootOptions(ctx))
	if err == nil {
		return tr, nil
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("fetching Sigstore trusted root: %w", ctx.Err())
	}
	var pathErr *fs.PathError
	if !errors.As(err, &pathErr) {
		return nil, fmt.Errorf("fetching Sigstore trusted root: %w", err)
	}
	tr, retryErr := root.FetchTrustedRootWithOptions(trustedRootOptions(ctx).WithDisableLocalCache())
	if retryErr != nil {
		return nil, fmt.Errorf("fetching Sigstore trusted root: %w", errors.Join(err, retryErr))
	}
	return tr, nil
}
