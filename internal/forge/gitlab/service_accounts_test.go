package gitlab

import (
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
)

func TestSafetyInventoriesRejectMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		call func(*LiveClient) error
		bad  []string
	}{
		{"accounts", "/api/v4/projects/myorg%2Fmyrepo/service_accounts", func(c *LiveClient) error {
			_, err := c.ListProjectServiceAccounts(context.Background(), "myorg", "myrepo")
			return err
		}, []string{`null`, `[{}]`, `[{"id":0,"name":"fullsend-poller"}]`, `[{"id":-1,"name":"fullsend-poller"}]`, `[{"id":77}]`, `[{"id":77,"name":null}]`, `[{"id":77,"name":""}]`, `[{"id":77,"name":" "}]`, `[{"id":77,"username":"fullsend-poller"}]`}},
		{"members", "/api/v4/projects/myorg%2Fmyrepo/members/all", func(c *LiveClient) error {
			_, err := c.ListProjectMembers(context.Background(), "myorg", "myrepo")
			return err
		}, []string{`null`, `[{}]`, `[{"name":"fullsend-poller"}]`, `[{"id":0,"name":"fullsend-poller"}]`, `[{"id":-1,"name":"fullsend-poller"}]`, `[{"id":77}]`, `[{"id":77,"name":null}]`, `[{"id":77,"name":""}]`, `[{"id":77,"name":" "}]`, `[{"id":77,"username":"fullsend-poller"}]`}},
		{"tokens", "/api/v4/projects/myorg%2Fmyrepo/service_accounts/77/personal_access_tokens", func(c *LiveClient) error {
			_, err := c.ListServiceAccountPATs(context.Background(), "myorg", "myrepo", 77)
			return err
		}, []string{`null`, `[{"id":1}]`, `[{"id":1,"active":null}]`, `[{"id":0,"active":false}]`, `[{"id":1,"active":true,"user_id":88}]`}},
		{"keys", "/api/v4/users/77/keys", func(c *LiveClient) error { _, err := c.ListUserSSHKeys(context.Background(), 77); return err }, []string{`null`, `[{}]`, `[{"id":-1}]`}},
		{"jobs", "/api/v4/projects/myorg%2Fmyrepo/jobs", func(c *LiveClient) error {
			_, err := c.ListProjectActiveJobs(context.Background(), "myorg", "myrepo")
			return err
		}, []string{`null`, `[{}]`, `[{"id":1,"user":{"id":0}}]`}},
		{"schedules", "/api/v4/projects/myorg%2Fmyrepo/pipeline_schedules", func(c *LiveClient) error {
			_, err := c.ListProjectPipelineSchedules(context.Background(), "myorg", "myrepo")
			return err
		}, []string{`null`, `[{}]`, `[{"id":1,"owner":{"id":-1}}]`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, body := range append(tc.bad, `[]`) {
				t.Run(body, func(t *testing.T) {
					client, mux := setupTest(t)
					called := false
					mux.HandleFunc(tc.path, func(w http.ResponseWriter, r *http.Request) {
						called = true
						w.Header().Set("Content-Type", "application/json")
						_, err := io.WriteString(w, body)
						require.NoError(t, err)
					})
					err := tc.call(client)
					assert.Equal(t, body != `[]`, err != nil)
					assert.True(t, called)
				})
			}
		})
	}
}

func TestListProjectPipelineSchedules(t *testing.T) {
	client, mux := setupTest(t)
	called := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/pipeline_schedules", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_, err := io.WriteString(w, `[{"id":1,"owner":{"id":77}},{"id":2},{"id":3,"owner":{"id":88}}]`)
		require.NoError(t, err)
	})

	got, err := client.ListProjectPipelineSchedules(context.Background(), "myorg", "myrepo")
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, []PipelineScheduleOwner{
		{ID: 1, OwnerID: 77},
		{ID: 2, OwnerID: 0},
		{ID: 3, OwnerID: 88},
	}, got)
}

