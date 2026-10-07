package gcf

import (
	"context"
	"encoding/json"
	"fmt"
)

// Compile-time check that fakeGCFClient implements GCFClient.
var _ GCFClient = (*fakeGCFClient)(nil)

// fakeGCFClient records calls and returns preset responses.
type fakeGCFClient struct {
	calls []string
	errs  map[string]error

	// Return values
	projectNumber string
	functionInfo  *FunctionInfo
	functionURL   string

	// Track GetFunction call count to return different results.
	getFunctionCalls int
	// functionInfoAfterCreate is returned on the second GetFunction call
	// (after CreateFunction). If nil, functionInfo is always returned.
	functionInfoAfterCreate *FunctionInfo

	// Captured WIF provider config and ID for assertion.
	lastWIFProviderConfig OIDCProviderConfig
	lastWIFProviderID     string

	// WIF provider state for GetWIFProvider.
	wifProvider *WIFProviderInfo

	// Track secret names written via AddSecretVersion.
	secretVersionNames []string
	deletedSecretIDs   []string

	// Per-secret state for CopyAgentPEM tests.
	secretData map[string][]byte // secretID → payload
	secrets    map[string]bool   // secretID → exists

	// Captured env vars from the last CreateFunction or UpdateFunction call.
	lastCreateFunctionEnvVars map[string]string

	// Captured env vars from the last UpdateServiceEnvVars call.
	lastUpdateServiceEnvVars map[string]string

	// updateServiceEnvVarsHistory records a copy of the env vars passed to
	// every UpdateServiceEnvVars call, in order.
	updateServiceEnvVarsHistory []map[string]string

	// applyEnvUpdatesToTraffic, when true, makes a successful
	// UpdateServiceEnvVars call (or a successful PinServiceTraffic, which
	// serves revisionInfo.TemplateEnvVars) replace the env returned by later
	// GetServiceTrafficEnvVars calls (the new revision is pinned and serving),
	// so tests can exercise multi-step flows statefully.
	applyEnvUpdatesToTraffic bool

	// updateServiceRevision is returned alongside the error from
	// UpdateServiceEnvVars. Non-empty simulates a partial failure where
	// the template PATCH succeeded (creating a revision) but the traffic
	// PATCH failed.
	updateServiceRevision string

	// trafficEnvVars is returned by GetServiceTrafficEnvVars.
	// If nil, falls back to functionInfo.EnvVars.
	trafficEnvVars map[string]string

	// Track revision info for GetServiceRevisionInfo.
	revisionInfo *ServiceRevisionInfo

	// revisionInfoSequence, when non-empty, is consumed FIFO by successive
	// GetServiceRevisionInfo calls (one entry per call) to simulate a
	// revision transitioning across polls (e.g. not-ready then ready).
	// Once exhausted, GetServiceRevisionInfo falls back to revisionInfo.
	revisionInfoSequence []*ServiceRevisionInfo

	// lastPinnedRevision is the short revision name passed to PinServiceTraffic.
	lastPinnedRevision string

	// updateEtags records the etag passed to every
	// UpdateServiceEnvVarsIfMatch call, in order.
	updateEtags []string

	// lastPinEtag and pinEtags record the etag passed to
	// PinServiceTrafficIfMatch (latest, and every call in order).
	lastPinEtag string
	pinEtags    []string

	// Captured project IAM binding arguments.
	projectIAMBindings []projectIAMBinding
}

type projectIAMBinding struct {
	ProjectID string
	Member    string
	Role      string
}

func newFakeGCFClient() *fakeGCFClient {
	return &fakeGCFClient{
		errs:          make(map[string]error),
		projectNumber: "123456789",
	}
}

func (f *fakeGCFClient) record(method string) error {
	f.calls = append(f.calls, method)
	return f.errs[method]
}

