package gcf

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewFakeGCFClient_OptionsAndMethods(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	info := &FunctionInfo{URI: "https://mint.example.com", EnvVars: map[string]string{"K": "V"}}
	afterCreate := &FunctionInfo{URI: "https://mint.example.com", EnvVars: map[string]string{"K": "after"}}
	traffic := map[string]string{"TRAFFIC": "yes"}
	rev := &ServiceRevisionInfo{TrafficRevisionShort: "rev-1"}
	secrets := map[string]bool{"fullsend-coder-app-pem": true}
	wif := &WIFProviderInfo{AttributeCondition: "assertion.repository_owner in ['acme']"}

	client := NewFakeGCFClient(
		WithFakeFunctionInfo(info),
		WithFakeTrafficEnvVars(traffic),
		WithFakeRevisionInfo(rev),
		WithFakeSecrets(secrets),
		WithFakeWIFProvider(wif),
		WithFakeErrors(map[string]error{
			"DisableSecretVersion": errors.New("disable failed"),
		}),
	)
	fake, ok := client.(*fakeGCFClient)
	require.True(t, ok)
	fake.functionInfoAfterCreate = afterCreate
	fake.secretData = map[string][]byte{"fullsend-coder-app-pem": []byte("pem-bytes")}

	require.NoError(t, client.CreateServiceAccount(ctx, "p", "a", "d"))
	require.NoError(t, client.CreateWIFPool(ctx, "p", "pool", "d"))
	require.NoError(t, client.CreateWIFProvider(ctx, "p", "pool", "prov", OIDCProviderConfig{AttributeCondition: "c"}))
	gotWIF, err := client.GetWIFProvider(ctx, "p", "pool", "prov")
	require.NoError(t, err)
	assert.Equal(t, wif, gotWIF)
	require.NoError(t, client.UpdateWIFProvider(ctx, "p", "pool", "prov", OIDCProviderConfig{AttributeCondition: "updated"}))

	require.NoError(t, client.GetSecret(ctx, "p", "fullsend-coder-app-pem"))
	require.NoError(t, client.CreateSecret(ctx, "p", "new-secret"))
	data, err := client.AccessSecretVersion(ctx, "p", "fullsend-coder-app-pem")
	require.NoError(t, err)
	assert.Equal(t, []byte("pem-bytes"), data)
	require.NoError(t, client.AddSecretVersion(ctx, "p", "fullsend-coder-app-pem", []byte("v2")))
	err = client.DisableSecretVersion(ctx, "p", "fullsend-coder-app-pem")
	require.Error(t, err)
	require.NoError(t, client.EnableSecretVersion(ctx, "p", "fullsend-coder-app-pem"))
	require.NoError(t, client.DeleteSecret(ctx, "p", "new-secret"))

	require.NoError(t, client.DisableWIFProvider(ctx, "p", "pool", "prov"))
	require.NoError(t, client.DeleteWIFProvider(ctx, "p", "pool", "prov"))
	require.NoError(t, client.SetSecretIAMBinding(ctx, "p", "s", "m"))
	require.NoError(t, client.SetProjectIAMBinding(ctx, "p", "m", "r"))
	require.NoError(t, client.SetCloudRunInvoker(ctx, "p", "s", "m"))

	first, err := client.GetFunction(ctx, "p", "r", "fn")
	require.NoError(t, err)
	assert.Equal(t, info, first)
	second, err := client.GetFunction(ctx, "p", "r", "fn")
	require.NoError(t, err)
	assert.Equal(t, afterCreate, second)

	_, err = client.UploadFunctionSource(ctx, "p", "fn", []byte("zip"))
	require.NoError(t, err)
	_, err = client.CreateFunction(ctx, "p", "r", "fn", FunctionConfig{EnvVars: map[string]string{"A": "1"}})
	require.NoError(t, err)
	_, err = client.UpdateFunction(ctx, "p", "r", "fn", FunctionConfig{EnvVars: map[string]string{"B": "2"}})
	require.NoError(t, err)
	_, err = client.UpdateFunctionEnvVars(ctx, "p", "r", "fn", map[string]string{"C": "3"})
	require.NoError(t, err)
	_, err = client.UpdateServiceEnvVars(ctx, "p", "r", "fn", map[string]string{"D": "4"})
	require.NoError(t, err)

	gotTraffic, err := client.GetServiceTrafficEnvVars(ctx, "p", "r", "fn")
	require.NoError(t, err)
	assert.Equal(t, traffic, gotTraffic)

	gotRev, err := client.GetServiceRevisionInfo(ctx, "p", "r", "fn")
	require.NoError(t, err)
	assert.Equal(t, rev, gotRev)

	require.NoError(t, client.WaitForOperation(ctx, "op"))
	num, err := client.GetProjectNumber(ctx, "p")
	require.NoError(t, err)
	assert.Equal(t, "123456789", num)
}

func TestNewFakeGCFClient_TrafficEnvVarsFallback(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	info := &FunctionInfo{EnvVars: map[string]string{"FROM": "function"}}
	client := NewFakeGCFClient(WithFakeFunctionInfo(info))
	fake := client.(*fakeGCFClient)

	got, err := client.GetServiceTrafficEnvVars(ctx, "p", "r", "fn")
	require.NoError(t, err)
	assert.Equal(t, info.EnvVars, got)

	fake.trafficEnvVars = nil
	fake.getFunctionCalls = 2
	fake.functionInfoAfterCreate = &FunctionInfo{EnvVars: map[string]string{"FROM": "after-create"}}
	got, err = client.GetServiceTrafficEnvVars(ctx, "p", "r", "fn")
	require.NoError(t, err)
	assert.Equal(t, fake.functionInfoAfterCreate.EnvVars, got)
}

func TestNewFakeGCFClient_AccessSecretVersionNotFound(t *testing.T) {
	t.Parallel()
	client := NewFakeGCFClient(WithFakeSecrets(map[string]bool{"missing": true}))
	_, err := client.AccessSecretVersion(context.Background(), "p", "missing")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSecretNotFound)
}
