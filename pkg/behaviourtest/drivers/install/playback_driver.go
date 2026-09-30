package install

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/fullsend-ai/fullsend/internal/e2etest"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/layers"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/drivers/install/common"
)

// PlaybackDriver implements Driver for playback behaviour tests. Each
// AllocateRepo call creates a fresh repo, installs fullsend via
// `repos install`, and waits for CI to index the workflow. Each
// DeallocateRepo deletes the repo. There is no pool or reuse — every
// scenario gets a clean repo.
type PlaybackDriver struct {
	org          string
	forgeName    string
	client       forge.Client
	token        string
	binary       string
	gcpProjectID string
	logf         func(string, ...any)
	runCLI       CLIRunnerFunc
	settle       SettleFunc

	mu           sync.Mutex
	outstanding  map[string]struct{}
	nextRepoHint string
}

// NewPlaybackDriver creates a playback driver for the given org and forge.
// forgeName is "github" or "gitlab".
func NewPlaybackDriver(
	org, forgeName string,
	client forge.Client,
	token, binary, gcpProjectID string,
	logf func(string, ...any),
) *PlaybackDriver {
	var settle SettleFunc
	if forgeName == "github" {
		settle = awaitWorkflowReady
	}
	return &PlaybackDriver{
		org:          org,
		forgeName:    forgeName,
		client:       client,
		token:        token,
		binary:       binary,
		gcpProjectID: gcpProjectID,
		logf:         logf,
		runCLI:       e2etest.TryRunCLI,
		settle:       settle,
		outstanding:  make(map[string]struct{}),
	}
}

// SetRepoHint sets a human-readable hint included in the next repo
// name. The value is consumed (cleared) by AllocateRepo.
func (d *PlaybackDriver) SetRepoHint(hint string) {
	d.mu.Lock()
	d.nextRepoHint = hint
	d.mu.Unlock()
}