func (f *fakeGCFClient) CreateServiceAccount(_ context.Context, _, _, _ string) error {
	return f.record("CreateServiceAccount")
}
func (f *fakeGCFClient) DeleteServiceAccount(_ context.Context, _, _ string) error {
	return f.record("DeleteServiceAccount")
}
func (f *fakeGCFClient) CreateWIFPool(_ context.Context, _, _, _ string) error {
	return f.record("CreateWIFPool")
}
func (f *fakeGCFClient) DeleteWIFPool(_ context.Context, _, _ string) error {
	return f.record("DeleteWIFPool")
}
func (f *fakeGCFClient) CreateWIFProvider(_ context.Context, _, _, providerID string, cfg OIDCProviderConfig) error {
	f.lastWIFProviderConfig = cfg
	f.lastWIFProviderID = providerID
	return f.record("CreateWIFProvider")
}
func (f *fakeGCFClient) GetWIFProvider(_ context.Context, _, _, _ string) (*WIFProviderInfo, error) {
	f.calls = append(f.calls, "GetWIFProvider")
	if err := f.errs["GetWIFProvider"]; err != nil {
		return nil, err
	}
	return f.wifProvider, nil
}
func (f *fakeGCFClient) UpdateWIFProvider(_ context.Context, _, _, _ string, cfg OIDCProviderConfig) error {
	f.lastWIFProviderConfig = cfg
	return f.record("UpdateWIFProvider")
}
func (f *fakeGCFClient) GetSecret(_ context.Context, _ string, sid string) error {
	f.calls = append(f.calls, "GetSecret")
	if err := f.errs["GetSecret"]; err != nil {
		return err
	}
	if f.secrets != nil {
		if !f.secrets[sid] {
			return ErrSecretNotFound
		}
	}
	return nil
}
func (f *fakeGCFClient) CreateSecret(_ context.Context, _ string, sid string) error {
	if f.secrets != nil {
		f.secrets[sid] = true
	}
	return f.record("CreateSecret")
}
func (f *fakeGCFClient) AddSecretVersion(_ context.Context, _ string, secretID string, data []byte) error {
	f.secretVersionNames = append(f.secretVersionNames, secretID)
	if f.secretData != nil {
		f.secretData[secretID] = append([]byte(nil), data...)
	}
	return f.record("AddSecretVersion")
}
func (f *fakeGCFClient) AccessSecretVersion(_ context.Context, _ string, sid string) ([]byte, error) {
	f.calls = append(f.calls, "AccessSecretVersion")
	if err := f.errs["AccessSecretVersion"]; err != nil {
		return nil, err
	}
	if f.secretData != nil {
		if data, ok := f.secretData[sid]; ok {
			return data, nil
		}
	}
	return nil, fmt.Errorf("secret %s: %w", sid, ErrSecretNotFound)
}
func (f *fakeGCFClient) DisableSecretVersion(_ context.Context, _ string, sid string) error {
	f.calls = append(f.calls, "DisableSecretVersion")
	return f.errs["DisableSecretVersion"]
}
func (f *fakeGCFClient) EnableSecretVersion(_ context.Context, _ string, sid string) error {
	f.calls = append(f.calls, "EnableSecretVersion")
	return f.errs["EnableSecretVersion"]
}
func (f *fakeGCFClient) DeleteSecret(_ context.Context, _ string, sid string) error {
	f.calls = append(f.calls, "DeleteSecret")
	f.deletedSecretIDs = append(f.deletedSecretIDs, sid)
	if f.secrets != nil {
		delete(f.secrets, sid)
	}
	return f.errs["DeleteSecret"]
}
func (f *fakeGCFClient) DisableWIFProvider(_ context.Context, _, _, _ string) error {
	return f.record("DisableWIFProvider")
}
func (f *fakeGCFClient) DeleteWIFProvider(_ context.Context, _, _, _ string) error {
	return f.record("DeleteWIFProvider")
}
func (f *fakeGCFClient) SetSecretIAMBinding(_ context.Context, _, _, _ string) error {
	return f.record("SetSecretIAMBinding")
}
func (f *fakeGCFClient) ReplaceSecretIAMBinding(_ context.Context, _, _, _ string) error {
	return f.record("ReplaceSecretIAMBinding")
}
func (f *fakeGCFClient) SetProjectIAMBinding(_ context.Context, projectID, member, role string) error {
	f.projectIAMBindings = append(f.projectIAMBindings, projectIAMBinding{projectID, member, role})
	return f.record("SetProjectIAMBinding")
}
func (f *fakeGCFClient) SetCloudRunInvoker(_ context.Context, _, _, _ string) error {
	return f.record("SetCloudRunInvoker")
}
func (f *fakeGCFClient) DeleteFunction(_ context.Context, _, _, _ string) error {
	return f.record("DeleteFunction")
}
func (f *fakeGCFClient) GetFunction(_ context.Context, _, _, _ string) (*FunctionInfo, error) {
	f.calls = append(f.calls, "GetFunction")
	f.getFunctionCalls++
	if err := f.errs["GetFunction"]; err != nil {
		return nil, err
	}
	// On the second call (after CreateFunction), return the post-deploy info.
	if f.getFunctionCalls > 1 && f.functionInfoAfterCreate != nil {
		return f.functionInfoAfterCreate, nil
	}
	return f.functionInfo, nil
}
func (f *fakeGCFClient) GetCloudRunServiceURI(_ context.Context, _, _, _ string) (string, error) {
	f.calls = append(f.calls, "GetCloudRunServiceURI")
	if err := f.errs["GetCloudRunServiceURI"]; err != nil {
		return "", err
	}
	if f.functionInfo != nil && f.functionInfo.URI != "" {
		return f.functionInfo.URI, nil
	}
	return f.functionURL, nil
}
func (f *fakeGCFClient) UploadFunctionSource(_ context.Context, _, _ string, _ []byte) (json.RawMessage, error) {
	f.calls = append(f.calls, "UploadFunctionSource")
	if err := f.errs["UploadFunctionSource"]; err != nil {
		return nil, err
	}
	return json.RawMessage(`{"bucket":"test-bucket","object":"source.zip"}`), nil
}
func (f *fakeGCFClient) CreateFunction(_ context.Context, _, _, _ string, cfg FunctionConfig) (string, error) {
	f.calls = append(f.calls, "CreateFunction")
	f.lastCreateFunctionEnvVars = cfg.EnvVars
	if err := f.errs["CreateFunction"]; err != nil {
		return "", err
	}
	return "operations/123", nil
}
func (f *fakeGCFClient) UpdateFunction(_ context.Context, _, _, _ string, cfg FunctionConfig) (string, error) {
	f.calls = append(f.calls, "UpdateFunction")
	f.lastCreateFunctionEnvVars = cfg.EnvVars
	if err := f.errs["UpdateFunction"]; err != nil {
		return "", err
	}
	return "operations/update-456", nil
}
func (f *fakeGCFClient) UpdateFunctionEnvVars(_ context.Context, _, _, _ string, envVars map[string]string) (string, error) {
	f.calls = append(f.calls, "UpdateFunctionEnvVars")
	if err := f.errs["UpdateFunctionEnvVars"]; err != nil {
		return "", err
	}
	return "operations/envvar-update-789", nil
}
func (f *fakeGCFClient) UpdateServiceEnvVars(_ context.Context, _, _, _ string, envVars map[string]string) (string, error) {
	f.calls = append(f.calls, "UpdateServiceEnvVars")
	f.lastUpdateServiceEnvVars = envVars
	snapshot := make(map[string]string, len(envVars))
	for k, v := range envVars {
		snapshot[k] = v
	}
	f.updateServiceEnvVarsHistory = append(f.updateServiceEnvVarsHistory, snapshot)
	err := f.errs["UpdateServiceEnvVars"]
	if err == nil && f.applyEnvUpdatesToTraffic {
		// UpdateServiceEnvVars creates a revision from these env vars and
		// pins traffic to it, so later traffic-serving reads see them.
		f.trafficEnvVars = snapshot
	}
	return f.updateServiceRevision, err
}

