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
	tests := []struct {
		name           string
		foreign        bool
		requestingRepo string
		repos          []string
		wantErrSubstr  string
		wantShape      string
	}{
		{"foreign empty", true, "fullsend-ai/fullsend", nil, "", ""},
		{"foreign non-empty allowed", true, "fullsend-ai/fullsend", []string{"e2e-lock"}, "", reposScopeShapeForeignRepoScoped},
		{"foreign non-empty multi", true, "fullsend-ai/fullsend", []string{"a", "b"}, "", reposScopeShapeForeignRepoScoped},
		{"same self", false, "acme/api", []string{"api"}, "", ""},
		{"same self case-insensitive", false, "acme/API", []string{"api"}, "", ""},
		{"same empty denied", false, "acme/api", nil, emptyDeny, ""},
		{"other repo denied", false, "acme/api", []string{"other"}, perRepoDeny, ""},
		{"fullsend self", false, "acme/.fullsend", []string{".fullsend"}, "", ""},
		// Legacy per-org shapes are no longer granted.
		{"legacy fullsend caller any denied", false, "acme/.fullsend", []string{"api"}, perRepoDeny, ""},
		{"legacy fullsend caller multi denied", false, "acme/.fullsend", []string{"a", "b", "c"}, perRepoDeny, ""},
		{"legacy enrolled fullsend denied", false, "acme/api", []string{".fullsend"}, perRepoDeny, ""},
		{"legacy enrolled pair denied", false, "acme/api", []string{"api", ".fullsend"}, perRepoDeny, ""},
		{"legacy enrolled pair reverse denied", false, "acme/api", []string{".fullsend", "api"}, perRepoDeny, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			shape, err := validateReposScope(tc.foreign, tc.requestingRepo, tc.repos)
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
	_, err := validateReposScope(false, "acme/api", []string{"other"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, errPerRepoCrossRepo) {
		t.Fatalf("expected errPerRepoCrossRepo sentinel, got %v", err)
	}
}
