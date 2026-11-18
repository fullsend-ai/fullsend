package cli

import (
	"fmt"
	"os"
	"path"
	"slices"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/harness"
	"github.com/fullsend-ai/fullsend/internal/resolve"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// Where the profile a provider uses came from.
const (
	profileSourceEmbedded = "embedded"
	profileSourceHarness  = "harness"
)

// maxProfileEndpointsListed is the most endpoints a provider line lists one
// by one; longer lists are summarised by host and access value.
const maxProfileEndpointsListed = 3

// profileCategoryInference is the profile category of a model provider.
// Its writes are model calls, not changes to remote state, so the durable
// credential warning does not name it.
const profileCategoryInference = "inference"

// providerProfileFile is the part of a provider profile that says what the
// provider permits: its endpoints and its binaries allowlist.
type providerProfileFile struct {
	Category  string                    `yaml:"category"`
	Endpoints []providerProfileEndpoint `yaml:"endpoints"`
	Binaries  []string                  `yaml:"binaries"`
}

type providerProfileEndpoint struct {
	Host     string                `yaml:"host"`
	Protocol string                `yaml:"protocol"`
	Access   string                `yaml:"access"`
	Rules    []providerProfileRule `yaml:"rules"`
}

type providerProfileRule struct {
	Allow *struct {
		Method string `yaml:"method"`
	} `yaml:"allow"`
}

// access names what the endpoint permits: its access preset, or the
// methods its allow rules list when it has no preset.
func (e providerProfileEndpoint) access() string {
	if e.Access != "" {
		return e.Access
	}
	var methods []string
	for _, r := range e.Rules {
		if r.Allow == nil {
			continue
		}
		m := strings.ToUpper(strings.TrimSpace(r.Allow.Method))
		if m == "" {
			m = "*"
		}
		methods = appendUnique(methods, m)
	}
	if len(methods) == 0 {
		return "unspecified"
	}
	return "rules " + strings.Join(methods, "/")
}

// writeCapable reports whether the endpoint may permit a write method: an
// access preset other than read-only, an allow rule for a method other than
// GET, HEAD or OPTIONS, or neither a preset nor any allow rule (deny-only
// rules included), which the profile does not show to be read-only. A
// preset and rules on the same endpoint are both checked.
func (e providerProfileEndpoint) writeCapable() bool {
	if e.Access != "" && e.Access != "read-only" {
		return true
	}
	allows := 0
	for _, r := range e.Rules {
		if r.Allow == nil {
			continue
		}
		allows++
		switch strings.ToUpper(strings.TrimSpace(r.Allow.Method)) {
		case "GET", "HEAD", "OPTIONS":
		default:
			return true
		}
	}
	return e.Access == "" && allows == 0
}

// providerProfileReport is the profile a provider was resolved to for this
// run, as the run output reports it.
type providerProfileReport struct {
	provider string
	profile  string
	// source is profileSourceEmbedded or profileSourceHarness, or "" when
	// the run did not import the profile and so cannot say what it permits.
	source string
	// line is the run output line for the provider.
	line string
	// writeCapable is true when any endpoint of the profile may permit a
	// write method.
	writeCapable bool
	// inference is true for a profile in the inference category.
	inference bool
	// unknown is true when the run could not read the profile, so what it
	// permits is not known.
	unknown bool
}

// resolveProviderProfiles reports the profile each provider in defs uses: a
// copy the harness lists (openshell.profiles, by path or URL) when there is
// one, since a listed copy of a reserved id is kept live
// (warnReservedProfileCopies), else the copy embedded in this binary for a
// reserved id. Providers named in skipped are left out.
func resolveProviderProfiles(defs []harness.ProviderDef, listed []resolve.ResolvedProfile, skipped map[string]struct{}) []providerProfileReport {
	reports := make([]providerProfileReport, 0, len(defs))
	for _, pd := range defs {
		if _, skip := skipped[pd.Name]; skip {
			continue
		}
		reports = append(reports, resolveProviderProfile(pd, listed))
	}
	return reports
}

func resolveProviderProfile(pd harness.ProviderDef, listed []resolve.ResolvedProfile) providerProfileReport {
	id := pd.Type
	// The run uses the canonical lowercase id for these two types.
	for _, canonical := range []string{openAIProviderType, jiraProviderType} {
		if strings.EqualFold(id, canonical) {
			id = canonical
		}
	}
	r := providerProfileReport{provider: pd.Name, profile: id}
	var data []byte
	var err error
	found := false
	for _, rp := range listed {
		if rp.ID == id {
			r.source = profileSourceHarness
			data, err = os.ReadFile(rp.LocalPath)
			found = true
			break
		}
	}
	if !found && isReservedProfileID(id) {
		r.source = profileSourceEmbedded
		data, err = scaffold.FullsendRepoFile("profiles/" + id + ".yaml")
		found = true
	}
	if !found {
		r.unknown = true
		r.line = fmt.Sprintf("provider %s: profile %s (not imported by this run; what it permits is not known to the runner)", pd.Name, id)
		return r
	}
	var prof providerProfileFile
	if err == nil {
		err = yaml.Unmarshal(data, &prof)
	}
	if err != nil {
		r.unknown = true
		r.line = fmt.Sprintf("provider %s: profile %s (%s) could not be read: %v", pd.Name, id, r.source, err)
		return r
	}
	r.inference = prof.Category == profileCategoryInference
	for _, e := range prof.Endpoints {
		if e.writeCapable() {
			r.writeCapable = true
		}
	}
	r.line = fmt.Sprintf("provider %s: profile %s (%s) -> %s; %s", pd.Name, id, r.source, describeProfileEndpoints(prof.Endpoints), describeProfileBinaries(prof.Binaries))
	return r
}

// describeProfileEndpoints lists each endpoint as "host protocol access",
// or summarises a long list as "<n> endpoints: <hosts> (<access values>)".
func describeProfileEndpoints(endpoints []providerProfileEndpoint) string {
	if len(endpoints) == 0 {
		return "no endpoints"
	}
	if len(endpoints) <= maxProfileEndpointsListed {
		parts := make([]string, 0, len(endpoints))
		for _, e := range endpoints {
			parts = append(parts, fmt.Sprintf("%s %s %s", e.Host, e.Protocol, e.access()))
		}
		return strings.Join(parts, ", ")
	}
	var hosts, access []string
	for _, e := range endpoints {
		hosts = appendUnique(hosts, e.Host)
		access = appendUnique(access, e.access())
	}
	return fmt.Sprintf("%d endpoints: %s (%s)", len(endpoints), strings.Join(hosts, ", "), strings.Join(access, ", "))
}

// describeProfileBinaries lists the binaries allowlist by base name
// ("**/gh" -> "gh").
func describeProfileBinaries(binaries []string) string {
	var names []string
	for _, b := range binaries {
		// Only a "**/" glob is shortened: an absolute path names one
		// file and is printed as is.
		if rest, ok := strings.CutPrefix(b, "**/"); ok {
			b = path.Base(rest)
		}
		names = appendUnique(names, b)
	}
	if len(names) == 0 {
		return "no binaries listed"
	}
	return "binaries " + strings.Join(names, ", ")
}

func appendUnique(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

// printProviderProfiles prints one line per provider naming the profile it
// uses, where that profile came from, and what it permits.
func printProviderProfiles(reports []providerProfileReport, printer *ui.Printer) {
	for _, r := range reports {
		printer.StepInfo(printableLine(r.line))
	}
}

// printableLine replaces control characters (newlines, carriage returns,
// escape sequences) with "?". Profile and host-file values come from
// repository files, and a raw newline could start a CI log command.
func printableLine(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '?'
		}
		return r
	}, s)
}

