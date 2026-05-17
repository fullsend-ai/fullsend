package gcf

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/gcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient creates a LiveGCFClient whose requests are routed to the
// given httptest.Server via a URL-rewriting transport.
func newTestClient(srv *httptest.Server) *LiveGCFClient {
	target, _ := url.Parse(srv.URL)
	transport := &rewriteTransport{base: target}
	httpClient := &http.Client{Transport: transport}
	return &LiveGCFClient{Client: gcp.NewClientWithHTTP(httpClient), skipUploadURLCheck: true}
}

// rewriteTransport rewrites all request URLs to point at a test server,
// preserving the original path and query string.
type rewriteTransport struct {
	base *url.URL
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = t.base.Scheme
	req.URL.Host = t.base.Host
	return http.DefaultTransport.RoundTrip(req)
}

// --- GetServiceAccount ---

// --- CreateServiceAccount ---

func TestLiveGCFClient_CreateServiceAccount(t *testing.T) {
	t.Run("created", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			var body struct {
				AccountID      string `json:"accountId"`
				ServiceAccount struct {
					DisplayName string `json:"displayName"`
				} `json:"serviceAccount"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			assert.Equal(t, "my-sa", body.AccountID)
			assert.Equal(t, "My SA", body.ServiceAccount.DisplayName)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		err := newTestClient(srv).CreateServiceAccount(context.Background(), "proj", "my-sa", "My SA")
		require.NoError(t, err)
	})

	t.Run("already exists", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
		}))
		defer srv.Close()

		err := newTestClient(srv).CreateServiceAccount(context.Background(), "proj", "sa", "SA")
		require.NoError(t, err)
	})

	t.Run("error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintln(w, `{"error":{"message":"permission denied"}}`)
		}))
		defer srv.Close()

		err := newTestClient(srv).CreateServiceAccount(context.Background(), "proj", "sa", "SA")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status 403")
	})
}

// --- CreateWIFPool ---

func TestLiveGCFClient_CreateWIFPool(t *testing.T) {
	t.Run("created", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Contains(t, r.URL.RawQuery, "workloadIdentityPoolId=pool-1")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, `{"name":"operations/pool-op","done":true}`)
		}))
		defer srv.Close()

		err := newTestClient(srv).CreateWIFPool(context.Background(), "123", "pool-1", "Pool 1")
		require.NoError(t, err)
	})

	t.Run("already exists", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
		}))
		defer srv.Close()

		err := newTestClient(srv).CreateWIFPool(context.Background(), "123", "pool", "Pool")
		require.NoError(t, err)
	})

	t.Run("error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintln(w, `{"error":{"message":"bad request"}}`)
		}))
		defer srv.Close()

		err := newTestClient(srv).CreateWIFPool(context.Background(), "123", "pool", "Pool")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status 400")
	})
}

// --- CreateWIFProvider ---

func TestLiveGCFClient_CreateWIFProvider(t *testing.T) {
	t.Run("created", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Contains(t, r.URL.RawQuery, "workloadIdentityPoolProviderId=gh-oidc")
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			oidc := body["oidc"].(map[string]interface{})
			assert.Equal(t, "https://token.actions.githubusercontent.com", oidc["issuerUri"])
			audiences, ok := oidc["allowedAudiences"].([]interface{})
			assert.True(t, ok, "allowedAudiences should be present")
			assert.Equal(t, []interface{}{"fullsend-mint"}, audiences)
			assert.NotEmpty(t, body["attributeCondition"])
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, `{"name":"operations/prov-op","done":true}`)
		}))
		defer srv.Close()

		err := newTestClient(srv).CreateWIFProvider(context.Background(), "123", "pool", "gh-oidc", OIDCProviderConfig{
			IssuerURI:          "https://token.actions.githubusercontent.com",
			AttributeCondition: "assertion.repository_owner == 'my-org'",
			AllowedAudiences:   []string{"fullsend-mint"},
		})
		require.NoError(t, err)
	})

	t.Run("already exists updates condition and audiences", func(t *testing.T) {
		callCount := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			switch callCount {
			case 1:
				assert.Equal(t, http.MethodPost, r.Method)
				w.WriteHeader(http.StatusConflict)
			case 2:
				// Undelete attempt — returns 400 (not actually deleted).
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Contains(t, r.URL.Path, ":undelete")
				w.WriteHeader(http.StatusBadRequest)
			case 3:
				assert.Equal(t, http.MethodPatch, r.Method)
				assert.Contains(t, r.URL.RawQuery, "attributeCondition")
				assert.Contains(t, r.URL.RawQuery, "oidc.allowedAudiences")
				var body map[string]interface{}
				json.NewDecoder(r.Body).Decode(&body)
				oidc, ok := body["oidc"].(map[string]interface{})
				assert.True(t, ok, "oidc config should be in PATCH body")
				audiences := oidc["allowedAudiences"].([]interface{})
				assert.Equal(t, []interface{}{"fullsend-mint", "https://iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/pool/providers/gh-oidc"}, audiences)
				w.WriteHeader(http.StatusOK)
				fmt.Fprintln(w, `{}`)
			}
		}))
		defer srv.Close()

		err := newTestClient(srv).CreateWIFProvider(context.Background(), "123", "pool", "gh-oidc", OIDCProviderConfig{
			AttributeCondition: "assertion.repository_owner == 'my-org'",
			AllowedAudiences:   []string{"fullsend-mint", "https://iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/pool/providers/gh-oidc"},
		})
		require.NoError(t, err)
		assert.Equal(t, 3, callCount)
	})
}

// --- GetWIFProvider ---

func TestLiveGCFClient_GetWIFProvider(t *testing.T) {
	t.Run("returns condition and audiences", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, `{"attributeCondition":"assertion.repository_owner == 'acme'","oidc":{"allowedAudiences":["fullsend-mint","https://iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/pool/providers/prov"]}}`)
		}))
		defer srv.Close()

		info, err := newTestClient(srv).GetWIFProvider(context.Background(), "123", "pool", "prov")
		require.NoError(t, err)
		require.NotNil(t, info)
		assert.Equal(t, "assertion.repository_owner == 'acme'", info.AttributeCondition)
		assert.Equal(t, []string{"fullsend-mint", "https://iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/pool/providers/prov"}, info.AllowedAudiences)
	})

	t.Run("not found returns nil", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		info, err := newTestClient(srv).GetWIFProvider(context.Background(), "123", "pool", "prov")
		require.NoError(t, err)
		assert.Nil(t, info)
	})
}

// --- GetSecret ---

func TestLiveGCFClient_GetSecret(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Contains(t, r.URL.Path, "secrets/my-secret")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, `{}`)
		}))
		defer srv.Close()

		err := newTestClient(srv).GetSecret(context.Background(), "proj", "my-secret")
		require.NoError(t, err)
	})

	t.Run("not found", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		err := newTestClient(srv).GetSecret(context.Background(), "proj", "missing")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrSecretNotFound)
	})
}

// --- CreateSecret ---

func TestLiveGCFClient_CreateSecret(t *testing.T) {
	t.Run("created", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Contains(t, r.URL.RawQuery, "secretId=app-pem")
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			replication := body["replication"].(map[string]interface{})
			assert.NotNil(t, replication["automatic"])
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		err := newTestClient(srv).CreateSecret(context.Background(), "proj", "app-pem")
		require.NoError(t, err)
	})

	t.Run("already exists", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
		}))
		defer srv.Close()

		err := newTestClient(srv).CreateSecret(context.Background(), "proj", "app-pem")
		require.NoError(t, err)
	})
}

// --- AddSecretVersion ---

func TestLiveGCFClient_AddSecretVersion(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Contains(t, r.URL.Path, ":addVersion")
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			payload := body["payload"].(map[string]interface{})
			assert.NotEmpty(t, payload["data"])
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		err := newTestClient(srv).AddSecretVersion(context.Background(), "proj", "secret", []byte("pem-data"))
		require.NoError(t, err)
	})

	t.Run("error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintln(w, `{"error":{"message":"denied"}}`)
		}))
		defer srv.Close()

		err := newTestClient(srv).AddSecretVersion(context.Background(), "proj", "secret", []byte("data"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status 403")
	})
}

// --- SetSecretIAMBinding ---

func TestLiveGCFClient_SetSecretIAMBinding(t *testing.T) {
	t.Run("adds new binding", func(t *testing.T) {
		callCount := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			if callCount == 1 {
				// getIamPolicy
				assert.Contains(t, r.URL.Path, ":getIamPolicy")
				w.WriteHeader(http.StatusOK)
				fmt.Fprintln(w, `{"bindings":[],"etag":"abc"}`)
				return
			}
			// setIamPolicy
			assert.Contains(t, r.URL.Path, ":setIamPolicy")
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			policy := body["policy"].(map[string]interface{})
			assert.Equal(t, "abc", policy["etag"])
			bindings := policy["bindings"].([]interface{})
			assert.Len(t, bindings, 1)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		err := newTestClient(srv).SetSecretIAMBinding(context.Background(),
			"projects/proj/secrets/s", "serviceAccount:sa@proj.iam.gserviceaccount.com", "roles/secretmanager.secretAccessor")
		require.NoError(t, err)
	})

	t.Run("member already bound", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// getIamPolicy — member already present
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, `{"bindings":[{"role":"roles/secretmanager.secretAccessor","members":["serviceAccount:sa@proj.iam.gserviceaccount.com"]}]}`)
		}))
		defer srv.Close()

		err := newTestClient(srv).SetSecretIAMBinding(context.Background(),
			"projects/proj/secrets/s", "serviceAccount:sa@proj.iam.gserviceaccount.com", "roles/secretmanager.secretAccessor")
		require.NoError(t, err)
	})

	t.Run("getIamPolicy error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintln(w, `{"error":{"message":"denied"}}`)
		}))
		defer srv.Close()

		err := newTestClient(srv).SetSecretIAMBinding(context.Background(), "projects/proj/secrets/s", "m", "role")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "getting IAM policy returned 403")
	})

	t.Run("rejects invalid resource path", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			t.Fatal("should not reach server")
		}))
		defer srv.Close()

		err := newTestClient(srv).SetSecretIAMBinding(context.Background(), "../../evil", "m", "role")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid secret resource path")
	})
}

// --- SetProjectIAMBinding ---

func TestLiveGCFClient_SetProjectIAMBinding(t *testing.T) {
	t.Run("adds new binding", func(t *testing.T) {
		callCount := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			assert.Equal(t, http.MethodPost, r.Method)
			if callCount == 1 {
				assert.Contains(t, r.URL.Path, ":getIamPolicy")
				w.WriteHeader(http.StatusOK)
				fmt.Fprintln(w, `{"bindings":[],"etag":"v1"}`)
				return
			}
			assert.Contains(t, r.URL.Path, ":setIamPolicy")
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			policy := body["policy"].(map[string]interface{})
			assert.Equal(t, "v1", policy["etag"])
			bindings := policy["bindings"].([]interface{})
			assert.Len(t, bindings, 1)
			b := bindings[0].(map[string]interface{})
			assert.Equal(t, "roles/aiplatform.user", b["role"])
			members := b["members"].([]interface{})
			assert.Contains(t, members, "principalSet://iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/pool/attribute.repository_owner/my-org")
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		err := newTestClient(srv).SetProjectIAMBinding(context.Background(),
			"my-project",
			"principalSet://iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/pool/attribute.repository_owner/my-org",
			"roles/aiplatform.user")
		require.NoError(t, err)
		assert.Equal(t, 2, callCount)
	})

	t.Run("member already bound", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, `{"bindings":[{"role":"roles/aiplatform.user","members":["principalSet://example"]}]}`)
		}))
		defer srv.Close()

		err := newTestClient(srv).SetProjectIAMBinding(context.Background(),
			"my-project", "principalSet://example", "roles/aiplatform.user")
		require.NoError(t, err)
	})

	t.Run("retries on 409 conflict", func(t *testing.T) {
		callCount := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			if callCount <= 2 {
				if callCount%2 == 1 {
					w.WriteHeader(http.StatusOK)
					fmt.Fprintln(w, `{"bindings":[],"etag":"v1"}`)
					return
				}
				w.WriteHeader(http.StatusConflict)
				fmt.Fprintln(w, `{"error":{"message":"conflict"}}`)
				return
			}
			if callCount == 3 {
				w.WriteHeader(http.StatusOK)
				fmt.Fprintln(w, `{"bindings":[],"etag":"v2"}`)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		err := newTestClient(srv).SetProjectIAMBinding(context.Background(),
			"proj", "member", "role")
		require.NoError(t, err)
		assert.Equal(t, 4, callCount)
	})

	t.Run("getIamPolicy error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintln(w, `{"error":{"message":"denied"}}`)
		}))
		defer srv.Close()

		err := newTestClient(srv).SetProjectIAMBinding(context.Background(), "proj", "m", "role")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "getting IAM policy returned 403")
	})
}

// --- SetCloudRunInvoker ---

func TestLiveGCFClient_SetCloudRunInvoker(t *testing.T) {
	t.Run("adds binding to empty policy", func(t *testing.T) {
		callCount := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callCount++
			if callCount == 1 {
				assert.Contains(t, r.URL.Path, ":getIamPolicy")
				w.WriteHeader(http.StatusOK)
				fmt.Fprintln(w, `{"bindings":[],"etag":"xyz"}`)
				return
			}
			assert.Contains(t, r.URL.Path, ":setIamPolicy")
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			policy := body["policy"].(map[string]interface{})
			assert.Equal(t, "xyz", policy["etag"])
			bindings := policy["bindings"].([]interface{})
			assert.Len(t, bindings, 1)
			b := bindings[0].(map[string]interface{})
			assert.Equal(t, "roles/run.invoker", b["role"])
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		err := newTestClient(srv).SetCloudRunInvoker(context.Background(), "proj", "us-central1", "my-func")
		require.NoError(t, err)
		assert.Equal(t, 2, callCount)
	})

	t.Run("already bound is no-op", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, `{"bindings":[{"role":"roles/run.invoker","members":["allUsers"]}]}`)
		}))
		defer srv.Close()

		err := newTestClient(srv).SetCloudRunInvoker(context.Background(), "proj", "us-central1", "func")
		require.NoError(t, err)
	})

	t.Run("preserves existing bindings", func(t *testing.T) {
		callCount := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			callCount++
			if callCount == 1 {
				w.WriteHeader(http.StatusOK)
				fmt.Fprintln(w, `{"bindings":[{"role":"roles/run.admin","members":["user:admin@example.com"]}]}`)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		err := newTestClient(srv).SetCloudRunInvoker(context.Background(), "proj", "us-central1", "func")
		require.NoError(t, err)
		assert.Equal(t, 2, callCount)
	})

	t.Run("getIamPolicy error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintln(w, `{"error":{"message":"denied"}}`)
		}))
		defer srv.Close()

		err := newTestClient(srv).SetCloudRunInvoker(context.Background(), "proj", "us-central1", "func")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "getting Cloud Run IAM policy returned 403")
	})
}

// --- GetFunction ---

func TestLiveGCFClient_GetFunction(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"name":  "projects/proj/locations/us-central1/functions/my-func",
				"state": "ACTIVE",
				"serviceConfig": map[string]string{
					"uri": "https://my-func-abc.run.app",
				},
			})
		}))
		defer srv.Close()

		info, err := newTestClient(srv).GetFunction(context.Background(), "proj", "us-central1", "my-func")
		require.NoError(t, err)
		require.NotNil(t, info)
		assert.Equal(t, "ACTIVE", info.State)
		assert.Equal(t, "https://my-func-abc.run.app", info.URI)
		assert.Equal(t, "us-central1", info.Region)
	})

	t.Run("not found returns nil", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		info, err := newTestClient(srv).GetFunction(context.Background(), "proj", "us-central1", "missing")
		require.NoError(t, err)
		assert.Nil(t, info)
	})

	t.Run("server error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintln(w, `{"error":{"message":"internal"}}`)
		}))
		defer srv.Close()

		_, err := newTestClient(srv).GetFunction(context.Background(), "proj", "us-central1", "func")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status 500")
	})
}

// --- UploadFunctionSource ---

func TestLiveGCFClient_UploadFunctionSource(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var srvURL string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"uploadUrl":     srvURL + "/upload",
					"storageSource": map[string]string{"bucket": "b", "object": "o"},
				})
				return
			}
			assert.Equal(t, http.MethodPut, r.Method)
			assert.Equal(t, "/upload", r.URL.Path)
			assert.Equal(t, "application/zip", r.Header.Get("Content-Type"))
			body, _ := io.ReadAll(r.Body)
			assert.Equal(t, []byte("zipdata"), body)
			w.WriteHeader(http.StatusOK)
		}))
		srvURL = srv.URL
		defer srv.Close()

		storageSource, err := newTestClient(srv).UploadFunctionSource(context.Background(), "proj", "us-central1", []byte("zipdata"))
		require.NoError(t, err)
		assert.Contains(t, string(storageSource), "bucket")
	})

	t.Run("rejects non-storage upload URL", func(t *testing.T) {
		tests := []struct {
			name string
			url  string
		}{
			{"spoofed suffix", "https://evil-googleapis.com/upload"},
			{"wrong subdomain", "https://cloudfunctions.googleapis.com/upload"},
			{"http scheme", "http://storage.googleapis.com/upload"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusOK)
					json.NewEncoder(w).Encode(map[string]interface{}{
						"uploadUrl":     tc.url,
						"storageSource": map[string]string{"bucket": "b", "object": "o"},
					})
				}))
				defer srv.Close()

				target, _ := url.Parse(srv.URL)
				client := &LiveGCFClient{
					Client:             gcp.NewClientWithHTTP(&http.Client{Transport: &rewriteTransport{base: target}}),
					skipUploadURLCheck: false,
				}
				_, err := client.UploadFunctionSource(context.Background(), "proj", "us-central1", []byte("zip"))
				require.Error(t, err)
				assert.Contains(t, err.Error(), "unexpected host")
			})
		}
	})

	t.Run("generateUploadUrl error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintln(w, `{"error":{"message":"denied"}}`)
		}))
		defer srv.Close()

		_, err := newTestClient(srv).UploadFunctionSource(context.Background(), "proj", "us-central1", []byte("zip"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status 403")
	})
}

// --- CreateFunction ---

func TestLiveGCFClient_CreateFunction(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPost, r.Method)
			assert.Contains(t, r.URL.RawQuery, "functionId=my-func")
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			sc := body["serviceConfig"].(map[string]interface{})
			assert.Equal(t, float64(10), sc["maxInstanceCount"])
			assert.Equal(t, float64(80), sc["maxInstanceRequestConcurrency"])
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"name": "operations/op-123"})
		}))
		defer srv.Close()

		opName, err := newTestClient(srv).CreateFunction(context.Background(), "proj", "us-central1", "my-func", FunctionConfig{
			ServiceAccount: "sa@proj.iam.gserviceaccount.com",
			EnvVars:        map[string]string{"KEY": "val"},
			StorageSource:  json.RawMessage(`{"bucket":"b","object":"o"}`),
			EntryPoint:     "ServeHTTP",
			Runtime:        "go126",
		})
		require.NoError(t, err)
		assert.Equal(t, "operations/op-123", opName)
	})

	t.Run("error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintln(w, `{"error":{"message":"invalid config"}}`)
		}))
		defer srv.Close()

		_, err := newTestClient(srv).CreateFunction(context.Background(), "proj", "us-central1", "func", FunctionConfig{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status 400")
	})
}

// --- UpdateFunction ---

func TestLiveGCFClient_UpdateFunction(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPatch, r.Method)
			assert.Contains(t, r.URL.Path, "/functions/my-func")
			assert.Contains(t, r.URL.RawQuery, "updateMask=")
			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)
			sc := body["serviceConfig"].(map[string]interface{})
			assert.Equal(t, "sa@proj.iam.gserviceaccount.com", sc["serviceAccountEmail"])
			bc := body["buildConfig"].(map[string]interface{})
			assert.Equal(t, "go126", bc["runtime"])
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"name": "operations/op-update"})
		}))
		defer srv.Close()

		opName, err := newTestClient(srv).UpdateFunction(context.Background(), "proj", "us-central1", "my-func", FunctionConfig{
			ServiceAccount: "sa@proj.iam.gserviceaccount.com",
			EnvVars:        map[string]string{"KEY": "val"},
			StorageSource:  json.RawMessage(`{"bucket":"b","object":"o"}`),
			EntryPoint:     "ServeHTTP",
			Runtime:        "go126",
		})
		require.NoError(t, err)
		assert.Equal(t, "operations/op-update", opName)
	})

	t.Run("error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintln(w, `{"error":{"message":"invalid update"}}`)
		}))
		defer srv.Close()

		_, err := newTestClient(srv).UpdateFunction(context.Background(), "proj", "us-central1", "func", FunctionConfig{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status 400")
	})
}

// --- UpdateFunctionEnvVars ---

func TestLiveGCFClient_UpdateFunctionEnvVars(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodPatch, r.Method)
			assert.Contains(t, r.URL.Path, "/functions/my-func")
			assert.Contains(t, r.URL.RawQuery, "serviceConfig.environmentVariables")

			var body map[string]interface{}
			json.NewDecoder(r.Body).Decode(&body)

			assert.Contains(t, body, "name")
			assert.Contains(t, body, "serviceConfig")
			assert.NotContains(t, body, "buildConfig")

			sc := body["serviceConfig"].(map[string]interface{})
			envVars := sc["environmentVariables"].(map[string]interface{})
			assert.Equal(t, "org1,org2", envVars["ALLOWED_ORGS"])

			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"name": "operations/envvar-op"})
		}))
		defer srv.Close()

		opName, err := newTestClient(srv).UpdateFunctionEnvVars(context.Background(), "proj", "us-central1", "my-func", map[string]string{
			"ALLOWED_ORGS": "org1,org2",
		})
		require.NoError(t, err)
		assert.Equal(t, "operations/envvar-op", opName)
	})

	t.Run("error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintln(w, `{"error":{"message":"permission denied"}}`)
		}))
		defer srv.Close()

		_, err := newTestClient(srv).UpdateFunctionEnvVars(context.Background(), "proj", "us-central1", "func", map[string]string{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status 403")
	})
}

// --- WaitForOperation ---

func TestLiveGCFClient_WaitForOperation(t *testing.T) {
	t.Run("completes on second poll", func(t *testing.T) {
		callCount := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			callCount++
			w.WriteHeader(http.StatusOK)
			if callCount == 1 {
				fmt.Fprintln(w, `{"done":false}`)
			} else {
				fmt.Fprintln(w, `{"done":true}`)
			}
		}))
		defer srv.Close()

		err := newTestClient(srv).WaitForOperation(context.Background(), "operations/op-1")
		require.NoError(t, err)
		assert.Equal(t, 2, callCount)
	})

	t.Run("operation error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, `{"done":true,"error":{"message":"deploy failed"}}`)
		}))
		defer srv.Close()

		err := newTestClient(srv).WaitForOperation(context.Background(), "operations/op-2")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "deploy failed")
	})

	t.Run("context canceled", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, `{"done":false}`)
		}))
		defer srv.Close()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := newTestClient(srv).WaitForOperation(ctx, "operations/op-3")
		require.Error(t, err)
	})
}

// --- GetProjectNumber ---

func TestLiveGCFClient_GetProjectNumber(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"projectNumber": "123456789"})
		}))
		defer srv.Close()

		num, err := newTestClient(srv).GetProjectNumber(context.Background(), "my-project")
		require.NoError(t, err)
		assert.Equal(t, "123456789", num)
	})

	t.Run("empty project number", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"projectNumber": ""})
		}))
		defer srv.Close()

		_, err := newTestClient(srv).GetProjectNumber(context.Background(), "proj")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty project number")
	})

	t.Run("error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintln(w, `{"error":{"message":"denied"}}`)
		}))
		defer srv.Close()

		_, err := newTestClient(srv).GetProjectNumber(context.Background(), "proj")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status 403")
	})
}

// --- iamAudience ---

func TestIAMAudience(t *testing.T) {
	got := iamAudience("123456789", "fullsend-pool", "github-oidc")
	assert.Equal(t, "https://iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/fullsend-pool/providers/github-oidc", got)
}

// --- encodeBase64 ---

func TestEncodeBase64(t *testing.T) {
	result := encodeBase64([]byte("hello"))
	assert.Equal(t, "aGVsbG8=", result)
}