// UpdateServiceEnvVarsIfMatch records the etag precondition and then behaves
// like UpdateServiceEnvVars (same call name and error key).
// errs["UpdateServiceEnvVarsIfMatch"] simulates a precondition (etag) failure
// before any revision is created. Like the pin variant it accepts an empty
// etag; tests assert on updateEtags instead.
func (f *fakeGCFClient) UpdateServiceEnvVarsIfMatch(ctx context.Context, p, r, s string, envVars map[string]string, etag string) (string, error) {
	f.updateEtags = append(f.updateEtags, etag)
	if err := f.errs["UpdateServiceEnvVarsIfMatch"]; err != nil {
		f.calls = append(f.calls, "UpdateServiceEnvVars")
		return "", err
	}
	return f.UpdateServiceEnvVars(ctx, p, r, s, envVars)
}
func (f *fakeGCFClient) PinServiceTraffic(_ context.Context, _, _, _, revision string) error {
	f.calls = append(f.calls, "PinServiceTraffic")
	f.lastPinnedRevision = revision
	err := f.errs["PinServiceTraffic"]
	if err == nil && f.applyEnvUpdatesToTraffic && f.revisionInfo != nil && f.revisionInfo.TemplateEnvVars != nil {
		// The pinned revision was built from the service template, so it now
		// serves the template's env vars.
		serving := make(map[string]string, len(f.revisionInfo.TemplateEnvVars))
		for k, v := range f.revisionInfo.TemplateEnvVars {
			serving[k] = v
		}
		f.trafficEnvVars = serving
	}
	return err
}