// durableCredentialWriteWarnings returns one warning per durable credential
// host file destination when any provider's profile may permit a write
// method, or could not be read. Both together mean a long-lived key whose
// lifetime the run does not control sits next to a route that can change
// remote state. Inference profiles are left out: their writes are model
// calls, and no read-only inference profile exists to point at.
func durableCredentialWriteWarnings(durableDests []string, reports []providerProfileReport) []string {
	var writers, unknown []string
	for _, r := range reports {
		named := fmt.Sprintf("%s (profile %s)", r.provider, r.profile)
		switch {
		case r.unknown:
			unknown = append(unknown, named)
		case r.writeCapable && !r.inference:
			writers = append(writers, named)
		}
	}
	if len(writers) == 0 && len(unknown) == 0 {
		return nil
	}
	var clauses []string
	if len(writers) > 0 {
		noun, verb := "provider", "permits"
		if len(writers) > 1 {
			noun, verb = "providers", "permit"
		}
		clauses = append(clauses, fmt.Sprintf("%s %s %s write methods", noun, strings.Join(writers, ", "), verb))
	}
	if len(unknown) > 0 {
		noun := "provider"
		if len(unknown) > 1 {
			noun = "providers"
		}
		clauses = append(clauses, fmt.Sprintf("%s %s could not be checked", noun, strings.Join(unknown, ", ")))
	}
	remedies := []string{"a short-lived credential that does not need allow_durable"}
	if len(writers) > 0 {
		remedies = append(remedies, "a provider whose profile is access: read-only (for example github-ro instead of github)")
	}
	if len(unknown) > 0 {
		remedies = append(remedies, "listing the unchecked profile in openshell.profiles so the run can read it")
	}
	remedy := strings.Join(remedies[:len(remedies)-1], ", ") + ", or " + remedies[len(remedies)-1]
	if len(remedies) == 2 {
		remedy = remedies[0] + " or " + remedies[1]
	}
	warnings := make([]string, 0, len(durableDests))
	for _, dest := range durableDests {
		warnings = append(warnings, fmt.Sprintf(
			"host file dest %s is a durable credential, and %s. Prefer %s. "+
				"A profile is a declaration: whether the gateway enforces it depends on the deployment, and this run does not check that.",
			dest, strings.Join(clauses, "; "), remedy))
	}
	return warnings
}

// durableHostFileDests returns the sandbox destination of every host_files
// entry the run copies whose source is a durable credential. Such entries
// carry allow_durable: true, or checkHostFileCredentials has already failed
// the run.
func durableHostFileDests(h *harness.Harness) []string {
	var dests []string
	for _, hf := range h.HostFiles {
		src, copied := hostFileSource(hf)
		if !copied {
			continue
		}
		shape, err := classifyHostFile(src, hf.Expand)
		if err != nil || !shape.durable() {
			continue
		}
		dests = append(dests, hf.Dest)
	}
	return dests
}

// warnDurableCredentialWriteProviders prints a warning when the harness
// mounts a durable credential host file and names a provider whose profile
// may permit a write method. It never fails the run.
func warnDurableCredentialWriteProviders(h *harness.Harness, reports []providerProfileReport, printer *ui.Printer) {
	if len(reports) == 0 {
		return
	}
	for _, w := range durableCredentialWriteWarnings(durableHostFileDests(h), reports) {
		printer.StepWarn(printableLine(w))
	}
}
