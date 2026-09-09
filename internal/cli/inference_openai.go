package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/spf13/cobra"

	"github.com/fullsend-ai/fullsend/internal/config"
	gh "github.com/fullsend-ai/fullsend/internal/forge/github"
	"github.com/fullsend-ai/fullsend/internal/inference/openaiwif"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const (
	// openAIRequestSchemaVersion versions this interchange format so a
	// document written by one release is readable by the next. OpenAI has
	// no API for providers or mappings today, so it is deliberately not a
	// claim about any future submission contract.
	openAIRequestSchemaVersion = "1"

	// defaultOpenAIAudiencePrefix is the default audience convention.
	defaultOpenAIAudiencePrefix = "fullsend://"

	// githubOIDCIssuer is the fixed OIDC issuer for GitHub Actions.
	githubOIDCIssuer = "https://token.actions.githubusercontent.com"

	// openAIDefaultPermission is the only permission an agent run needs.
	openAIDefaultPermission = "api.model.request"

	// openAIPullRefPattern is the ref assertion added as a companion
	// mapping when --ref is passed explicitly: PR-review-triggered agent
	// runs carry refs/pull/<N>/merge, and OpenAI mapping assertions are
	// AND-ed exact values, so covering both ref families requires two
	// mappings. OpenAI allows one trailing wildcard with a non-empty
	// prefix.
	openAIPullRefPattern = "refs/pull/*"

	// openAIMaxMappingsPerProvider is OpenAI's documented ceiling on
	// service-account mappings per identity provider.
	openAIMaxMappingsPerProvider = 50
)

// --- request command ---

// openAIRequestDoc is the top-level JSON schema for the request document.
type openAIRequestDoc struct {
	Version  string                 `json:"version"`
	Provider openAIRequestProvider  `json:"provider"`
	Mappings []openAIRequestMapping `json:"mappings"`
	Reply    openAIRequestReply     `json:"reply"`
}

type openAIRequestProvider struct {
	Issuer       string `json:"issuer"`
	Audience     string `json:"audience"`
	UploadedJWKS bool   `json:"uploaded_jwks"`
}

type openAIRequestMapping struct {
	Repository string                  `json:"repository"`
	Assertions openAIRequestAssertions `json:"assertions"`
	Target     openAIRequestTarget     `json:"target"`
}

type openAIRequestAssertions struct {
	Iss        string `json:"iss"`
	Aud        string `json:"aud"`
	Repository string `json:"repository"`
	Ref        string `json:"ref,omitempty"`
}

type openAIRequestTarget struct {
	Project        string `json:"project"`
	ServiceAccount string `json:"service_account"`
	// CreateInline is false when the caller named an existing service
	// account with --service-account, so the document does not tell the
	// administrator to create one.
	CreateInline bool     `json:"create_inline"`
	Permissions  []string `json:"permissions"`
}

type openAIRequestReply struct {
	IdentityProviderID string            `json:"identity_provider_id"`
	Audience           string            `json:"audience"`
	ServiceAccountIDs  map[string]string `json:"service_account_ids"`
}

func newInferenceOpenAICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "openai",
		Short: "Manage OpenAI Workload Identity Federation enrollment",
		Long: `Commands for enrolling repositories with OpenAI Workload Identity
Federation. Generate a request document for your administrator,
import the reply into fullsend configuration, and check the
exchange status.

'request' and 'import' produce a document and update local
configuration. 'import --variables' calls the GitHub API through the
forge client to set repository variables, and 'status' performs one OpenAI token
exchange when it runs inside a GitHub Actions job with id-token: write.
No OpenAI API key is used or created by any of them.`,
	}
	cmd.AddCommand(newInferenceOpenAIRequestCmd())
	cmd.AddCommand(newInferenceOpenAIImportCmd())
	cmd.AddCommand(newInferenceOpenAIStatusCmd())
	return cmd
}

