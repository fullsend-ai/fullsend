package mintcore

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeMintRepos(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"nil", nil, nil},
		{"empty", []string{}, []string{}},
		{"star alone", []string{"*"}, nil},
		{"star with other", []string{"*", "api"}, []string{"*", "api"}},
		{"normal", []string{"api"}, []string{"api"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeMintRepos(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("len=%d want %d (%v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v want %v", got, tc.want)
				}
			}
		})
	}
}

func TestEnvTruthy(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"1", "true", "TRUE", "Yes", " yes "} {
		if !EnvTruthy(v) {
			t.Fatalf("%q should be truthy", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no", "on"} {
		if EnvTruthy(v) {
			t.Fatalf("%q should not be truthy", v)
		}
	}
}

func TestValidateReposScope(t *testing.T) {
	t.Parallel()
	const emptyDeny = "same-org mint requires non-empty repos"
	const perRepoDeny = "per-repo mint requires repos to be exactly the requesting repository"
	const perOrgDeny = "repos scope not allowed for per-org caller"
	tests := []struct {
		name           string
		foreign        bool
		requestingRepo string
		repos          []string
		perRepo        bool
		wantErrSubstr  string
		wantShape      string
	}{
		{"foreign empty", true, "fullsend-ai/fullsend", nil, false, "", ""},
		{"foreign non-empty allowed", true, "fullsend-ai/fullsend", []string{"e2e-lock"}, false, "", reposScopeShapeForeignRepoScoped},
		{"foreign non-empty multi", true, "fullsend-ai/fullsend", []string{"a", "b"}, true, "", reposScopeShapeForeignRepoScoped},
		{"same self", false, "acme/api", []string{"api"}, false, "", ""},
		{"same empty per-org", false, "acme/api", nil, false, emptyDeny, ""},
		{"same empty per-repo", false, "acme/api", nil, true, emptyDeny, ""},
		{"per-repo other denied", false, "acme/.fullsend", []string{"api"}, true, perRepoDeny, ""},
		{"per-org fullsend any", false, "acme/.fullsend", []string{"api"}, false, "", reposScopeShapeFullsendAny},
		{"per-org fullsend multi", false, "acme/.fullsend", []string{"a", "b", "c"}, false, "", reposScopeShapeFullsendAny},
		{"per-org fullsend pair", false, "acme/.fullsend", []string{"api", ".fullsend"}, false, "", reposScopeShapeFullsendAny},
		{"fullsend self per-repo", false, "acme/.fullsend", []string{".fullsend"}, true, "", ""},
		{"enrolled fullsend per-org", false, "acme/api", []string{".fullsend"}, false, "", reposScopeShapeEnrolledFullsend},
		{"enrolled pair per-org", false, "acme/api", []string{"api", ".fullsend"}, false, "", reposScopeShapeEnrolledPair},
		{"enrolled pair reverse", false, "acme/api", []string{".fullsend", "api"}, false, "", reposScopeShapeEnrolledPair},
		{"enrolled other per-org denied", false, "acme/api", []string{"other"}, false, perOrgDeny, ""},
		{"enrolled multi per-org denied", false, "acme/api", []string{"api", ".fullsend", "x"}, false, perOrgDeny, ""},
		{"enrolled pair per-repo denied", false, "acme/api", []string{"api", ".fullsend"}, true, perRepoDeny, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			shape, err := validateReposScope(tc.foreign, tc.requestingRepo, tc.repos, tc.perRepo)
			if tc.wantErrSubstr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if shape != tc.wantShape {
					t.Fatalf("shape=%q want %q", shape, tc.wantShape)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error")
			}
			if shape != "" {
				t.Fatalf("expected empty shape on error, got %q", shape)
			}
			if !strings.Contains(err.Error(), tc.wantErrSubstr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErrSubstr)
			}
		})
	}
}

func TestValidateReposScope_PerRepoSentinel(t *testing.T) {
	t.Parallel()
	_, err := validateReposScope(false, "acme/api", []string{"other"}, true)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errPerRepoCrossRepo) {
		t.Fatalf("expected errPerRepoCrossRepo sentinel, got %v", err)
	}
}
