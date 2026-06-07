package githubactions

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	dispatchMaxTry = 12
)

// Driver implements ci.Driver against GitHub Actions.
type Driver struct {
	Client forge.Client
	Token  string
}

func New(client forge.Client, token string) ci.Driver {
	return &Driver{Client: client, Token: token}
}

func (d *Driver) WaitForWorkflow(ctx context.Context, owner, repo, workflowFile string, after time.Time) (*forge.WorkflowRun, error) {
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
		for _, run := range runs {
			runTime, parseErr := time.Parse(time.RFC3339, run.CreatedAt)
			if parseErr != nil || runTime.Before(after) {
				continue
			}
			r := run
			triageRun = &r
			break
		}
		if triageRun != nil {
			break
		}
	}
	if triageRun == nil {
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
			if run.Conclusion != "success" {
				return run, fmt.Errorf("workflow %s run %d concluded with %q", workflowFile, run.ID, run.Conclusion)
			}
			return run, nil
		}
	}
	return nil, fmt.Errorf("workflow %s run %d did not complete within deadline", workflowFile, triageRun.ID)
}

func (d *Driver) AssertNoWorkflow(ctx context.Context, owner, repo, workflowFile string, after time.Time) error {
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
	return nil
}

func (d *Driver) GetRunLogs(ctx context.Context, owner, repo string, runID int) (string, error) {
	return d.Client.GetWorkflowRunLogs(ctx, owner, repo, runID)
}

func (d *Driver) DownloadArtifacts(ctx context.Context, owner, repo string, runID int, destDir string) error {
	listURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/runs/%d/artifacts", owner, repo, runID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+d.Token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("list artifacts returned HTTP %d", resp.StatusCode)
	}

	var result struct {
		Artifacts []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"artifacts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}

	for _, art := range result.Artifacts {
		if err := downloadArtifact(ctx, d.Token, owner, repo, art.ID, art.Name, destDir); err != nil {
			return err
		}
	}
	return nil
}

func downloadArtifact(ctx context.Context, token, owner, repo string, artifactID int, name, destDir string) error {
	dlURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/actions/artifacts/%d/zip", owner, repo, artifactID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download artifact %s returned HTTP %d", name, resp.StatusCode)
	}

	zipData, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if err != nil {
		return err
	}

	artDir := filepath.Join(destDir, name)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		return err
	}

	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		rawPath := filepath.Join(destDir, name+".bin")
		return os.WriteFile(rawPath, zipData, 0o644)
	}

	for _, f := range zr.File {
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
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			continue
		}
		if err := os.WriteFile(outPath, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// FindBehaviourResults locates behaviour-results.json in downloaded artifacts.
func FindBehaviourResults(artifactRoot string) ([]byte, error) {
	var found []byte
	err := filepath.WalkDir(artifactRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Base(path) == "behaviour-results.json" {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			found = data
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("behaviour-results.json not found under %s", artifactRoot)
	}
	return found, nil
}

// FindOutputFile searches artifact downloads for a sandbox output file by name.
func FindOutputFile(artifactRoot, fileName string) ([]byte, error) {
	var found []byte
	err := filepath.WalkDir(artifactRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Base(path) == fileName {
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			found = data
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, fmt.Errorf("%s not found under %s", fileName, artifactRoot)
	}
	return found, nil
}