func newInferenceOpenAIRequestCmd() *cobra.Command {
	var audience string
	var project string
	var serviceAccount string
	var ref string
	var format string
	var outFile string

	cmd := &cobra.Command{
		Use:   "request <owner/repo>[,<owner/repo>…]",
		Short: "Generate OpenAI WIF provider/mapping request",
		Long: `Generates the request document an administrator needs to enable
OpenAI Workload Identity Federation for one or more repositories.

Every value in the document is computed from the repository names.
Nothing is sent anywhere; the command needs no credentials.

Output formats:
  --format json   A versioned interchange schema, so a document written
                  by one release is readable by the next.
  --format md     A copy-paste ticket/email matching the guide's
                  route-B template.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repos, err := parseRepoList(args[0])
			if err != nil {
				return err
			}
			if len(repos) == 0 {
				return fmt.Errorf("at least one owner/repo is required")
			}

			audience = strings.TrimSpace(audience)
			project = strings.TrimSpace(project)
			serviceAccount = strings.TrimSpace(serviceAccount)
			ref = strings.TrimSpace(ref)

			// The assertion is compared literally against the OIDC ref
			// claim, which always starts with refs/. A value that does
			// not is a mapping no run can ever match, and the failure
			// would only surface as a 4xx at exchange time.
			if ref != "" && !strings.HasPrefix(ref, "refs/") {
				return fmt.Errorf("--ref must be a full git ref such as refs/heads/main (got %q)", ref)
			}

			// The default audience is derived from the owner, so every
			// repository in one request must share it — otherwise the
			// second owner's mapping would silently carry the first
			// owner's audience.
			if audience == "" {
				owners := repoOwners(repos)
				if len(owners) > 1 {
					return fmt.Errorf("repositories span more than one owner (%s): pass --audience with the provider's audience, since the default is derived from the owner", strings.Join(owners, ", "))
				}
				audience = defaultOpenAIAudiencePrefix + owners[0]
			}

			doc := buildRequestDoc(repos, audience, project, serviceAccount, ref)

			// OpenAI caps a provider at 50 mappings and --ref emits two
			// per repository, so a large enrolment can produce a
			// document the administrator cannot install as written.
			if len(doc.Mappings) > openAIMaxMappingsPerProvider {
				because := ""
				if ref != "" {
					because = " (--ref emits two mappings per repository)"
				}
				printer := ui.New(cmd.ErrOrStderr())
				printer.StepWarn(fmt.Sprintf(
					"%d mappings for %d repositories exceeds OpenAI's limit of %d per provider%s — split the enrolment across providers, or ask for a second provider",
					len(doc.Mappings), len(repos), openAIMaxMappingsPerProvider, because))
			}

			var output string
			switch format {
			case "json":
				b, err := json.MarshalIndent(doc, "", "  ")
				if err != nil {
					return fmt.Errorf("marshaling request JSON: %w", err)
				}
				output = string(b) + "\n"
			case "md":
				output, err = renderRequestMarkdown(doc)
				if err != nil {
					return fmt.Errorf("rendering request markdown: %w", err)
				}
			default:
				return fmt.Errorf("--format must be one of: json, md (got %q)", format)
			}

			if outFile != "" {
				if err := os.WriteFile(outFile, []byte(output), 0o644); err != nil {
					return fmt.Errorf("writing output to %s: %w", outFile, err)
				}
				printer := ui.New(cmd.OutOrStdout())
				printer.StepDone(fmt.Sprintf("Request written to %s", outFile))
				return nil
			}

			fmt.Fprint(cmd.OutOrStdout(), output)
			return nil
		},
	}

	cmd.Flags().StringVar(&audience, "audience", "", "OpenAI Workload Identity audience (default: fullsend://<owner>)")
	cmd.Flags().StringVar(&project, "project", "", "OpenAI project name or ID for the service accounts")
	cmd.Flags().StringVar(&serviceAccount, "service-account", "", "existing service account ID to map (default: create fullsend-<repo>-ci per repo)")
	cmd.Flags().StringVar(&ref, "ref", "", "optional ref assertion: when set, emits two mappings per repository (one for the given ref and one for refs/pull/*) so both branch and PR-review-triggered runs work")
	cmd.Flags().StringVar(&format, "format", "json", "output format: json, md")
	cmd.Flags().StringVar(&outFile, "out", "", "write output to a file instead of stdout")

	return cmd
}

// parseRepoList splits a comma-separated list of owner/repo arguments
// and validates each one.
func parseRepoList(arg string) ([]string, error) {
	parts := strings.Split(arg, ",")
	var repos []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			return nil, fmt.Errorf("expected owner/repo format, got %q (org-only targets are not supported; specify the repository)", p)
		}
		_, _, err := parseOrgOrRepo(p)
		if err != nil {
			return nil, err
		}
		repos = append(repos, p)
	}
	// A duplicate would render as two identical mappings in the request.
	// GitHub compares owner/repo case-insensitively, so Acme/Widget and
	// acme/widget are the same repository; the first spelling is kept.
	seen := make(map[string]bool, len(repos))
	deduped := repos[:0]
	for _, r := range repos {
		key := strings.ToLower(r)
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, r)
	}
	return deduped, nil
}

// repoOwners returns the distinct owners of a repository list, in order.
func repoOwners(repos []string) []string {
	var owners []string
	seen := make(map[string]bool, len(repos))
	for _, r := range repos {
		owner := strings.SplitN(r, "/", 2)[0]
		key := strings.ToLower(owner)
		if seen[key] {
			continue
		}
		seen[key] = true
		owners = append(owners, owner)
	}
	return owners
}

// defaultServiceAccountID derives the default service account name for
// a repository: fullsend-<repo>-ci.
func defaultServiceAccountID(repo string) string {
	parts := strings.SplitN(repo, "/", 2)
	repoName := parts[1]
	return "fullsend-" + repoName + "-ci"
}

func buildRequestDoc(repos []string, audience, project, serviceAccount, ref string) openAIRequestDoc {
	ref = strings.TrimSpace(ref)
	// An explicit --ref refs/pull/* asks for the mapping the companion
	// already provides; emitting both spends two of the fifty mapping
	// slots on one identical assertion.
	companion := ref != "" && ref != openAIPullRefPattern
	doc := openAIRequestDoc{
		Version: openAIRequestSchemaVersion,
		Provider: openAIRequestProvider{
			Issuer:       githubOIDCIssuer,
			Audience:     audience,
			UploadedJWKS: false,
		},
		Reply: openAIRequestReply{
			IdentityProviderID: "",
			Audience:           audience,
			ServiceAccountIDs:  make(map[string]string),
		},
	}

	for _, repo := range repos {
		sa := serviceAccount
		createInline := serviceAccount == ""
		if createInline {
			sa = defaultServiceAccountID(repo)
		}

		target := openAIRequestTarget{
			Project:        project,
			ServiceAccount: sa,
			CreateInline:   createInline,
			Permissions:    []string{openAIDefaultPermission},
		}

		// Default: assert iss, aud, repository only — no ref. This
		// matches the Vertex path's attribute.repository scoping and
		// covers all event types (issues, PR review, workflow_dispatch).
		//
		// When --ref is passed, emit two mappings per repository:
		// one for the explicit ref (e.g. refs/heads/main) and one for
		// refs/pull/* so PR-review-triggered runs work too. OpenAI
		// allows one trailing wildcard with a non-empty prefix, and
		// mapping assertions are OR-ed across mappings.
		mapping := openAIRequestMapping{
			Repository: repo,
			Assertions: openAIRequestAssertions{
				Iss:        githubOIDCIssuer,
				Aud:        audience,
				Repository: repo,
				Ref:        ref, // empty when --ref is not passed → omitted from JSON
			},
			Target: target,
		}
		doc.Mappings = append(doc.Mappings, mapping)

		if companion {
			// Companion mapping for PR-review-triggered runs.
			pullMapping := openAIRequestMapping{
				Repository: repo,
				Assertions: openAIRequestAssertions{
					Iss:        githubOIDCIssuer,
					Aud:        audience,
					Repository: repo,
					Ref:        openAIPullRefPattern,
				},
				Target: target,
			}
			doc.Mappings = append(doc.Mappings, pullMapping)
		}

		doc.Reply.ServiceAccountIDs[repo] = ""
	}

	return doc
}

// requestMarkdownTmpl matches the guide's route-B template structure.
//
// The reply table ranges over Reply.ServiceAccountIDs rather than over
// Mappings: --ref emits two mappings per repository, and the
// administrator fills in one service account per repository, not one
// per mapping. Row order stays stable because text/template visits map
// keys in sorted order, unlike a bare range over a map in Go.
var requestMarkdownTmpl = template.Must(template.New("request").Parse(`# OpenAI Workload Identity Federation Request

## Provider (reuse or create)

| Field | Value |
|---|---|
| OIDC issuer URL | ` + "`" + `{{ .Provider.Issuer }}` + "`" + ` |
| Audience | ` + "`" + `{{ .Provider.Audience }}` + "`" + ` |
| Use uploaded JWKS for token verification | **Off** |

If the organization already has a provider with a different audience,
use that audience — in the mapping assertions below as well as in the
Reply, since the two must match — and report it back. Re-running
` + "`" + `fullsend inference openai request --audience "<the provider's audience>"` + "`" + `
regenerates this document with it.

## Service account mappings

One mapping per repository, with these rules:

- Assertions are exact scalar values, AND-ed within a mapping and OR-ed across
  mappings; one trailing wildcard with a non-empty prefix is permitted per value
  (e.g. ` + "`" + `refs/pull/*` + "`" + `).
- Do not assert ` + "`" + `repository_owner` + "`" + `, ` + "`" + `workflow_ref` + "`" + ` or ` + "`" + `sub` + "`" + `: a fullsend
  installation starts agent runs from more than one workflow file, so any single
  value would exclude the others.
- Do **not** create an API key for the service account.
{{ range .Mappings }}
### {{ .Repository }}{{ if .Assertions.Ref }} (ref: ` + "`" + `{{ .Assertions.Ref }}` + "`" + `){{ end }}

| Field | Value |
|---|---|
| Claim assertions | ` + "`" + `iss` + "`" + ` = ` + "`" + `{{ .Assertions.Iss }}` + "`" + ` · ` + "`" + `aud` + "`" + ` = ` + "`" + `{{ .Assertions.Aud }}` + "`" + ` · ` + "`" + `repository` + "`" + ` = ` + "`" + `{{ .Assertions.Repository }}` + "`" + `{{ if .Assertions.Ref }} · ` + "`" + `ref` + "`" + ` = ` + "`" + `{{ .Assertions.Ref }}` + "`" + `{{ end }} |
| Project | {{ if .Target.Project }}` + "`" + `{{ .Target.Project }}` + "`" + `{{ else }}*(specify the project name or ID)*{{ end }} |
| Service account | {{ .Target.ServiceAccount }}{{ if .Target.CreateInline }} (create inline in the mapping){{ else }} (existing — map it, do not create a new one){{ end }} |
| Permissions | {{ range $i, $p := .Target.Permissions }}{{ if $i }}, {{ end }}` + "`" + `{{ $p }}` + "`" + `{{ end }} only |
{{ end }}
## Reply

Please provide the following identifiers so we can configure the repository:

| Identifier | Value |
|---|---|
| Identity provider ID | *(from the provider you created or reused)* |
| Provider's audience | ` + "`" + `{{ .Reply.Audience }}` + "`" + ` *(confirm or update if different)* |
{{ range $repo, $_ := .Reply.ServiceAccountIDs }}| Service account ID for {{ $repo }} | *(from the mapping above)* |
{{ end }}
These identifiers are not secrets — they grant nothing on their own.
The mapping only trusts a GitHub OIDC token whose claims match.
`))

func renderRequestMarkdown(doc openAIRequestDoc) (string, error) {
	var sb strings.Builder
	if err := requestMarkdownTmpl.Execute(&sb, doc); err != nil {
		return "", err
	}
	return sb.String(), nil
}

// --- import command ---

// openAIReplyDoc is the JSON shape accepted by the import command. Two
// documents are accepted, because both reach an administrator: the reply
// section on its own, and the whole request document from
// `inference openai request --format json` with its "reply" object filled
// in — which is what an administrator who edits the file we sent them
// hands back.
type openAIReplyDoc struct {
	IdentityProviderID string            `json:"identity_provider_id"`
	Audience           string            `json:"audience"`
	ServiceAccountID   string            `json:"service_account_id,omitempty"`
	ServiceAccountIDs  map[string]string `json:"service_account_ids,omitempty"`

	// Provider carries the audience when an administrator reusing an
	// existing provider edited it there — which is what the generated
	// document and the guide tell them to do.
	Provider *struct {
		Audience string `json:"audience"`
	} `json:"provider,omitempty"`

	// Reply carries the same fields when the file is a full request
	// document; its values win, since that is the section the
	// administrator was asked to fill in.
	Reply *openAIReplyDoc `json:"reply,omitempty"`
}

// resolved folds a full request document into the reply shape.
//
// The audience appears twice in a generated document — in the provider
// block and pre-filled in the reply — and the mapping was written
// against exactly one of them. An administrator reusing an existing
// provider edits whichever they read first, so a document where the two
// disagree is ambiguous rather than merely redundant: picking either
// silently configures the repository with an audience no mapping
// asserts, and every exchange then fails with a 4xx far from its cause.
func (d openAIReplyDoc) resolved() (openAIReplyDoc, error) {
	providerAudience := ""
	if d.Provider != nil {
		providerAudience = strings.TrimSpace(d.Provider.Audience)
	}

	if d.Reply == nil {
		if d.Audience == "" {
			d.Audience = providerAudience
		} else if err := checkAudienceAgreement(d.Audience, providerAudience); err != nil {
			return d, err
		}
		return d, nil
	}
	out := *d.Reply
	out.Reply = nil
	if out.Audience == "" {
		out.Audience = d.Audience
	}
	if out.Audience == "" {
		out.Audience = providerAudience
	} else if err := checkAudienceAgreement(out.Audience, providerAudience); err != nil {
		return out, err
	}
	if out.IdentityProviderID == "" {
		out.IdentityProviderID = d.IdentityProviderID
	}
	if out.ServiceAccountID == "" {
		out.ServiceAccountID = d.ServiceAccountID
	}
	if len(out.ServiceAccountIDs) == 0 {
		out.ServiceAccountIDs = d.ServiceAccountIDs
	}
	return out, nil
}

// checkAudienceAgreement refuses a document whose provider block and
// reply disagree about the audience.
func checkAudienceAgreement(replyAudience, providerAudience string) error {
	if providerAudience == "" || strings.TrimSpace(replyAudience) == providerAudience {
		return nil
	}
	return fmt.Errorf(
		"the document disagrees with itself about the audience: provider.audience is %q but reply.audience is %q — "+
			"set both to the audience the mapping actually asserts, or pass --audience to override the file",
		providerAudience, replyAudience)
}

// serviceAccountFor picks the service account for repo out of a reply.
// A reply for several repositories needs a selector, and says so rather
// than silently importing none.
func (d openAIReplyDoc) serviceAccountFor(repo string) (string, error) {
	if d.ServiceAccountID != "" {
		return d.ServiceAccountID, nil
	}
	filled := make(map[string]string, len(d.ServiceAccountIDs))
	for k, v := range d.ServiceAccountIDs {
		if strings.TrimSpace(v) != "" {
			filled[k] = v
		}
	}
	if repo != "" {
		for k, v := range filled {
			if strings.EqualFold(k, repo) {
				return v, nil
			}
		}
		if len(filled) > 0 {
			keys := make([]string, 0, len(filled))
			for k := range filled {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return "", fmt.Errorf("the reply has no service account for %s (it names %s)", repo, strings.Join(keys, ", "))
		}
	}
	// One filled entry among several named repositories is still
	// ambiguous: the others are filled in later, and a silent pick would
	// then attribute the wrong account.
	if len(d.ServiceAccountIDs) <= 1 {
		switch len(filled) {
		case 0:
			return "", nil
		case 1:
			for _, v := range filled {
				return v, nil
			}
		}
	}
	if len(filled) == 0 {
		return "", nil
	}
	keys := make([]string, 0, len(filled))
	for k := range filled {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return "", fmt.Errorf("the reply names %d service accounts (%s): pass --repo <owner/repo> to choose one, or --service-account-id", len(filled), strings.Join(keys, ", "))
}

func newInferenceOpenAIImportCmd() *cobra.Command {
	var (
		flagAudience           string
		flagIdentityProviderID string
		flagServiceAccountID   string
		fullsendDir            string
		variables              bool
		repo                   string
	)

	cmd := &cobra.Command{
		Use:   "import [reply.json]",
		Short: "Import OpenAI WIF identifiers into fullsend config",
		Long: `Takes the administrator's reply and writes inference.openai into
.fullsend/config.yaml through the same setters as
'fullsend github setup --openai-*'.

The reply can be provided as a JSON file argument or via flags.
All three identifiers (audience, identity-provider-id,
service-account-id) must be present — a partial trio is refused.

With --variables, sets the three FULLSEND_OPENAI_* repository
variables instead of writing to config.yaml (requires a GitHub
token with variable-write permissions and --repo).`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer := ui.New(cmd.OutOrStdout())

			ids, err := resolveImportIDs(printer, args, flagAudience, flagIdentityProviderID, flagServiceAccountID, repo)
			if err != nil {
				return err
			}

			if err := validateImportIDs(ids); err != nil {
				return err
			}

			if variables {
				return runImportVariables(cmd.Context(), printer, ids, repo)
			}

			return runImportConfig(printer, ids, fullsendDir)
		},
	}

	cmd.Flags().StringVar(&flagAudience, "audience", "", "OpenAI Workload Identity audience")
	cmd.Flags().StringVar(&flagIdentityProviderID, "identity-provider-id", "", "OpenAI identity provider ID")
	cmd.Flags().StringVar(&flagServiceAccountID, "service-account-id", "", "OpenAI service account ID")
	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", ".fullsend", "path to the .fullsend configuration directory")
	cmd.Flags().BoolVar(&variables, "variables", false, "set FULLSEND_OPENAI_* repository variables instead of writing config.yaml (requires --repo)")
	cmd.Flags().StringVar(&repo, "repo", "", "target repository (owner/repo) for --variables, and the repository to select from a reply naming several")

	return cmd
}

// resolveImportIDs takes the command arguments and flags and returns the
// OpenAI WIF config. Flags take precedence over the JSON file.
func resolveImportIDs(printer *ui.Printer, args []string, flagAudience, flagIdentityProviderID, flagServiceAccountID, repo string) (config.OpenAIWIFConfig, error) {
	var ids config.OpenAIWIFConfig

	// Load from JSON file if provided.
	if len(args) == 1 {
		data, err := os.ReadFile(args[0])
		if err != nil {
			return ids, fmt.Errorf("reading reply file: %w", err)
		}
		var doc openAIReplyDoc
		if err := json.Unmarshal(data, &doc); err != nil {
			return ids, fmt.Errorf("parsing reply JSON: %w", err)
		}
		// An explicit --audience is the documented way past a document
		// whose provider block and reply disagree, so the conflict is
		// only fatal when the operator has not already answered it.
		reply, err := doc.resolved()
		if err != nil {
			if flagAudience == "" {
				return ids, err
			}
			// --audience answers the ambiguity, but the operator should
			// still hear that the file contradicts itself: the mapping
			// was written against one of the two values, and only they
			// know which.
			printer.StepWarn(err.Error())
			printer.StepInfo("Using --audience " + flagAudience + "; make sure it is the audience the mapping asserts")
		}
		ids.Audience = reply.Audience
		ids.IdentityProviderID = reply.IdentityProviderID
		// An explicit --service-account-id is the answer to the very
		// ambiguity serviceAccountFor reports, so it is applied first
		// rather than after an error the operator was told to fix that way.
		if flagServiceAccountID == "" {
			sa, err := reply.serviceAccountFor(repo)
			if err != nil {
				return ids, err
			}
			ids.ServiceAccountID = sa
		}
	}

	// Flags override file values.
	if flagAudience != "" {
		ids.Audience = flagAudience
	}
	if flagIdentityProviderID != "" {
		ids.IdentityProviderID = flagIdentityProviderID
	}
	if flagServiceAccountID != "" {
		ids.ServiceAccountID = flagServiceAccountID
	}

	return ids.Trimmed(), nil
}

// validateImportIDs enforces the all-three-or-none rule.
func validateImportIDs(ids config.OpenAIWIFConfig) error {
	if ids.IsZero() {
		return fmt.Errorf("no identifiers provided: pass a reply JSON file or all three flags (--audience, --identity-provider-id, --service-account-id)")
	}
	if missing := ids.Missing(); len(missing) > 0 {
		return fmt.Errorf("--audience, --identity-provider-id, and --service-account-id must all be set (missing %s)", strings.Join(missing, ", "))
	}
	return nil
}

func runImportConfig(printer *ui.Printer, ids config.OpenAIWIFConfig, fullsendDir string) error {
	writer, err := config.LoadConfigWriter(fullsendDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		return fmt.Errorf("loading config from %s: %w", fullsendDir, err)
	}

	perRepo, ok := writer.(config.PerRepoConfigWriter)
	if !ok {
		return fmt.Errorf("inference openai import writes per-repo config; %s contains an org-mode config", fullsendDir)
	}

	perRepo.SetInferenceOpenAI(ids)

	// Fail closed the way `fullsend github setup` does, rather than
	// writing a config the next run would reject.
	if err := perRepo.Validate(); err != nil {
		return fmt.Errorf("invalid config after import: %w", err)
	}

	data, err := perRepo.Marshal()
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	configPath := filepath.Join(fullsendDir, "config.yaml")
	if err := os.MkdirAll(fullsendDir, 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", fullsendDir, err)
	}
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		return fmt.Errorf("writing config to %s: %w", configPath, err)
	}

	printer.StepDone("Wrote inference.openai to " + configPath)
	printer.StepInfo("This is a local write: commit " + configPath + " so CI runs see it (fullsend reads the base branch for pull-request events)")
	printer.KeyValue("audience", ids.Audience)
	printer.KeyValue("identity_provider_id", ids.IdentityProviderID)
	printer.KeyValue("service_account_id", ids.ServiceAccountID)

	return nil
}

// repoVariableSetter is the forge operation `import --variables` needs.
// Setting a repository variable is a forge operation, so it goes through
// forge.Client like every other one (docs/contributing/forge-abstraction.md
// prohibits shelling out to the gh CLI outside internal/forge/github).
type repoVariableSetter interface {
	CreateOrUpdateRepoVariable(ctx context.Context, owner, repo, name, value string) error
}

// newRepoVariableSetter builds the client from the usual token
// resolution; tests replace it.
var newRepoVariableSetter = func() (repoVariableSetter, error) {
	token, err := resolveToken()
	if err != nil {
		return nil, err
	}
	return gh.New(token), nil
}

func runImportVariables(ctx context.Context, printer *ui.Printer, ids config.OpenAIWIFConfig, repo string) error {
	if repo == "" {
		return fmt.Errorf("--repo is required when using --variables")
	}
	if !strings.Contains(repo, "/") {
		return fmt.Errorf("--repo must be in owner/repo format")
	}

	parts := strings.SplitN(repo, "/", 2)
	owner, repoName := parts[0], parts[1]

	if !githubOwnerPattern.MatchString(owner) {
		return fmt.Errorf("invalid owner name %q", owner)
	}
	if !githubRepoPattern.MatchString(repoName) {
		return fmt.Errorf("invalid repo name %q", repoName)
	}

	client, err := newRepoVariableSetter()
	if err != nil {
		return err
	}

	type varEntry struct {
		name  string
		value string
	}
	vars := []varEntry{
		{openAIAudienceEnv, ids.Audience},
		{openAIIdentityProviderIDEnv, ids.IdentityProviderID},
		{openAIServiceAccountIDEnv, ids.ServiceAccountID},
	}

	// The three are written one at a time, so a failure halfway leaves a
	// partial trio on the repository — which a run refuses. Say exactly
	// what was written so the operator can re-run or clear it.
	var written []string
	for _, v := range vars {
		printer.StepStart(fmt.Sprintf("Setting %s on %s", v.name, repo))
		if err := client.CreateOrUpdateRepoVariable(ctx, owner, repoName, v.name, v.value); err != nil {
			printer.StepFail(fmt.Sprintf("Failed to set %s", v.name))
			if len(written) > 0 {
				printer.StepWarn(fmt.Sprintf("%s already set on %s: the repository now holds a partial trio, which a run refuses — re-run the same command to finish, or remove them", strings.Join(written, ", "), repo))
			}
			return fmt.Errorf("setting variable %s on %s: %w", v.name, repo, err)
		}
		written = append(written, v.name)
		printer.StepDone(fmt.Sprintf("Set %s on %s", v.name, repo))
	}

	return nil
}

// --- status command ---

func newInferenceOpenAIStatusCmd() *cobra.Command {
	var fullsendDir string

	cmd := &cobra.Command{
		Use:   "status <owner/repo>",
		Short: "Check OpenAI WIF configuration and exchange status",
		Long: `Prints the resolved OpenAI WIF identifiers and their source
(config.yaml or environment variables), and flags a partial trio.

When run inside a GitHub Actions job with id-token: write, performs
one exchange through internal/inference/openaiwif and reports the
returned scope and expiry (the same code path as 'fullsend run')
without ever printing the token. Outside Actions, says so and stops
at the config checks.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, repo, err := parseOrgOrRepo(args[0])
			if err != nil {
				return err
			}
			if repo == "" {
				return fmt.Errorf("expected owner/repo format, got org-only %q", args[0])
			}

			printer := ui.New(cmd.OutOrStdout())
			return runInferenceOpenAIStatus(cmd, printer, repo, fullsendDir)
		},
	}

	cmd.Flags().StringVar(&fullsendDir, "fullsend-dir", ".fullsend", "path to the .fullsend configuration directory")

	return cmd
}

