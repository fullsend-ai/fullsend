package githubactions

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/e2e/behaviour/drivers/ci"
	"github.com/fullsend-ai/fullsend/internal/forge"
)

const (
	pollInterval   = 15 * time.Second
	dispatchWait   = 12 * time.Minute
	dispatchPoll   = 5 * time.Second
	dispatchMaxTry = 24

	artifactRunPoll = 5 * time.Second
	artifactRunWait = 5 * time.Minute

	agentWorkflowName = "Triage Agent"

	assertNoWorkflowChecks = 3
	assertNoWorkflowDelay  = 10 * time.Second
)

// Driver implements ci.Driver against GitHub Actions.
type Driver struct {
	Client forge.Client
	Token  string
}

func New(client forge.Client, token string) ci.Driver {
	return &Driver{Client: client, Token: token}
}

func (d *Driver) WaitForWorkflow(ctx context.Context, owner, repo, workflowFile string, after time.Time, event string) (*forge.WorkflowRun, error) {
	workflowFile = filepath.Base(workflowFile)
	var triageRun *forge.WorkflowRun
	for attempt := 0; attempt < dispatchMaxTry; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(dispatchPoll):
		}
		runs, err := d.Client.ListWorkflowRuns(ctx, owner, repo, workflowFile)
		if err != nil {
			continue
		}
		if candidate := selectWorkflowRun(runs, after, event); candidate != nil {
			if candidate.Status == "completed" && candidate.Conclusion != "success" {
				return nil, fmt.Errorf("workflow %s run %d concluded with %q during dispatch", workflowFile, candidate.ID, candidate.Conclusion)
			}
			triageRun = candidate
			break
		}
	}
	if triageRun == nil {
		if event != "" {
			return nil, fmt.Errorf("workflow %s (%s) was not dispatched", workflowFile, event)
		}
		return nil, fmt.Errorf("workflow %s was not dispatched", workflowFile)
	}

	deadline := time.Now().Add(dispatchWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
		run, err := d.Client.GetWorkflowRun(ctx, owner, repo, triageRun.ID)
		if err != nil {
			continue
		}
		if run.Status == "completed" {
			if run.Conclusion == "success" {
				return run, nil
			}
			if replacement := selectSuccessfulWorkflowRun(latestRuns(ctx, d, owner, repo, workflowFile), after, event); replacement != nil && replacement.ID > triageRun.ID {
				triageRun = replacement
				continue
			}
			return run, fmt.Errorf("workflow %s run %d concluded with %q", workflowFile, run.ID, run.Conclusion)
		}
	}
	return nil, fmt.Errorf("workflow %s run %d did not complete within deadline", workflowFile, triageRun.ID)
}

func latestRuns(ctx context.Context, d *Driver, owner, repo, workflowFile string) []forge.WorkflowRun {
	runs, err := d.Client.ListWorkflowRuns(ctx, owner, repo, workflowFile)
	if err != nil {
		return nil
	}
	return runs
}

// selectWorkflowRun returns the newest workflow run after triggerTime that matches
// the optional event filter. Callers decide how to handle non-success conclusions.
func selectWorkflowRun(runs []forge.WorkflowRun, triggerTime time.Time, event string) *forge.WorkflowRun {
	var best *forge.WorkflowRun
	for _, run := range runs {
		if !workflowRunMatches(run, triggerTime, event) {
			continue
		}
		if best == nil || run.ID > best.ID {
			r := run
			best = &r
		}
	}
	return best
}

func selectSuccessfulWorkflowRun(runs []forge.WorkflowRun, triggerTime time.Time, event string) *forge.WorkflowRun {
	var best *forge.WorkflowRun
	for _, run := range runs {
		if !workflowRunMatches(run, triggerTime, event) {
			continue
		}
		if run.Status != "completed" || run.Conclusion != "success" {
			continue
		}
		if best == nil || run.ID > best.ID {
			r := run
			best = &r
		}
	}
	return best
}

func workflowRunMatches(run forge.WorkflowRun, triggerTime time.Time, event string) bool {
	runTime, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
	if parseErr != nil || runTime.Before(triggerTime) {
		return false
	}
	if event != "" && run.Event != event {
		return false
	}
	return true
}

func (d *Driver) FindCompletedWorkflowRun(ctx context.Context, owner, repo, workflowFile string, after time.Time) (*forge.WorkflowRun, error) {
	workflowFile = filepath.Base(workflowFile)
	deadline := time.Now().Add(artifactRunWait)
	for time.Now().Before(deadline) {
		run, err := d.findCompletedWorkflowRunOnce(ctx, owner, repo, workflowFile, after)
		if err != nil {
			return nil, err
		}
		if run != nil {
			return run, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(artifactRunPoll):
		}
	}
	return nil, fmt.Errorf("no completed workflow run for %s after %s", workflowFile, after.Format(time.RFC3339))
}

