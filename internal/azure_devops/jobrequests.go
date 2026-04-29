package azure_devops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Demand is one (name, value) capability requirement attached to a job.
// Anklet uses demands to discover which Anka template a job needs without
// running the agent first - the YAML pool.demands block compiles into the
// runtime job request and the handler reads it from there.
//
// Value is empty for "exists" style demands (the YAML "- some_capability"
// short form). Anklet treats an empty value on a required demand (like
// anka-template) as missing and refuses to provision a VM for it.
type Demand struct {
	Name  string
	Value string
}

// JobRequest is the subset of TaskAgentJobRequest Anklet needs: enough to
// match the request against our queued job and read its demands.
type JobRequest struct {
	RequestID int
	JobID     string
	Demands   []Demand
}

// DemandValue returns the value of the first demand whose Name matches.
// The boolean reports whether such a demand was present at all so callers
// can distinguish "missing" from "present but empty".
func (r *JobRequest) DemandValue(name string) (string, bool) {
	if r == nil {
		return "", false
	}
	for _, d := range r.Demands {
		if d.Name == name {
			return d.Value, true
		}
	}
	return "", false
}

// jobRequestsEnvelope mirrors the standard ADO list-response envelope.
type jobRequestsEnvelope struct {
	Count int                 `json:"count"`
	Value []jobRequestPayload `json:"value"`
}

// jobRequestPayload is the on-the-wire shape we unmarshal into. We keep
// Demands as []json.RawMessage so we can decide per-entry whether each
// item is an object or a string at parse time.
type jobRequestPayload struct {
	RequestID int               `json:"requestId"`
	JobID     string            `json:"jobId"`
	Demands   []json.RawMessage `json:"demands"`
}

// FindJobRequest returns the most recent job request (highest RequestID)
// matching jobID, or nil if no match exists. ADO creates a new request for
// each retry but reuses the JobID, and the agent runtime always picks the
// latest one - we mirror that here.
func FindJobRequest(reqs []JobRequest, jobID string) *JobRequest {
	var match *JobRequest
	for i := range reqs {
		if !strings.EqualFold(reqs[i].JobID, jobID) {
			continue
		}
		if match == nil || reqs[i].RequestID > match.RequestID {
			match = &reqs[i]
		}
	}
	return match
}

// ListJobRequests fetches the job requests currently sitting in the given
// agent pool. This endpoint is the only ADO surface that returns runtime
// pool.demands today: Build.Demands is server-side broken for YAML
// pipelines (https://developercommunity.visualstudio.com/t/Demands-object-missing-from-buildsget-a/11024369),
// and the Go SDK doesn't expose this resource as a typed method, so we
// issue the request via raw HTTP using the same auth header the SDK uses.
//
// Endpoint reference:
//
//	https://learn.microsoft.com/en-us/rest/api/azure/devops/distributedtask/agent-requests/list
func (c *Client) ListJobRequests(ctx context.Context, poolID int) ([]JobRequest, error) {
	base := strings.TrimRight(c.conn.BaseUrl, "/")
	endpoint := fmt.Sprintf("%s/_apis/distributedtask/pools/%d/jobrequests", base, poolID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("building job-requests request: %w", err)
	}
	q := url.Values{}
	q.Set("api-version", "7.1-preview.1")
	req.URL.RawQuery = q.Encode()
	if c.conn.AuthorizationString != "" {
		req.Header.Set("Authorization", c.conn.AuthorizationString)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching job requests: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("listing job requests for pool %d: HTTP %d: %s",
			poolID, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var env jobRequestsEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decoding job requests body: %w", err)
	}

	out := make([]JobRequest, 0, len(env.Value))
	for _, p := range env.Value {
		out = append(out, JobRequest{
			RequestID: p.RequestID,
			JobID:     p.JobID,
			Demands:   parseRawDemands(p.Demands),
		})
	}
	return out, nil
}

// parseRawDemands accepts the per-entry JSON tokens and normalizes them
// into typed Demand values. Each entry is either:
//
//   - object form: {"name": "anka-template", "value": "<uuid>"}
//   - string form: "anka-template -equals <uuid>"   (legacy / on-prem)
//
// Malformed entries are dropped silently; they shouldn't block resolution
// of other demands on the same job.
func parseRawDemands(raw []json.RawMessage) []Demand {
	if len(raw) == 0 {
		return nil
	}
	out := make([]Demand, 0, len(raw))
	for _, item := range raw {
		trimmed := bytes.TrimSpace(item)
		if len(trimmed) == 0 {
			continue
		}
		switch trimmed[0] {
		case '{':
			var obj struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			}
			if err := json.Unmarshal(item, &obj); err != nil || obj.Name == "" {
				continue
			}
			out = append(out, Demand{Name: obj.Name, Value: obj.Value})
		case '"':
			var s string
			if err := json.Unmarshal(item, &s); err != nil || s == "" {
				continue
			}
			out = append(out, parseDemandString(s))
		}
	}
	return out
}

// parseDemandString splits the legacy "name -equals value" / bare-name
// format into a Demand. ADO supports operators other than -equals
// (-exists, -gtVersion, etc.) but Anklet only ever consumes equality
// demands, so anything else is folded into Value as-is and the caller
// decides what to do with it.
func parseDemandString(s string) Demand {
	const equalsOp = " -equals "
	if i := strings.Index(s, equalsOp); i >= 0 {
		return Demand{
			Name:  strings.TrimSpace(s[:i]),
			Value: strings.TrimSpace(s[i+len(equalsOp):]),
		}
	}
	return Demand{Name: strings.TrimSpace(s)}
}