// openAIStatusSource describes where an identifier was resolved from.
type openAIStatusSource struct {
	// Source names where the trio came from as a whole ("variables" or
	// "config.yaml"), mirroring the run path's all-or-nothing rule.
	Source             string
	Audience           string
	AudienceSource     string
	IdentityProviderID string
	IDPSource          string
	ServiceAccountID   string
	SASource           string
}

// resolveOpenAIStatusSources resolves the identifiers exactly as
// resolveOpenAICredential does for a real run: the FULLSEND_OPENAI_*
// variables win as a set — if any one of them is set, that is the source,
// and a gap in it is a gap — and the committed inference.openai block is
// used only when none of them is set. Reporting a mixed trio here would
// tell the operator a run will work when the run would refuse it.
func resolveOpenAIStatusSources(fullsendDir string) (openAIStatusSource, error) {
	var s openAIStatusSource

	envAud := strings.TrimSpace(os.Getenv(openAIAudienceEnv))
	envIDP := strings.TrimSpace(os.Getenv(openAIIdentityProviderIDEnv))
	envSA := strings.TrimSpace(os.Getenv(openAIServiceAccountIDEnv))

	if envAud != "" || envIDP != "" || envSA != "" {
		s.Source = "variables"
		s.Audience, s.AudienceSource = envAud, "variable "+openAIAudienceEnv
		s.IdentityProviderID, s.IDPSource = envIDP, "variable "+openAIIdentityProviderIDEnv
		s.ServiceAccountID, s.SASource = envSA, "variable "+openAIServiceAccountIDEnv
		return s, nil
	}

	// The run path ignores the committed block where an exchange is
	// impossible and a static key is present — a developer's
	// OPENAI_API_KEY is not overridden by the repository's CI
	// configuration (run_openai.go, configApplies). Reporting the block
	// as the source there would describe a run that will not happen.
	if os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL") == "" && strings.TrimSpace(os.Getenv(openAIStaticKeyEnv)) != "" {
		s.Source = "static key"
		return s, nil
	}

	writer, err := config.LoadConfigWriter(fullsendDir, config.LoadOpts{MissingOK: true})
	if err != nil {
		// A malformed or unreadable config must not read as "nothing
		// configured yet" — that is the one state an operator would
		// misdiagnose.
		return s, fmt.Errorf("loading config from %s: %w", fullsendDir, err)
	}
	perRepo, ok := writer.(config.PerRepoConfigReader)
	if !ok {
		// Same condition import refuses by name, rather than a generic
		// "nothing configured" (org mode is deprecated, ADR 0044).
		return s, fmt.Errorf("%s contains an org-mode config; OpenAI WIF enrolment is per-repo", fullsendDir)
	}
	cfgIDs := perRepo.ConfigInferenceOpenAI().Trimmed()
	s.Source = "config.yaml"
	s.Audience, s.AudienceSource = cfgIDs.Audience, "config.yaml"
	s.IdentityProviderID, s.IDPSource = cfgIDs.IdentityProviderID, "config.yaml"
	s.ServiceAccountID, s.SASource = cfgIDs.ServiceAccountID, "config.yaml"
	return s, nil
}