// A malformed later page must discard earlier results rather than turn a
// partial inventory into evidence that an account or member is absent.
func TestIdentityInventoriesRejectMalformedLaterPages(t *testing.T) {
	for _, kind := range []string{"service_accounts", "members/all"} {
		for _, body := range []string{`null`, `[{"id":101}]`, `[{"id":0,"name":"fullsend-poller"}]`} {
			t.Run(kind+"/"+body, func(t *testing.T) {
				client, mux := setupTest(t)
				var pages []string
				mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/"+kind, func(w http.ResponseWriter, r *http.Request) {
					page := r.URL.Query().Get("page")
					pages = append(pages, page)
					if page == "1" {
						entries := make([]map[string]any, serviceAccountPerPage)
						for i := range entries {
							entries[i] = map[string]any{"id": i + 1, "username": "service_account_project_1_abc", "name": "fullsend-poller", "access_level": 30}
						}
						writeJSON(t, w, http.StatusOK, entries)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, err := io.WriteString(w, body)
					require.NoError(t, err)
				})
				if kind == "service_accounts" {
					got, err := client.ListProjectServiceAccounts(context.Background(), "myorg", "myrepo")
					require.ErrorContains(t, err, "page 2")
					assert.Nil(t, got)
				} else {
					got, err := client.ListProjectMembers(context.Background(), "myorg", "myrepo")
					require.ErrorContains(t, err, "page 2")
					assert.Nil(t, got)
				}
				assert.Equal(t, []string{"1", "2"}, pages)
			})
		}
	}
}

func TestListProjectActiveJobsIncludesCanceling(t *testing.T) {
	client, mux := setupTest(t)
	called := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/jobs", func(w http.ResponseWriter, r *http.Request) {
		called = true
		assert.ElementsMatch(t, []string{"created", "scheduled", "pending", "preparing", "waiting_for_resource", "waiting_for_callback", "running", "canceling"}, r.URL.Query()["scope[]"])
		writeJSON(t, w, http.StatusOK, []map[string]any{{"id": 88, "user": map[string]any{"id": 77}, "status": "canceling"}})
	})
	jobs, err := client.ListProjectActiveJobs(context.Background(), "myorg", "myrepo")
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, 88, jobs[0].ID)
	assert.Equal(t, 77, jobs[0].UserID)
	assert.True(t, called)
}

func TestDeleteProjectServiceAccount(t *testing.T) {
	for _, code := range []int{http.StatusNoContent, http.StatusNotFound, http.StatusForbidden} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			client, mux := setupTest(t)
			called := false
			mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/service_accounts/77", func(w http.ResponseWriter, r *http.Request) {
				called = true
				assert.Equal(t, http.MethodDelete, r.Method)
				assert.Empty(t, r.URL.Query(), "never hard-delete contributions")
				w.WriteHeader(code)
			})
			err := client.DeleteProjectServiceAccount(context.Background(), "myorg", "myrepo", 77)
			assert.Equal(t, code == http.StatusForbidden, err != nil)
			assert.True(t, called)
		})
	}
}

func TestCreateProjectServiceAccount(t *testing.T) {
	client, mux := setupTest(t)
	handlerCalled := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		readJSONBody(t, r, &body)
		assert.Equal(t, "fullsend-poller", body["name"])
		writeJSON(t, w, http.StatusCreated, map[string]any{"id": 77, "username": "service_account_project_1_abc", "name": "fullsend-poller"})
	})

	sa, err := client.CreateProjectServiceAccount(context.Background(), "myorg", "myrepo", "fullsend-poller")
	require.NoError(t, err)
	assert.True(t, handlerCalled, "the registered handler must have served the request")
	assert.Equal(t, 77, sa.ID)
	assert.Equal(t, "fullsend-poller", sa.Name)
	assert.Equal(t, "service_account_project_1_abc", sa.Username)
}

func TestCreateProjectServiceAccount_NotFound(t *testing.T) {
	client, mux := setupTest(t)
	handlerCalled := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		writeJSON(t, w, http.StatusNotFound, map[string]any{"message": "404 Not Found"})
	})

	_, err := client.CreateProjectServiceAccount(context.Background(), "myorg", "myrepo", "fullsend-poller")
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrNotFound)
	assert.True(t, handlerCalled, "the registered 404 handler must have served the request")
}

