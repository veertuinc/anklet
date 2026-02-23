package github

import (
	"context"
	"encoding/json"
	"fmt"

	internalModels "github.com/veertuinc/anklet/internal/models"
	internalPluginGlobals "github.com/veertuinc/anklet/internal/plugin"
)

type PluginGlobals = internalPluginGlobals.PluginGlobals
type QueueJob = internalModels.QueueJob

type Repository struct {
	Name       *string `json:"name"`
	Owner      *string `json:"owner"`
	Visibility *string `json:"visibility"`
	Private    *bool   `json:"private"`
}

func (r *Repository) UnmarshalJSON(data []byte) error {
	type repositoryRaw struct {
		Name       *string         `json:"name"`
		Owner      json.RawMessage `json:"owner"`
		Visibility *string         `json:"visibility"`
		Private    *bool           `json:"private"`
	}
	var raw repositoryRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.Name = raw.Name
	r.Visibility = raw.Visibility
	r.Private = raw.Private
	if raw.Owner != nil && string(raw.Owner) != "null" {
		var s string
		if err := json.Unmarshal(raw.Owner, &s); err == nil {
			r.Owner = &s
			return nil
		}
		var obj struct {
			Login *string `json:"login"`
		}
		if err := json.Unmarshal(raw.Owner, &obj); err != nil {
			return fmt.Errorf("cannot unmarshal repository owner: %w", err)
		}
		r.Owner = obj.Login
	}
	return nil
}

type SimplifiedJob = internalModels.Job

type WorkflowRunJobDetail struct {
	JobID           int64
	JobName         string
	JobURL          string
	WorkflowName    string
	AnkaTemplate    string
	AnkaTemplateTag string
	RunID           int64
	Labels          []string
	Repo            string
	Conclusion      string
}

func GetPluginGlobalsFromContext(ctx context.Context) (*PluginGlobals, error) {
	return internalPluginGlobals.GetPluginGlobalsFromContext(ctx)
}