func runInferenceOpenAIStatus(cmd *cobra.Command, printer *ui.Printer, repo, fullsendDir string) error {
	printer.Banner(Version())
	printer.Blank()
	printer.Header("OpenAI WIF Status: " + repo)
	printer.Blank()

	sources, err := resolveOpenAIStatusSources(fullsendDir)
	if err != nil {
		printer.StepFail("Could not read the configuration")
		return err
	}

	// Print resolved identifiers.
	printOpenAIStatusField(printer, "audience", sources.Audience, sources.AudienceSource)
	printOpenAIStatusField(printer, "identity_provider_id", sources.IdentityProviderID, sources.IDPSource)
	printOpenAIStatusField(printer, "service_account_id", sources.ServiceAccountID, sources.SASource)
	printer.Blank()

	// Check completeness.
	ids := config.OpenAIWIFConfig{
		Audience:           sources.Audience,
		IdentityProviderID: sources.IdentityProviderID,
		ServiceAccountID:   sources.ServiceAccountID,
	}

	if ids.IsZero() {
		if sources.Source == "static key" {
			printer.StepInfo(openAIStaticKeyEnv + " is set and this is not a GitHub Actions job, so a run here would use that key and ignore inference.openai — the same rule fullsend run applies")
			return nil
		}
		printer.StepFail("No OpenAI WIF identifiers configured")
		printer.StepInfo("Run 'fullsend inference openai import' or 'fullsend github setup --openai-*' to configure")
		return fmt.Errorf("no OpenAI WIF identifiers configured for %s", repo)
	}

	if missing := ids.Missing(); len(missing) > 0 {
		printer.StepWarn(fmt.Sprintf("Partial trio in %s: missing %s", sources.Source, strings.Join(missing, ", ")))
		if sources.Source == "variables" {
			printer.StepInfo("A run takes the three identifiers from one source: with any FULLSEND_OPENAI_* variable set, all three must come from variables — the config.yaml block is not consulted")
		}
		printer.StepInfo("All three identifiers must be set for the exchange to work")
		return fmt.Errorf("OpenAI WIF is partially configured in %s: missing %s", sources.Source, strings.Join(missing, ", "))
	}

	printer.StepDone("All three identifiers are set")
	printer.Blank()

	// Check if we're inside GitHub Actions with OIDC.
	oidcURL := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL")
	oidcToken := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")

	if oidcURL == "" || oidcToken == "" {
		printer.StepInfo("Not inside a GitHub Actions job with id-token: write")
		printer.StepInfo("The exchange can only be tested from a GitHub Actions workflow")
		return nil
	}

	// Only now does the repository argument matter: an exchange proves
	// the identity of the job it runs in, so exchanging for another
	// repository's name would report a mapping as healthy on the strength
	// of this repository's token. An unknown job repository is refused
	// too — without it there is nothing to attribute the result to.
	current := strings.TrimSpace(os.Getenv("GITHUB_REPOSITORY"))
	switch {
	case current == "":
		printer.StepWarn("GITHUB_REPOSITORY is not set, so the exchange could not be attributed to " + repo)
		printer.StepInfo("Run the command from a GitHub Actions job in " + repo)
		return nil
	case !strings.EqualFold(current, repo):
		printer.StepWarn(fmt.Sprintf("This job runs in %s, so it cannot test %s: an exchange here would prove %s's mapping only", current, repo, current))
		printer.StepInfo("Run the command from a job in " + repo + " to test its mapping")
		return nil
	}

	// Perform the exchange.
	printer.StepStart("Performing WIF exchange")
	tok, err := openAIExchange(cmd.Context(), openaiwif.Config{
		Audience:           ids.Audience,
		IdentityProviderID: ids.IdentityProviderID,
		ServiceAccountID:   ids.ServiceAccountID,
		OIDCRequestURL:     oidcURL,
		OIDCRequestToken:   oidcToken,
	})
	if err != nil {
		printer.StepFail("Exchange failed")
		// Never print the token in error messages — openaiwif already
		// ensures that, but be explicit.
		return fmt.Errorf("OpenAI WIF exchange: %w", err)
	}

	// A token broader than model access is refused by `fullsend run`, so
	// nothing may be reported as succeeded until the scope has passed.
	warning, scopeErr := checkOpenAIScope(tok.Scope)
	if scopeErr != nil {
		printer.StepFail("Exchange refused: the mapping grants more than model access")
		return fmt.Errorf("OpenAI WIF token refused: %w", scopeErr)
	}

	printer.StepDone("Exchange succeeded for " + repo)
	printer.Blank()

	scope := tok.Scope
	if scope == "" {
		scope = "(not narrowed)"
	}
	printer.KeyValue("scope", scope)
	printer.KeyValue("expires_in", time.Until(tok.ExpiresAt).Round(time.Second).String())

	if warning != "" {
		printer.StepWarn(warning)
	}

	return nil
}

func printOpenAIStatusField(printer *ui.Printer, name, value, source string) {
	if value == "" {
		printer.StepInfo(fmt.Sprintf("%s: (not set)", name))
	} else {
		printer.StepInfo(fmt.Sprintf("%s: %s (from %s)", name, value, source))
	}
}