func (d *Driver) findCompletedWorkflowRunOnce(ctx context.Context, owner, repo, workflowFile string, after time.Time) (*forge.WorkflowRun, error) {
	workflowFile = filepath.Base(workflowFile)
	for _, wf := range []string{workflowFile, ".github/workflows/" + workflowFile} {
		runs, err := d.Client.ListWorkflowRuns(ctx, owner, repo, wf)
		if err != nil {
			continue
		}
		if run := selectCompletedSuccessRun(runs, after); run != nil {
			return run, nil
		}
	}
	runs, err := d.Client.ListRecentWorkflowRuns(ctx, owner, repo, 30)
	if err != nil {
		return nil, err
	}
	return selectCompletedSuccessRunByName(runs, after, agentWorkflowName), nil
}

func selectCompletedSuccessRunByName(runs []forge.WorkflowRun, after time.Time, name string) *forge.WorkflowRun {
	var best *forge.WorkflowRun
	for _, run := range runs {
		if run.Name != name {
			continue
		}
		runTime, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
		if parseErr != nil || runTime.Before(after) {
			continue
		}
		if run.Status != "completed" || run.Conclusion != "success" {
			continue
		}
		if best == nil || run.ID > best.ID {
			r := run
			best = &r
		}
	}
	return best
}

func selectCompletedSuccessRun(runs []forge.WorkflowRun, after time.Time) *forge.WorkflowRun {
	var best *forge.WorkflowRun
	for _, run := range runs {
		runTime, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
		if parseErr != nil || runTime.Before(after) {
			continue
		}
		if run.Status != "completed" || run.Conclusion != "success" {
			continue
		}
		if best == nil || run.ID > best.ID {
			r := run
			best = &r
		}
	}
	return best
}

func (d *Driver) AssertNoWorkflow(ctx context.Context, owner, repo, workflowFile string, after time.Time) error {
	for attempt := 0; attempt < assertNoWorkflowChecks; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(assertNoWorkflowDelay):
			}
		}
		runs, err := d.Client.ListWorkflowRuns(ctx, owner, repo, workflowFile)
		if err != nil {
			return err
		}
		for _, run := range runs {
			runTime, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
			if parseErr != nil {
				continue
			}
			if !runTime.Before(after) {
				return fmt.Errorf("unexpected workflow run %d for %s", run.ID, workflowFile)
			}
		}
	}
	return nil
}

func (d *Driver) GetRunLogs(ctx context.Context, owner, repo string, runID int) (string, error) {
	return d.Client.GetWorkflowRunLogs(ctx, owner, repo, runID)
}

func (d *Driver) DownloadArtifacts(ctx context.Context, owner, repo string, runID int, destDir string) error {
	artifacts, err := d.Client.ListWorkflowRunArtifacts(ctx, owner, repo, runID)
	if err != nil {
		return err
	}
	for _, art := range artifacts {
		zipData, err := d.Client.DownloadWorkflowRunArtifact(ctx, owner, repo, art.ID)
		if err != nil {
			return err
		}
		if err := extractArtifactZip(art.Name, zipData, destDir); err != nil {
			return err
		}
	}
	return nil
}

func (d *Driver) DownloadNamedArtifactAfter(ctx context.Context, owner, repo, artifactName string, after time.Time, destDir string) error {
	deadline := time.Now().Add(artifactRunWait)
	for time.Now().Before(deadline) {
		arts, err := d.Client.ListRepositoryArtifacts(ctx, owner, repo, 100)
		if err != nil {
			return err
		}
		if art := selectRepositoryArtifactAfter(arts, artifactName, after); art != nil {
			zipData, err := d.Client.DownloadWorkflowRunArtifact(ctx, owner, repo, art.ID)
			if err != nil {
				return err
			}
			return extractArtifactZip(art.Name, zipData, destDir)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(artifactRunPoll):
		}
	}
	return fmt.Errorf("artifact %q not found after %s", artifactName, after.Format(time.RFC3339))
}

func selectRepositoryArtifactAfter(arts []forge.RepositoryArtifact, name string, after time.Time) *forge.RepositoryArtifact {
	var best *forge.RepositoryArtifact
	for _, art := range arts {
		if art.Name != name {
			continue
		}
		artTime, parseErr := time.Parse(time.RFC3339, art.CreatedAt)
		if parseErr != nil || artTime.Before(after) {
			continue
		}
		if best == nil || art.ID > best.ID {
			a := art
			best = &a
		}
	}
	return best
}

func extractArtifactZip(name string, zipData []byte, destDir string) error {
	safeName := filepath.Base(name)
	if safeName == "" || safeName == "." {
		safeName = "artifact"
	}
	artDir := filepath.Join(destDir, safeName)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		return err
	}

	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return fmt.Errorf("parse artifact zip %q: %w", safeName, err)
	}

	const perFileLimit = 10 << 20
	for _, f := range zr.File {
		if f.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("artifact zip %q contains symlink entry %q", safeName, f.Name)
		}
		outPath := filepath.Join(artDir, f.Name)
		if !strings.HasPrefix(filepath.Clean(outPath), filepath.Clean(artDir)+string(os.PathSeparator)) {
			continue
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(outPath, 0o755)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(outPath), 0o755)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		data, err := readLimited(rc, perFileLimit)
		rc.Close()
		if err != nil {
			return fmt.Errorf("read artifact entry %q: %w", f.Name, err)
		}
		if err := os.WriteFile(outPath, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("entry exceeds %d byte limit", limit)
	}
	return data, nil
}