func TestListProjectServiceAccounts(t *testing.T) {
	client, mux := setupTest(t)
	handlerCalled := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "1", r.URL.Query().Get("page"))
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{"id": 1, "username": "sa1", "name": "fullsend-poller"},
			{"id": 2, "username": "sa2", "name": "fullsend-coder"},
		})
	})

	got, err := client.ListProjectServiceAccounts(context.Background(), "myorg", "myrepo")
	require.NoError(t, err)
	assert.True(t, handlerCalled, "the registered handler must have served the request")
	require.Len(t, got, 2)
	assert.Equal(t, "fullsend-coder", got[1].Name)
}

func TestListProjectServiceAccounts_Error(t *testing.T) {
	client, mux := setupTest(t)
	handlerCalled := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/service_accounts", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		writeJSON(t, w, http.StatusForbidden, map[string]any{"message": "403 Forbidden"})
	})

	_, err := client.ListProjectServiceAccounts(context.Background(), "myorg", "myrepo")
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrForbidden)
	assert.True(t, handlerCalled, "the registered 403 handler must have served the request")
}

// A forbidden or not-found later page means accounts were already
// discovered: the listing is incomplete and must not keep the classification
// that reads as "service accounts unavailable".
func TestListProjectServiceAccounts_LaterPageFailureIsIncomplete(t *testing.T) {
	for name, status := range map[string]int{"forbidden": http.StatusForbidden, "not found": http.StatusNotFound} {
		t.Run(name, func(t *testing.T) {
			client, mux := setupTest(t)
			handlerCalled := false
			mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/service_accounts", func(w http.ResponseWriter, r *http.Request) {
				handlerCalled = true
				if r.URL.Query().Get("page") != "1" {
					writeJSON(t, w, status, map[string]any{"message": "denied"})
					return
				}
				page := make([]map[string]any, serviceAccountPerPage)
				for i := range page {
					page[i] = map[string]any{"id": i + 1, "username": "service_account_project_1_abc", "name": "fullsend-coder"}
				}
				writeJSON(t, w, http.StatusOK, page)
			})

			got, err := client.ListProjectServiceAccounts(context.Background(), "myorg", "myrepo")
			require.Error(t, err)
			assert.True(t, handlerCalled, "the registered handler must have served the request")
			assert.Nil(t, got)
			assert.ErrorContains(t, err, "incomplete")
			assert.False(t, forge.IsForbidden(err))
			assert.False(t, forge.IsNotFound(err))
			assert.False(t, forge.IsNotSupported(err))
		})
	}
}

func TestCreateServiceAccountPAT(t *testing.T) {
	client, mux := setupTest(t)
	handlerCalled := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/service_accounts/77/personal_access_tokens", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		readJSONBody(t, r, &body)
		assert.Equal(t, "fullsend-poller", body["name"])
		assert.Equal(t, []any{"api"}, body["scopes"])
		assert.Equal(t, "2027-01-01", body["expires_at"])
		writeJSON(t, w, http.StatusCreated, map[string]any{"id": 5, "name": "fullsend-poller", "active": true, "token": "glpat-test"})
	})

	tok, err := client.CreateServiceAccountPAT(context.Background(), "myorg", "myrepo", 77, "fullsend-poller", []string{"api"}, "2027-01-01")
	require.NoError(t, err)
	assert.True(t, handlerCalled, "the registered handler must have served the request")
	assert.Equal(t, 5, tok.ID)
	assert.Equal(t, 77, tok.UserID, "the owning service account is recorded when GitLab omits user_id")
	assert.Equal(t, "glpat-test", tok.Token)
}

func TestCreateServiceAccountPAT_Error(t *testing.T) {
	client, mux := setupTest(t)
	handlerCalled := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/service_accounts/77/personal_access_tokens", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		writeJSON(t, w, http.StatusBadRequest, map[string]any{"message": "bad"})
	})

	_, err := client.CreateServiceAccountPAT(context.Background(), "myorg", "myrepo", 77, "fullsend-poller", []string{"api"}, "2027-01-01")
	require.Error(t, err)
	assert.True(t, handlerCalled, "the registered 400 handler must have served the request")
	assert.Contains(t, err.Error(), `create service account token "fullsend-poller" for user 77`)
}