func (d *PlaybackDriver) AllocateRepo(ctx context.Context) (string, error) {
	d.mu.Lock()
	hint := d.nextRepoHint
	d.nextRepoHint = ""
	d.mu.Unlock()

	suffix := uuid.New().String()[:8]
	name := "playback-" + suffix
	if hint != "" {
		name = "playback-" + suffix + "-" + hint
	}
	target := d.org + "/" + name

	d.logf("[playback] creating repo %s", target)
	if _, err := d.client.CreateRepo(ctx, d.org, name, "Playback behaviour test", false); err != nil {
		return "", fmt.Errorf("creating repo %s: %w", target, err)
	}

	var commentAPIPath string
	if playbackRuntime() == "dummy-playback" {
		d.logf("[playback] creating tracking issue on %s", target)
		issue, err := d.client.CreateIssue(ctx, d.org, name, "Playback tracking", "Internal issue for playback counter tracking")
		if err != nil {
			d.deleteRepo(ctx, name)
			return "", fmt.Errorf("creating tracking issue on %s: %w", target, err)
		}
		comment, err := d.client.CreateIssueComment(ctx, d.org, name, issue.Number, "playback-current: 1")
		if err != nil {
			d.deleteRepo(ctx, name)
			return "", fmt.Errorf("creating tracking comment on %s: %w", target, err)
		}
		commentAPIPath = d.trackingCommentRef(target, issue.Number, comment.ID)
	}

	d.logf("[playback] provisioning inference for %s", target)
	_, err := common.ProvisionInference(d.binary, d.token, target, d.gcpProjectID, d.runCLI, d.logf)
	if err != nil {
		d.deleteRepo(ctx, name)
		return "", fmt.Errorf("inference provision %s: %w", target, err)
	}

	d.logf("[playback] running repos install on %s", target)
	tmpDir, err := os.MkdirTemp("", "playback-manifest-*")
	if err != nil {
		d.deprovisionInference(name)
		d.deleteRepo(ctx, name)
		return "", fmt.Errorf("creating temp dir for manifest: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	manifestPath := tmpDir + "/repos.yaml"

	if d.forgeName == "gitlab" {
		setDefaults := [][]string{
			{"repos", "set-default", "gitlab.url", d.gitlabURL(), "-f", manifestPath},
			{"repos", "set-default", "gitlab.runner_tags", d.gitlabRunnerTags(), "-f", manifestPath},
		}
		for _, sdArgs := range setDefaults {
			d.logf("[playback] fullsend %s", strings.Join(sdArgs, " "))
			if _, err := d.runCLI(d.binary, d.token, sdArgs...); err != nil {
				d.deprovisionInference(name)
				d.deleteRepo(ctx, name)
				return "", fmt.Errorf("repos %s: %w", strings.Join(sdArgs[1:4], " "), err)
			}
		}
	}

	args := []string{
		"repos", "install", target,
		"--forge", d.forgeName,
		"--direct",
		"--runtime", playbackRuntime(),
		"--inference-project", d.gcpProjectID,
		"-f", manifestPath,
	}
	if d.forgeName == "gitlab" {
		args = append(args, "--gitlab-bot-token", d.token)
	}
	d.logf("[playback] fullsend %s", redactTokenArgs(args))
	if _, err := d.runCLI(d.binary, d.token, args...); err != nil {
		d.deprovisionInference(name)
		d.deleteRepo(ctx, name)
		return "", fmt.Errorf("repos install %s: %w", target, err)
	}

	d.logf("[playback] vendoring local binary into %s", target)
	if err := layers.VendorBinary(ctx, d.client, d.org, name,
		layers.VendoredBinaryPathPerRepo, d.binary,
		"chore: vendor local fullsend binary for playback test [skip ci]"); err != nil {
		d.deprovisionInference(name)
		d.deleteRepo(ctx, name)
		return "", fmt.Errorf("vendoring binary into %s: %w", target, err)
	}

	if commentAPIPath != "" {
		d.logf("[playback] committing tracking comment path to %s", target)
		if _, err := d.client.CommitFiles(ctx, d.org, name,
			"chore: store playback tracking comment [skip ci]",
			[]forge.TreeFile{{Path: ".fullsend/playback-comment-url", Content: []byte(commentAPIPath), Mode: "100644"}}); err != nil {
			d.deprovisionInference(name)
			d.deleteRepo(ctx, name)
			return "", fmt.Errorf("committing tracking comment path to %s: %w", target, err)
		}
	}

	if d.settle != nil {
		d.logf("[playback] waiting for CI to index workflow on %s", target)
		if err := d.settle(ctx, d.client, d.org, name, PerRepoTriageWorkflow, d.logf); err != nil {
			d.deprovisionInference(name)
			d.deleteRepo(ctx, name)
			return "", fmt.Errorf("waiting for CI on %s: %w", target, err)
		}
	}

	d.mu.Lock()
	d.outstanding[name] = struct{}{}
	d.mu.Unlock()

	d.logf("[playback] allocated %s", target)
	return name, nil
}

// trackingCommentRef returns a two-line "<cli>\n<api-path>" string that
// the dummy-playback runtime uses to read/update the tracking comment.
// GitHub: gh + /repos/{owner/repo}/issues/comments/{id}
// GitLab: glab + /projects/{encoded}/issues/{iid}/notes/{noteId}
func (d *PlaybackDriver) trackingCommentRef(target string, issueNumber, commentID int) string {
	switch d.forgeName {
	case "gitlab":
		proj := url.PathEscape(target)
		return fmt.Sprintf("glab\n/projects/%s/issues/%d/notes/%d", proj, issueNumber, commentID)
	default:
		return fmt.Sprintf("gh\n/repos/%s/issues/comments/%d", target, commentID)
	}
}

// InstalledBotToken reads the FULLSEND_FORGE_TOKEN CI/CD variable from
// the project, returning the bot token created during repos install.
func (d *PlaybackDriver) InstalledBotToken(ctx context.Context, repoName string) (string, error) {
	val, ok, err := d.client.GetRepoVariable(ctx, d.org, repoName, "FULLSEND_FORGE_TOKEN")
	if err != nil {
		return "", fmt.Errorf("reading FULLSEND_FORGE_TOKEN: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("FULLSEND_FORGE_TOKEN not found on %s/%s", d.org, repoName)
	}
	return val, nil
}

func (d *PlaybackDriver) DeallocateRepo(ctx context.Context, repoName string) error {
	d.mu.Lock()
	if _, ok := d.outstanding[repoName]; !ok {
		d.mu.Unlock()
		return fmt.Errorf("DeallocateRepo: %q is not an outstanding lease", repoName)
	}
	delete(d.outstanding, repoName)
	d.mu.Unlock()

	if playbackKeepRepos() {
		d.logf("[playback] PLAYBACK_KEEP_REPOS set — keeping %s/%s", d.org, repoName)
		return nil
	}

	d.deprovisionInference(repoName)
	d.deleteRepo(ctx, repoName)
	d.logf("[playback] deallocated %s/%s", d.org, repoName)
	return nil
}

func (d *PlaybackDriver) Finalize(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.outstanding) == 0 {
		return nil
	}

	leaked := make([]string, 0, len(d.outstanding))
	for name := range d.outstanding {
		leaked = append(leaked, name)
	}
	if playbackKeepRepos() {
		d.logf("[playback] PLAYBACK_KEEP_REPOS set — keeping %d leaked repo(s): %v", len(leaked), leaked)
		return nil
	}

	d.logf("[playback] Finalize: cleaning up %d leaked repo(s): %v", len(leaked), leaked)

	for _, name := range leaked {
		d.deprovisionInference(name)
		d.deleteRepo(ctx, name)
		delete(d.outstanding, name)
	}

	return fmt.Errorf("Finalize: %d leaked repo(s): %v", len(leaked), leaked)
}

func (d *PlaybackDriver) Capacity() int { return DefaultPoolSize }

func (d *PlaybackDriver) deprovisionInference(repoName string) {
	target := d.org + "/" + repoName
	args := []string{"inference", "deprovision", target, "--project", d.gcpProjectID}
	d.logf("[playback] fullsend %s", strings.Join(args, " "))
	if _, err := d.runCLI(d.binary, d.token, args...); err != nil {
		d.logf("[playback] failed to deprovision inference for %s: %v", target, err)
	}
}

func (d *PlaybackDriver) deleteRepo(ctx context.Context, name string) {
	if err := d.client.DeleteRepo(ctx, d.org, name); err != nil && !forge.IsNotFound(err) {
		d.logf("[playback] failed to delete %s/%s: %v", d.org, name, err)
	}
}

func (d *PlaybackDriver) gitlabURL() string {
	if u := os.Getenv("GITLAB_BASE_URL"); u != "" {
		return u
	}
	return "https://gitlab.com"
}

func (d *PlaybackDriver) gitlabRunnerTags() string {
	if t := os.Getenv("GITLAB_RUNNER_TAGS"); t != "" {
		return t
	}
	return "fullsend-gitlab-runner"
}

func playbackRuntime() string {
	if rt := os.Getenv("PLAYBACK_RUNTIME"); rt != "" {
		return rt
	}
	return "dummy-playback"
}

func playbackKeepRepos() bool {
	v, _ := strconv.ParseBool(os.Getenv("PLAYBACK_KEEP_REPOS"))
	return v
}

func redactTokenArgs(args []string) string {
	redacted := make([]string, len(args))
	copy(redacted, args)
	for i, a := range redacted {
		if (a == "--gitlab-bot-token" || a == "--token") && i+1 < len(redacted) {
			redacted[i+1] = "REDACTED"
		}
	}
	return strings.Join(redacted, " ")
}

var _ Driver = (*PlaybackDriver)(nil)
