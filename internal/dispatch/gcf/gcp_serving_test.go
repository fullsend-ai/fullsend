package gcf

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A split such as 60% latest / 40% older revision is not a template match:
// the older revision's code is still serving, so callers must not skip the pin.
func TestLiveGCFClient_GetServiceRevisionInfo_SplitTrafficIsNotAMatch(t *testing.T) {
	serve := func(latestPercent int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.Contains(r.URL.Path, "/revisions/my-svc-00043-def"):
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"containers": []interface{}{map[string]interface{}{
						"env": []interface{}{map[string]string{"name": "ALLOWED_ORGS", "value": "org-a"}},
					}},
				})
			case strings.Contains(r.URL.Path, "/revisions"):
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{"revisions": []interface{}{}})
			default:
				statuses := []interface{}{
					map[string]interface{}{
						"type":     "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION",
						"revision": "my-svc-00043-def",
						"percent":  latestPercent,
					},
				}
				if latestPercent < 100 {
					statuses = append(statuses, map[string]interface{}{
						"type":     "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION",
						"revision": "my-svc-00042-abc",
						"percent":  100 - latestPercent,
					})
				}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"template": map[string]interface{}{
						"revision":   "my-svc-00043-def",
						"containers": []interface{}{map[string]interface{}{}},
					},
					"trafficStatuses":       statuses,
					"latestReadyRevision":   "my-svc-00043-def",
					"latestCreatedRevision": "my-svc-00043-def",
				})
			}
		}))
	}

	t.Run("split_60_40", func(t *testing.T) {
		srv := serve(60)
		defer srv.Close()
		info, err := newTestClient(srv).GetServiceRevisionInfo(context.Background(), "proj", "us-central1", "my-svc")
		require.NoError(t, err)
		assert.Equal(t, "my-svc-00043-def", info.TrafficRevisionShort)
		assert.Equal(t, 60, info.TrafficPercent)
		assert.False(t, info.TemplateMatchesTraffic)
	})

	t.Run("full_100", func(t *testing.T) {
		srv := serve(100)
		defer srv.Close()
		info, err := newTestClient(srv).GetServiceRevisionInfo(context.Background(), "proj", "us-central1", "my-svc")
		require.NoError(t, err)
		assert.Equal(t, 100, info.TrafficPercent)
		assert.True(t, info.TemplateMatchesTraffic)
	})
}

// The strict serving-env read must not return template env vars as serving
// state when no traffic-serving revision can be resolved; the lenient read
// keeps its template fallback for callers that tolerate it.
func TestLiveGCFClient_GetServiceServingEnvVars(t *testing.T) {
	staleTemplate := func(statuses []interface{}) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"template": map[string]interface{}{
					"containers": []interface{}{map[string]interface{}{
						"env": []interface{}{
							map[string]string{"name": "ALLOWED_ORGS", "value": "revoked-org"},
							map[string]string{"name": "PER_REPO_WIF_REPOS", "value": "*"},
						},
					}},
				},
				"trafficStatuses": statuses,
			})
		}))
	}

	for name, statuses := range map[string][]interface{}{
		"empty_traffic_statuses": {},
		"nil_traffic_statuses":   nil,
		"unresolved_revision_name": {
			map[string]interface{}{"type": "TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST", "revision": "", "percent": 100},
		},
	} {
		t.Run(name+"_strict_errors", func(t *testing.T) {
			srv := staleTemplate(statuses)
			defer srv.Close()
			envVars, err := newTestClient(srv).GetServiceServingEnvVars(context.Background(), "proj", "us-central1", "my-svc")
			require.Error(t, err)
			assert.Nil(t, envVars)
			assert.Contains(t, err.Error(), "no traffic-serving revision")
		})

		t.Run(name+"_lenient_still_falls_back", func(t *testing.T) {
			srv := staleTemplate(statuses)
			defer srv.Close()
			envVars, err := newTestClient(srv).GetServiceTrafficEnvVars(context.Background(), "proj", "us-central1", "my-svc")
			require.NoError(t, err)
			assert.Equal(t, "revoked-org", envVars["ALLOWED_ORGS"])
		})
	}

	t.Run("reads_serving_revision_when_resolved", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			if strings.Contains(r.URL.Path, "/revisions/my-svc-00001-abc") {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"containers": []interface{}{map[string]interface{}{
						"env": []interface{}{map[string]string{"name": "ALLOWED_ORGS", "value": "org-a"}},
					}},
				})
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"template": map[string]interface{}{
					"containers": []interface{}{map[string]interface{}{
						"env": []interface{}{map[string]string{"name": "ALLOWED_ORGS", "value": "revoked-org"}},
					}},
				},
				"trafficStatuses": []interface{}{
					map[string]interface{}{
						"type":     "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION",
						"revision": "my-svc-00001-abc",
						"percent":  100,
					},
				},
			})
		}))
		defer srv.Close()
		envVars, err := newTestClient(srv).GetServiceServingEnvVars(context.Background(), "proj", "us-central1", "my-svc")
		require.NoError(t, err)
		assert.Equal(t, "org-a", envVars["ALLOWED_ORGS"])
	})
}

// A desired explicit-revision split that differs from the observed
// trafficStatuses (an accepted rollback not yet observed) marks routing
// unsettled; matching desired and observed routing does not.
func TestLiveGCFClient_GetServiceRevisionInfo_RoutingUnsettled(t *testing.T) {
	const rev = "projects/proj/locations/us-central1/services/my-svc/revisions/"
	cases := []struct {
		name     string
		body     map[string]interface{}
		unsetled bool
	}{
		{
			name: "desired_differs_from_observed",
			body: map[string]interface{}{
				"traffic": []interface{}{map[string]interface{}{
					"type": "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION", "revision": "my-svc-00001-aaa", "percent": 100}},
				"trafficStatuses": []interface{}{map[string]interface{}{
					"type": "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION", "revision": rev + "my-svc-00002-bbb", "percent": 100}},
			},
			unsetled: true,
		},
		{
			name: "desired_matches_observed",
			body: map[string]interface{}{
				"traffic": []interface{}{map[string]interface{}{
					"type": "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION", "revision": "my-svc-00001-aaa", "percent": 100}},
				"trafficStatuses": []interface{}{map[string]interface{}{
					"type": "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION", "revision": rev + "my-svc-00001-aaa", "percent": 100}},
			},
		},
		{
			name: "generation_lags",
			body: map[string]interface{}{
				"generation":         "7",
				"observedGeneration": "6",
				"trafficStatuses": []interface{}{map[string]interface{}{
					"type": "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION", "revision": rev + "my-svc-00001-aaa", "percent": 100}},
			},
			unsetled: true,
		},
		{
			name: "reconciling",
			body: map[string]interface{}{
				"reconciling": true,
				"trafficStatuses": []interface{}{map[string]interface{}{
					"type": "TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION", "revision": rev + "my-svc-00001-aaa", "percent": 100}},
			},
			unsetled: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "/revisions") {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(tc.body)
			}))
			defer srv.Close()

			info, err := newTestClient(srv).GetServiceRevisionInfo(context.Background(), "proj", "us-central1", "my-svc")
			require.NoError(t, err)
			assert.Equal(t, tc.unsetled, info.RoutingUnsettled)
		})
	}
}