func TestListServiceAccountPATs(t *testing.T) {
	client, mux := setupTest(t)
	handlerCalled := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/service_accounts/77/personal_access_tokens", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{"id": 5, "name": "fullsend-poller", "active": true},
			{"id": 6, "name": "fullsend-poller", "active": false, "revoked": true, "user_id": 77},
		})
	})

	got, err := client.ListServiceAccountPATs(context.Background(), "myorg", "myrepo", 77)
	require.NoError(t, err)
	assert.True(t, handlerCalled, "the registered handler must have served the request")
	require.Len(t, got, 2)
	assert.Equal(t, 77, got[0].UserID)
	assert.True(t, got[1].Revoked)
}

func TestListServiceAccountPATs_Error(t *testing.T) {
	client, mux := setupTest(t)
	handlerCalled := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/service_accounts/77/personal_access_tokens", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		writeJSON(t, w, http.StatusNotFound, map[string]any{"message": "404"})
	})

	_, err := client.ListServiceAccountPATs(context.Background(), "myorg", "myrepo", 77)
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrNotFound)
	assert.True(t, handlerCalled, "the registered 404 handler must have served the request")
}

func TestRevokeServiceAccountPAT(t *testing.T) {
	client, mux := setupTest(t)
	called := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/service_accounts/77/personal_access_tokens/5", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodDelete, r.Method)
		called = true
		w.WriteHeader(http.StatusNoContent)
	})

	require.NoError(t, client.RevokeServiceAccountPAT(context.Background(), "myorg", "myrepo", 77, 5))
	assert.True(t, called)
}

func TestRevokeServiceAccountPAT_Error(t *testing.T) {
	client, mux := setupTest(t)
	handlerCalled := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/service_accounts/77/personal_access_tokens/5", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		writeJSON(t, w, http.StatusForbidden, map[string]any{"message": "403"})
	})

	err := client.RevokeServiceAccountPAT(context.Background(), "myorg", "myrepo", 77, 5)
	require.Error(t, err)
	assert.True(t, handlerCalled, "the registered 403 handler must have served the request")
	assert.ErrorIs(t, err, forge.ErrForbidden)
}

func TestAddProjectMember(t *testing.T) {
	client, mux := setupTest(t)
	handlerCalled := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/members", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		assert.Equal(t, http.MethodPost, r.Method)
		var body map[string]any
		readJSONBody(t, r, &body)
		assert.Equal(t, float64(77), body["user_id"])
		assert.Equal(t, float64(30), body["access_level"])
		writeJSON(t, w, http.StatusCreated, map[string]any{"id": 77, "access_level": 30})
	})

	require.NoError(t, client.AddProjectMember(context.Background(), "myorg", "myrepo", 77, 30))
	assert.True(t, handlerCalled, "the registered handler must have served the request")
}

func TestAddProjectMember_AlreadyMember(t *testing.T) {
	client, mux := setupTest(t)
	handlerCalled := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/members", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		writeJSON(t, w, http.StatusConflict, map[string]any{"message": "Member already exists"})
	})

	err := client.AddProjectMember(context.Background(), "myorg", "myrepo", 77, 30)
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrAlreadyExists)
	assert.True(t, handlerCalled, "the registered 409 handler must have served the request")
}

func TestUpdateProjectMemberAccessLevel(t *testing.T) {
	client, mux := setupTest(t)
	handlerCalled := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/members/77", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		assert.Equal(t, http.MethodPut, r.Method)
		var body map[string]any
		readJSONBody(t, r, &body)
		assert.Equal(t, float64(40), body["access_level"])
		writeJSON(t, w, http.StatusOK, map[string]any{"id": 77, "access_level": 40})
	})

	require.NoError(t, client.UpdateProjectMemberAccessLevel(context.Background(), "myorg", "myrepo", 77, 40))
	assert.True(t, handlerCalled, "the registered handler must have served the request")
}

func TestUpdateProjectMemberAccessLevel_Forbidden(t *testing.T) {
	client, mux := setupTest(t)
	handlerCalled := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/members/77", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		writeJSON(t, w, http.StatusForbidden, map[string]any{"message": "403"})
	})

	err := client.UpdateProjectMemberAccessLevel(context.Background(), "myorg", "myrepo", 77, 40)
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrForbidden)
	assert.True(t, handlerCalled, "the registered 403 handler must have served the request")
}