// PinServiceTrafficIfMatch records the etag precondition and then behaves like
// PinServiceTraffic (same call name and error key), so tests that assert on
// "PinServiceTraffic" cover both. errs["PinServiceTrafficIfMatch"] simulates a
// precondition (etag) failure. Unlike the live client it accepts an empty
// etag (fixtures that predate Etag); tests assert on lastPinEtag instead.
func (f *fakeGCFClient) PinServiceTrafficIfMatch(ctx context.Context, p, r, s, revision, etag string) error {
	f.lastPinEtag = etag
	f.pinEtags = append(f.pinEtags, etag)
	if err := f.errs["PinServiceTrafficIfMatch"]; err != nil {
		f.calls = append(f.calls, "PinServiceTraffic")
		return err
	}
	return f.PinServiceTraffic(ctx, p, r, s, revision)
}
func (f *fakeGCFClient) GetServiceTrafficEnvVars(_ context.Context, _, _, _ string) (map[string]string, error) {
	f.calls = append(f.calls, "GetServiceTrafficEnvVars")
	if err := f.errs["GetServiceTrafficEnvVars"]; err != nil {
		return nil, err
	}
	if f.trafficEnvVars != nil {
		return f.trafficEnvVars, nil
	}
	// Fall back to function info env vars for backward compatibility with
	// existing tests that don't set trafficEnvVars explicitly. Mirrors
	// GetFunction's logic: use functionInfoAfterCreate when available
	// (post-deploy), otherwise use functionInfo.
	if f.getFunctionCalls > 1 && f.functionInfoAfterCreate != nil {
		return f.functionInfoAfterCreate.EnvVars, nil
	}
	if f.functionInfo != nil {
		return f.functionInfo.EnvVars, nil
	}
	return nil, nil
}

