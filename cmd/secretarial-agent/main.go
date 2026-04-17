// Command secretarial-agent ingests meeting notes from Google Drive and
// updates the GitHub issue backlog. See issue #149 for design context.
//
// Usage (local dry run with LLM extraction):
//
//	export GOOGLE_APPLICATION_CREDENTIALS_JSON="$(cat /path/to/sa-key.json)"
//	export GH_TOKEN="$(gh auth token)"
//	go run ./cmd/secretarial-agent \
//	    --repo your-org/your-repo \
//	    --search-query "team sync" \
//	    --gcp-project-id your-gcp-project \
//	    --dry-run \
//	    --verbose
//
// Omit --gcp-project-id to fall back to heuristic (regex) extraction.
//
// In GitHub Actions the binary reads from env vars and never uses --verbose,
// so meeting content never appears in the public CI log.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/secretarial"
)

func main() {
	repo := flag.String("repo", "", "GitHub repository (owner/name)")
	searchQuery := flag.String("search-query", "", "Find docs by name (e.g. \"team sync\") — works with Shared Drives")
	nameFilter := flag.String("name-filter", "", "Only process docs whose name contains this substring (e.g. \"Notes by Gemini\")")
	gcpProject := flag.String("gcp-project-id", "", "GCP project for Vertex AI (enables LLM extraction)")
	vertexRegion := flag.String("vertex-region", "", "Vertex AI region (default \"global\")")
	llmModel := flag.String("llm-model", "", "Claude model ID (default \"claude-sonnet-4-6\")")
	issueLimit := flag.Int("issue-limit", 0, "Max open issues to fetch for LLM context (default 500)")
	commentsOnly := flag.Bool("comments-only", false, "Only comment on existing issues; skip new issue creation")
	dryRun := flag.Bool("dry-run", false, "Preview without writing to GitHub")
	verbose := flag.Bool("verbose", false, "Show full topic detail (local use only — never in CI)")
	lookback := flag.Int("lookback-hours", 0, "How far back to look for docs (default 3)")
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: level,
	})))

	cfg, err := buildConfig(*repo, *searchQuery, *nameFilter, *gcpProject, *vertexRegion, *llmModel, *issueLimit, *commentsOnly, *dryRun, *verbose, *lookback)
	if err != nil {
		slog.Error("configuration error", "err", err)
		os.Exit(1)
	}

	ctx := context.Background()
	report, err := secretarial.Run(ctx, cfg)
	if err != nil {
		slog.Error("agent failed", "err", err)
		os.Exit(1)
	}

	if report != nil {
		if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
			if f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644); err == nil {
				fmt.Fprint(f, report.FormatMarkdown(cfg.Repo))
				f.Close()
			}
		}

		if path := os.Getenv("SECRETARIAL_REPORT_FILE"); path != "" {
			runURL := os.Getenv("SECRETARIAL_RUN_URL")
			os.WriteFile(path, []byte(report.FormatSlack(cfg.Repo, runURL)), 0644)
		}
	}
}

// buildConfig merges CLI flags with env vars. Flags take precedence.
func buildConfig(
	flagRepo, flagSearch, flagNameFilter string,
	flagGCPProject, flagVertexRegion, flagLLMModel string,
	flagIssueLimit int,
	flagCommentsOnly, flagDry, flagVerbose bool,
	flagLookback int,
) (secretarial.Config, error) {
	repo := firstNonEmpty(flagRepo, os.Getenv("GITHUB_REPOSITORY"))
	if repo == "" {
		return secretarial.Config{}, fmt.Errorf("--repo or GITHUB_REPOSITORY is required")
	}

	searchQuery := firstNonEmpty(flagSearch, os.Getenv("GDRIVE_SEARCH_QUERY"))
	if searchQuery == "" {
		return secretarial.Config{}, fmt.Errorf("--search-query or GDRIVE_SEARCH_QUERY is required")
	}

	credsJSON := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS_JSON")
	if credsJSON == "" {
		return secretarial.Config{}, fmt.Errorf("GOOGLE_APPLICATION_CREDENTIALS_JSON env var is required")
	}

	lookback := 3
	if flagLookback > 0 {
		lookback = flagLookback
	} else if v := os.Getenv("LOOKBACK_HOURS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return secretarial.Config{}, fmt.Errorf("LOOKBACK_HOURS must be an integer: %w", err)
		}
		lookback = n
	}

	dryRun := flagDry
	if !dryRun {
		v := strings.ToLower(os.Getenv("DRY_RUN"))
		dryRun = v == "true" || v == "1" || v == "yes"
	}

	commentsOnly := flagCommentsOnly
	if !commentsOnly {
		v := strings.ToLower(os.Getenv("COMMENTS_ONLY"))
		commentsOnly = v == "true" || v == "1" || v == "yes"
	}

	issueLimit := flagIssueLimit
	if issueLimit <= 0 {
		if v := os.Getenv("ISSUE_LIMIT"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return secretarial.Config{}, fmt.Errorf("ISSUE_LIMIT must be an integer: %w", err)
			}
			issueLimit = n
		}
	}

	nameFilter := firstNonEmpty(flagNameFilter, os.Getenv("GDRIVE_NAME_FILTER"))
	gcpProjectID := firstNonEmpty(flagGCPProject, os.Getenv("GCP_PROJECT_ID"))
	vertexRegion := firstNonEmpty(flagVertexRegion, os.Getenv("VERTEX_REGION"))
	llmModel := firstNonEmpty(flagLLMModel, os.Getenv("LLM_MODEL"))

	return secretarial.Config{
		Repo:        repo,
		SearchQuery: searchQuery,
		NameFilter:      nameFilter,
		CredentialsJSON: []byte(credsJSON),
		GCPProjectID:    gcpProjectID,
		VertexRegion:    vertexRegion,
		LLMModel:        llmModel,
		LookbackHours:   lookback,
		IssueLimit:      issueLimit,
		DryRun:          dryRun,
		CommentsOnly:    commentsOnly,
		Verbose:         flagVerbose,
	}, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