func TestGetAuthenticatedUserID(t *testing.T) {
	client, mux := setupTest(t)
	handlerCalled := false
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		writeJSON(t, w, http.StatusOK, map[string]any{"id": 77, "username": "sa"})
	})

	id, err := client.GetAuthenticatedUserID(context.Background())
	require.NoError(t, err)
	assert.True(t, handlerCalled, "the registered handler must have served the request")
	assert.Equal(t, 77, id)
}

func TestGetOwnPersonalAccessToken(t *testing.T) {
	client, mux := setupTest(t)
	handlerCalled := false
	mux.HandleFunc("/api/v4/personal_access_tokens/self", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		writeJSON(t, w, http.StatusOK, map[string]any{"id": 9, "name": "fullsend-poller", "active": true, "user_id": 77})
	})

	tok, err := client.GetOwnPersonalAccessToken(context.Background())
	require.NoError(t, err)
	assert.True(t, handlerCalled, "the registered handler must have served the request")
	assert.Equal(t, 9, tok.ID)
	assert.Equal(t, "fullsend-poller", tok.Name)
	assert.True(t, tok.Active)
	assert.Equal(t, 77, tok.UserID)
}

func TestGetOwnPersonalAccessToken_Errors(t *testing.T) {
	client, mux := setupTest(t)
	handlerCalled := false
	mux.HandleFunc("/api/v4/personal_access_tokens/self", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		writeJSON(t, w, http.StatusNotFound, map[string]any{"message": "404"})
	})
	_, err := client.GetOwnPersonalAccessToken(context.Background())
	assert.ErrorIs(t, err, forge.ErrNotFound)
	assert.True(t, handlerCalled, "the registered 404 handler must have served the request")
}

func TestGetAuthenticatedUserID_Errors(t *testing.T) {
	t.Run("missing id", func(t *testing.T) {
		client, mux := setupTest(t)
		handlerCalled := false
		mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
			handlerCalled = true
			writeJSON(t, w, http.StatusOK, map[string]any{"username": "sa"})
		})
		_, err := client.GetAuthenticatedUserID(context.Background())
		require.Error(t, err)
		assert.True(t, handlerCalled, "the registered handler must have served the request")
	})
	t.Run("unauthorized", func(t *testing.T) {
		client, mux := setupTest(t)
		handlerCalled := false
		mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
			handlerCalled = true
			writeJSON(t, w, http.StatusUnauthorized, map[string]any{"message": "401"})
		})
		_, err := client.GetAuthenticatedUserID(context.Background())
		require.Error(t, err)
		assert.True(t, handlerCalled, "the registered 401 handler must have served the request")
	})
}

func TestListProjectMembers(t *testing.T) {
	client, mux := setupTest(t)
	handlerCalled := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/members/all", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		assert.Equal(t, http.MethodGet, r.Method)
		writeJSON(t, w, http.StatusOK, []map[string]any{
			{"id": 5, "username": "alice", "name": "Alice", "access_level": 50},
			{"id": 9, "username": "sa_9", "name": "fullsend-poller", "access_level": 30},
		})
	})

	got, err := client.ListProjectMembers(context.Background(), "myorg", "myrepo")
	require.NoError(t, err)
	assert.True(t, handlerCalled, "the registered handler must have served the request")
	require.Len(t, got, 2)
	assert.Equal(t, "fullsend-poller", got[1].Name)
	assert.Equal(t, 30, got[1].AccessLevel)
}

func TestListProjectMembers_Error(t *testing.T) {
	client, mux := setupTest(t)
	handlerCalled := false
	mux.HandleFunc("/api/v4/projects/myorg%2Fmyrepo/members/all", func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		writeJSON(t, w, http.StatusForbidden, map[string]any{"message": "403"})
	})

	_, err := client.ListProjectMembers(context.Background(), "myorg", "myrepo")
	require.Error(t, err)
	assert.ErrorIs(t, err, forge.ErrForbidden)
	assert.True(t, handlerCalled, "the registered 403 handler must have served the request")
}
