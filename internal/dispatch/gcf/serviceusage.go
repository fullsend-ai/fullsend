package gcf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/gcp"
)

// RequiredInferenceAPIs lists the GCP APIs that 'fullsend inference
// provision' needs enabled on the target project.
var RequiredInferenceAPIs = []string{
	"iam.googleapis.com",
	"cloudresourcemanager.googleapis.com",
	"aiplatform.googleapis.com",
}

// ServiceUsageClient abstracts the Service Usage API operations used to
// check and enable GCP APIs on a project. *LiveGCFClient implements it.
type ServiceUsageClient interface {
	// ServiceEnabled reports whether the given service (e.g.
	// "iam.googleapis.com") is enabled on the project.
	ServiceEnabled(ctx context.Context, projectID, service string) (bool, error)
	// EnableServices enables the given services on the project and waits
	// for the Service Usage operation to finish.
	EnableServices(ctx context.Context, projectID string, services []string) error
}

// serviceUsageOperationPattern validates Service Usage long-running
// operation names before they are interpolated into a polling URL.
var serviceUsageOperationPattern = regexp.MustCompile(`^operations/[A-Za-z0-9._:-]+$`)

// serviceNamePattern validates GCP service names (e.g. "iam.googleapis.com").
var serviceNamePattern = regexp.MustCompile(`^[a-z0-9-]+(\.[a-z0-9-]+)+$`)

// ServiceEnabled reports whether a GCP API is enabled on the project via
// the Service Usage API (services.get).
func (c *LiveGCFClient) ServiceEnabled(ctx context.Context, projectID, service string) (bool, error) {
	if !serviceNamePattern.MatchString(service) {
		return false, fmt.Errorf("invalid service name: %q", service)
	}
	reqURL := fmt.Sprintf("https://serviceusage.googleapis.com/v1/projects/%s/services/%s",
		url.PathEscape(projectID), url.PathEscape(service))

	resp, err := c.Client.DoRequest(ctx, http.MethodGet, reqURL, "")
	if err != nil {
		return false, fmt.Errorf("checking service %s: %w", service, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return false, fmt.Errorf("unexpected status %d checking service %s: %s",
			resp.StatusCode, service, gcp.ExtractErrorMessage(body))
	}

	var result struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("decoding service %s state: %w", service, err)
	}
	return result.State == "ENABLED", nil
}

// EnableServices enables the given GCP APIs on the project via the Service
// Usage API (services.batchEnable) and waits for the operation to finish.
func (c *LiveGCFClient) EnableServices(ctx context.Context, projectID string, services []string) error {
	if len(services) == 0 {
		return nil
	}
	for _, s := range services {
		if !serviceNamePattern.MatchString(s) {
			return fmt.Errorf("invalid service name: %q", s)
		}
	}
	reqURL := fmt.Sprintf("https://serviceusage.googleapis.com/v1/projects/%s/services:batchEnable",
		url.PathEscape(projectID))
	payload, err := json.Marshal(map[string][]string{"serviceIds": services})
	if err != nil {
		return fmt.Errorf("marshaling enable request: %w", err)
	}

	resp, err := c.Client.DoRequest(ctx, http.MethodPost, reqURL, string(payload))
	if err != nil {
		return fmt.Errorf("enabling services: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("unexpected status %d enabling services: %s",
			resp.StatusCode, gcp.ExtractErrorMessage(body))
	}

	return c.waitForServiceUsageOperation(ctx, resp.Body)
}

// waitForServiceUsageOperation parses a Service Usage long-running
// operation and polls it until done.
func (c *LiveGCFClient) waitForServiceUsageOperation(ctx context.Context, body io.Reader) error {
	var op struct {
		Name  string `json:"name"`
		Done  bool   `json:"done"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(body).Decode(&op); err != nil {
		return fmt.Errorf("decoding operation response: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	delay := c.getPollDelay()
	for {
		if op.Done {
			if op.Error != nil {
				return fmt.Errorf("operation failed: %s", op.Error.Message)
			}
			return nil
		}
		if !serviceUsageOperationPattern.MatchString(op.Name) {
			return fmt.Errorf("invalid operation name format: %q", op.Name)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-delay(2 * time.Second):
		}

		reqURL := "https://serviceusage.googleapis.com/v1/" + op.Name
		resp, err := c.Client.DoRequest(ctx, http.MethodGet, reqURL, "")
		if err != nil {
			return fmt.Errorf("polling operation: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			return fmt.Errorf("unexpected status %d polling operation: %s",
				resp.StatusCode, gcp.ExtractErrorMessage(b))
		}
		name := op.Name
		op.Error = nil
		decodeErr := json.NewDecoder(resp.Body).Decode(&op)
		resp.Body.Close()
		if decodeErr != nil {
			return fmt.Errorf("decoding operation status: %w", decodeErr)
		}
		if op.Name == "" {
			op.Name = name
		}
	}
}

// IsAPINotEnabledError reports whether err looks like a GCP "API has not
// been used in project ... or it is disabled" (SERVICE_DISABLED) error.
// Provisioner errors are wrapped strings, so this matches on the message.
func IsAPINotEnabledError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SERVICE_DISABLED") ||
		strings.Contains(msg, "has not been used in project") ||
		strings.Contains(msg, "it is disabled. Enable it")
}
