// Package schedulerclient is a minimal HTTP client for the anna scheduler REST API.
package schedulerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/vaayne/anna/internal/config"
)

// Job is the scheduler job representation returned by the API.
type Job struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Message     string `json:"message"`
	Cron        string `json:"cron,omitempty"`
	Every       string `json:"every,omitempty"`
	At          string `json:"at,omitempty"`
	SessionMode string `json:"session_mode"`
	Enabled     bool   `json:"enabled"`
	AgentID     string `json:"agent_id,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	LastRunAt   string `json:"last_run_at,omitempty"`
	LastError   string `json:"last_error,omitempty"`
}

// CreateJobRequest is the body for POST /api/scheduler/jobs.
type CreateJobRequest struct {
	Name        string `json:"name"`
	Message     string `json:"message"`
	Cron        string `json:"cron,omitempty"`
	Every       string `json:"every,omitempty"`
	At          string `json:"at,omitempty"`
	SessionMode string `json:"session_mode,omitempty"`
	Enabled     bool   `json:"enabled"`
	AgentID     string `json:"agent_id,omitempty"`
}

// Client is a minimal HTTP client for the scheduler API.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// NewFromEnv builds a Client pointed at config.ServerURL() authenticated
// with ANNA_TOKEN. Returns an error if ANNA_TOKEN is unset.
func NewFromEnv() (*Client, error) {
	token := os.Getenv("ANNA_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("ANNA_TOKEN env var is required (run 'anna serve' and set ANNA_TOKEN to a generated token)")
	}
	return &Client{base: config.ServerURL(), token: token, http: &http.Client{}}, nil
}

// ListJobs calls GET /api/scheduler/jobs and returns all visible jobs.
func (c *Client) ListJobs(ctx context.Context) ([]Job, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/scheduler/jobs", nil)
	if err != nil {
		return nil, err
	}
	c.auth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck
	var out []Job
	if err := decodeData(resp, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateJob calls POST /api/scheduler/jobs and returns the created job.
func (c *Client) CreateJob(ctx context.Context, body CreateJobRequest) (Job, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return Job{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/scheduler/jobs", bytes.NewReader(data))
	if err != nil {
		return Job{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.auth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return Job{}, err
	}
	defer resp.Body.Close() //nolint:errcheck
	var job Job
	if err := decodeData(resp, &job); err != nil {
		return Job{}, err
	}
	return job, nil
}

// DeleteJob calls DELETE /api/scheduler/jobs/{id}.
func (c *Client) DeleteJob(ctx context.Context, id string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.base+"/api/scheduler/jobs/"+id, nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	return decodeData(resp, nil)
}

func (c *Client) auth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
}

// decodeData reads `{"data": ...}` on success and `{"error": "..."}` on failure.
func decodeData(resp *http.Response, out any) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		var apiErr struct {
			Error string `json:"error"`
		}
		if jerr := json.Unmarshal(body, &apiErr); jerr == nil && apiErr.Error != "" {
			return fmt.Errorf("anna server %d: %s", resp.StatusCode, apiErr.Error)
		}
		snippet := strings.TrimSpace(string(body))
		if len(snippet) > 200 {
			snippet = snippet[:200] + "…"
		}
		if snippet == "" {
			return fmt.Errorf("anna server returned %d", resp.StatusCode)
		}
		return fmt.Errorf("anna server %d: %s", resp.StatusCode, snippet)
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return json.Unmarshal(envelope.Data, out)
}