// GetServiceServingEnvVars mirrors the live strict read: it returns the same
// data as GetServiceTrafficEnvVars, but errors when the configured revision
// info reports no resolvable traffic-serving revision (the live client would
// otherwise have fallen back to the template).
func (f *fakeGCFClient) GetServiceServingEnvVars(ctx context.Context, p, r, s string) (map[string]string, error) {
	f.calls = append(f.calls, "GetServiceServingEnvVars")
	if err := f.errs["GetServiceServingEnvVars"]; err != nil {
		return nil, err
	}
	if f.revisionInfo != nil && f.revisionInfo.TrafficRevisionShort == "" {
		return nil, fmt.Errorf("no traffic-serving revision could be resolved")
	}
	calls := f.calls
	envVars, err := f.GetServiceTrafficEnvVars(ctx, p, r, s)
	f.calls = calls
	return envVars, err
}
func (f *fakeGCFClient) GetServiceRevisionInfo(_ context.Context, _, _, _ string) (*ServiceRevisionInfo, error) {
	f.calls = append(f.calls, "GetServiceRevisionInfo")
	if err := f.errs["GetServiceRevisionInfo"]; err != nil {
		return nil, err
	}
	if len(f.revisionInfoSequence) > 0 {
		next := f.revisionInfoSequence[0]
		f.revisionInfoSequence = f.revisionInfoSequence[1:]
		return next, nil
	}
	if f.revisionInfo != nil {
		return f.revisionInfo, nil
	}
	// By default the serving revision carries the function's env (serving
	// matches Cloud Functions metadata), plus a project-number marker so the
	// read is never an unverified empty one.
	trafficEnv := map[string]string{"GCP_PROJECT_NUMBER": f.projectNumber}
	if f.functionInfo != nil {
		for k, v := range f.functionInfo.EnvVars {
			trafficEnv[k] = v
		}
	}
	return &ServiceRevisionInfo{
		TrafficRevisionShort:   "fullsend-mint-00001-abc",
		TrafficAllocType:       "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST",
		TemplateMatchesTraffic: true,
		TrafficEnvVars:         trafficEnv,
	}, nil
}
func (f *fakeGCFClient) WaitForOperation(_ context.Context, _ string) error {
	return f.record("WaitForOperation")
}
func (f *fakeGCFClient) GetProjectNumber(_ context.Context, _ string) (string, error) {
	f.calls = append(f.calls, "GetProjectNumber")
	if err := f.errs["GetProjectNumber"]; err != nil {
		return "", err
	}
	return f.projectNumber, nil
}

// FakeGCFOption configures a client from NewFakeGCFClient.
type FakeGCFOption func(*fakeGCFClient)

// NewFakeGCFClient returns an in-memory GCFClient for tests.
func NewFakeGCFClient(opts ...FakeGCFOption) GCFClient {
	f := newFakeGCFClient()
	for _, opt := range opts {
		opt(f)
	}
	return f
}

func WithFakeFunctionInfo(info *FunctionInfo) FakeGCFOption {
	return func(f *fakeGCFClient) { f.functionInfo = info }
}

func WithFakeTrafficEnvVars(env map[string]string) FakeGCFOption {
	return func(f *fakeGCFClient) { f.trafficEnvVars = env }
}

func WithFakeRevisionInfo(info *ServiceRevisionInfo) FakeGCFOption {
	return func(f *fakeGCFClient) { f.revisionInfo = info }
}

func WithFakeSecrets(secrets map[string]bool) FakeGCFOption {
	return func(f *fakeGCFClient) { f.secrets = secrets }
}

func WithFakeErrors(errs map[string]error) FakeGCFOption {
	return func(f *fakeGCFClient) { f.errs = errs }
}

func WithFakeWIFProvider(p *WIFProviderInfo) FakeGCFOption {
	return func(f *fakeGCFClient) { f.wifProvider = p }
}

// LastWIFProviderCondition returns the AttributeCondition passed to the most
// recent CreateWIFProvider or UpdateWIFProvider call on a fake client, for
// cross-package test assertions. Returns "" if client isn't a fake or no
// call was made yet.
func LastWIFProviderCondition(client GCFClient) string {
	f, ok := client.(*fakeGCFClient)
	if !ok {
		return ""
	}
	return f.lastWIFProviderConfig.AttributeCondition
}

// ProjectIAMBindingCount returns the number of SetProjectIAMBinding calls
// recorded on a fake client, for cross-package test assertions. Returns 0 if
// client isn't a fake.
func ProjectIAMBindingCount(client GCFClient) int {
	f, ok := client.(*fakeGCFClient)
	if !ok {
		return 0
	}
	return len(f.projectIAMBindings)
}

// DeletedSecretIDs returns the secret IDs passed to DeleteSecret calls on a
// fake client, for cross-package test assertions. Returns nil if client isn't
// a fake.
func DeletedSecretIDs(client GCFClient) []string {
	f, ok := client.(*fakeGCFClient)
	if !ok {
		return nil
	}
	return f.deletedSecretIDs
}

// RecordedCalls returns the method names recorded on a fake client, for
// cross-package test assertions. Returns nil if client isn't a fake.
func RecordedCalls(client GCFClient) []string {
	f, ok := client.(*fakeGCFClient)
	if !ok {
		return nil
	}
	return f.calls
}
