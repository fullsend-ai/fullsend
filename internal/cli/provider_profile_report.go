package cli

import (
	"fmt"
	"os"
	"path"
	"slices"
	"strings"

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

// providerProfileFile is the part of a provider profile that says what the
// provider permits: its endpoints and its binaries allowlist.
type providerProfileFile struct {
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
// GET, HEAD or OPTIONS, or neither a preset nor rules, which the profile
// does not show to be read-only.
func (e providerProfileEndpoint) writeCapable() bool {
	if e.Access != "" {
		return e.Access != "read-only"
	}
	if len(e.Rules) == 0 {
		return true
	}
	for _, r := range e.Rules {
		if r.Allow == nil {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(r.Allow.Method)) {
		case "GET", "HEAD", "OPTIONS":
		default:
			return true
		}
	}
	return false
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
		r.line = fmt.Sprintf("provider %s: profile %s (not imported by this run; what it permits is not known to the runner)", pd.Name, id)
		return r
	}
	var prof providerProfileFile
	if err == nil {
		err = yaml.Unmarshal(data, &prof)
	}
	if err != nil {
		r.line = fmt.Sprintf("provider %s: profile %s (%s) could not be read: %v", pd.Name, id, r.source, err)
		return r
	}
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
		names = appendUnique(names, path.Base(b))
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
		printer.StepInfo(r.line)
	}
}

// durableCredentialWriteWarnings returns one warning per durable credential
// host file destination when any provider's profile may permit a write
// method. Both together mean a long-lived key whose lifetime the run does
// not control sits next to a route that can change remote state.
func durableCredentialWriteWarnings(durableDests []string, reports []providerProfileReport) []string {
	var writers []string
	for _, r := range reports {
		if r.writeCapable {
			writers = append(writers, fmt.Sprintf("%s (profile %s)", r.provider, r.profile))
		}
	}
	if len(writers) == 0 {
		return nil
	}
	warnings := make([]string, 0, len(durableDests))
	for _, dest := range durableDests {
		warnings = append(warnings, fmt.Sprintf(
			"host file dest %s is a durable credential, and provider %s permits write methods (access other than read-only); "+
				"a provider whose profile is access: read-only is the narrower choice. "+
				"The profile is a declaration: whether the gateway enforces it is a property of the deployment, which this run does not check",
			dest, strings.Join(writers, ", ")))
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
		printer.StepWarn(w)
	}
}
