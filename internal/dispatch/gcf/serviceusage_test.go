package gcf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ ServiceUsageClient = (*LiveGCFClient)(nil)

func TestLiveGCFClient_ServiceEnabled(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, http.MethodGet, r.Method)
			assert.Equal(t, "/v1/projects/my-project/services/iam.googleapis.com", r.URL.Path)
			json.NewEncoder(w).Encode(map[string]string{"state": "ENABLED"})
		}))
		defer srv.Close()

		enabled, err := newTestClient(srv).ServiceEnabled(context.Background(), "my-project", "iam.googleapis.com")
		require.NoError(t, err)
		assert.True(t, enabled)
	})

	t.Run("disabled", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			json.NewEncoder(w).Encode(map[string]string{"state": "DISABLED"})
		}))
		defer srv.Close()

		enabled, err := newTestClient(srv).ServiceEnabled(context.Background(), "my-project", "iam.googleapis.com")
		require.NoError(t, err)
		assert.False(t, enabled)
	})

	t.Run("permission denied", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintln(w, `{"error":{"message":"denied"}}`)
		}))
		defer srv.Close()

		_, err := newTestClient(srv).ServiceEnabled(context.Background(), "my-project", "iam.googleapis.com")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status 403")
		assert.Contains(t, err.Error(), "denied")
	})

	t.Run("bad json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintln(w, `not json`)
		}))
		defer srv.Close()

		_, err := newTestClient(srv).ServiceEnabled(context.Background(), "my-project", "iam.googleapis.com")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decoding service")
	})

	t.Run("invalid service name", func(t *testing.T) {
		_, err := (&LiveGCFClient{}).ServiceEnabled(context.Background(), "my-project", "../iam")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid service name")
	})
}

func TestLiveGCFClient_EnableServices(t *testing.T) {
	t.Run("no services is a no-op", func(t *testing.T) {
		require.NoError(t, (&LiveGCFClient{}).EnableServices(context.Background(), "my-project", nil))
	})

	t.Run("invalid service name", func(t *testing.T) {
		err := (&LiveGCFClient{}).EnableServices(context.Background(), "my-project", []string{"bad/name"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid service name")
	})

	t.Run("polls operation until done", func(t *testing.T) {
		polls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/projects/my-project/services:batchEnable":
				assert.Equal(t, http.MethodPost, r.Method)
				var body struct {
					ServiceIDs []string `json:"serviceIds"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				assert.Equal(t, []string{"iam.googleapis.com", "aiplatform.googleapis.com"}, body.ServiceIDs)
				fmt.Fprintln(w, `{"name":"operations/acf.p2-123","done":false}`)
			case "/v1/operations/acf.p2-123":
				polls++
				if polls < 2 {
					fmt.Fprintln(w, `{"name":"operations/acf.p2-123","done":false}`)
					return
				}
				fmt.Fprintln(w, `{"done":true}`)
			default:
				t.Errorf("unexpected path %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()

		c := newTestClient(srv)
		c.pollDelay = immediateDelay
		err := c.EnableServices(context.Background(), "my-project", []string{"iam.googleapis.com", "aiplatform.googleapis.com"})
		require.NoError(t, err)
		assert.Equal(t, 2, polls)
	})

	t.Run("operation already done", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintln(w, `{"name":"operations/noop.DONE_OPERATION","done":true}`)
		}))
		defer srv.Close()

		require.NoError(t, newTestClient(srv).EnableServices(context.Background(), "my-project", []string{"iam.googleapis.com"}))
	})

	t.Run("operation error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintln(w, `{"name":"operations/x","done":true,"error":{"message":"billing required"}}`)
		}))
		defer srv.Close()

		err := newTestClient(srv).EnableServices(context.Background(), "my-project", []string{"iam.googleapis.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "billing required")
	})

	t.Run("permission denied", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintln(w, `{"error":{"message":"Permission denied to enable service"}}`)
		}))
		defer srv.Close()

		err := newTestClient(srv).EnableServices(context.Background(), "my-project", []string{"iam.googleapis.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status 403 enabling services")
	})

	t.Run("invalid operation name", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintln(w, `{"name":"../evil","done":false}`)
		}))
		defer srv.Close()

		err := newTestClient(srv).EnableServices(context.Background(), "my-project", []string{"iam.googleapis.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid operation name")
	})

	t.Run("poll error status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				fmt.Fprintln(w, `{"name":"operations/x","done":false}`)
				return
			}
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprintln(w, `{"error":{"message":"denied"}}`)
		}))
		defer srv.Close()

		c := newTestClient(srv)
		c.pollDelay = immediateDelay
		err := c.EnableServices(context.Background(), "my-project", []string{"iam.googleapis.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected status 403 polling operation")
	})

	t.Run("poll bad json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				fmt.Fprintln(w, `{"name":"operations/x","done":false}`)
				return
			}
			fmt.Fprintln(w, `not json`)
		}))
		defer srv.Close()

		c := newTestClient(srv)
		c.pollDelay = immediateDelay
		err := c.EnableServices(context.Background(), "my-project", []string{"iam.googleapis.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decoding operation status")
	})

	t.Run("bad initial json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintln(w, `not json`)
		}))
		defer srv.Close()

		err := newTestClient(srv).EnableServices(context.Background(), "my-project", []string{"iam.googleapis.com"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decoding operation response")
	})

	t.Run("context cancelled while polling", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintln(w, `{"name":"operations/x","done":false}`)
		}))
		defer srv.Close()

		ctx, cancel := context.WithCancel(context.Background())
		c := newTestClient(srv)
		c.pollDelay = func(time.Duration) <-chan time.Time {
			cancel()
			return make(chan time.Time)
		}
		err := c.EnableServices(ctx, "my-project", []string{"iam.googleapis.com"})
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestIsAPINotEnabledError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"other", errors.New("permission denied"), false},
		{"canceled", context.Canceled, false},
		{"reason code", errors.New("status 403: SERVICE_DISABLED"), true},
		{
			"gcp message",
			fmt.Errorf("getting project number: %w", errors.New(
				"unexpected status 403 looking up project: Cloud Resource Manager API has not been used in project 123 before or it is disabled. Enable it by visiting https://console.developers.google.com/apis/api/cloudresourcemanager.googleapis.com/overview?project=123 then retry.")),
			true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsAPINotEnabledError(tc.err))
		})
	}
}
