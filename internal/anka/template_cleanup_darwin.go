//go:build darwin

package anka

import (
	"context"
	"fmt"

	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/logging"
)

// EnsureSpaceForTemplateOnDarwin ensures there's enough space for a new template by cleaning up LRU templates if needed
func (cli *Cli) EnsureSpaceForTemplateOnDarwin(
	workerCtx context.Context,
	pluginCtx context.Context,
	template, tag string,
) (error, error) { // ensureSpaceError, genericError
	var bytesFreed uint64
	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}
	pullJson, err := cli.AnkaExecutePullCommand(pluginCtx, template, "--tag", tag, "--check-download-size")
	if err != nil {
		return nil, err
	}
	if pullJson.Status != "OK" {
		return nil, fmt.Errorf("error checking template size: %+v", pullJson)
	}
	body, ok := pullJson.Body.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("unable to parse pull check output")
	}
	logging.Debug(pluginCtx, "pull check output", "pullJson", pullJson)
	if len(body) > 0 { // check that it's not empty {"status":"OK","code":0,"body":{},"message":"6c14r: available locally"}
		pullCheckOutput := &AnkaPullCheckOutput{}
		if body, ok := pullJson.Body.(map[string]any); ok {
			pullCheckOutput.Size = uint64(body["size"].(float64))
			pullCheckOutput.Cached = uint64(body["cached"].(float64))
			pullCheckOutput.Available = uint64(body["available"].(float64))
		} else {
			return nil, fmt.Errorf("unable to parse pull check output")
		}

		downloadSize := pullCheckOutput.Size - pullCheckOutput.Cached
		// Calculate the minimum available space required, considering the buffer as a percentage
		bufferSize := uint64(float64(pullCheckOutput.Available) * (pluginConfig.TemplateDiskBuffer / 100.0))
		bufferedAvailable := pullCheckOutput.Available - bufferSize

		if bufferedAvailable <= downloadSize {
			// if not enough space, try to free up space by deleting the LRU templates
			bytesToFree := downloadSize - bufferedAvailable

			logging.Warn(pluginCtx, "insufficient space for template, cleaning up LRU templates",
				"bufferedAvailable", bufferedAvailable,
				"downloadSize", downloadSize,
				"bytesToFree", bytesToFree,
			)

			// First, calculate total space that could potentially be freed
			lruTemplates := workerGlobals.TemplateTracker.GetLeastRecentlyUsedTemplates()
			var totalFreeable uint64
			for _, templateUsage := range lruTemplates {
				totalFreeable += templateUsage.ImageSize
			}

			logging.Debug(pluginCtx, "lru templates", "lruTemplates", lruTemplates, "totalFreeable", totalFreeable, "bytesToFree", bytesToFree)

			// If even deleting all LRU templates wouldn't free enough space, don't delete anything
			if totalFreeable <= bytesToFree {
				return fmt.Errorf("insufficient space on host even if we cleaned up all templates (need %d, can free %d, lruTemplateCount %d)", bytesToFree, totalFreeable, len(lruTemplates)), nil
			}

			// Now proceed with actual deletion since we know it's possible to free enough space
			for _, templateUsage := range lruTemplates {
				if bytesFreed >= bytesToFree {
					break
				}

				logging.Info(pluginCtx, "deleting LRU template to free space",
					"template", templateUsage.Template,
					"tag", templateUsage.Tag,
					"size", templateUsage.ImageSize,
					"lastUsed", templateUsage.LastUsed,
					"usageCount", templateUsage.UsageCount)

				err := cli.AnkaDeleteTemplate(pluginCtx, templateUsage.Template, templateUsage.Tag)
				if err != nil {
					logging.Error(pluginCtx, "failed to delete LRU template", "error", err)
					continue
				}

				// Template tracker is automatically updated by AnkaDeleteTemplate
				bytesFreed += templateUsage.ImageSize
			}

			logging.Info(pluginCtx, "successfully freed space for template", "bytesFreed", bytesFreed)

			// This should not happen since we pre-calculated; but safety check
			if bytesFreed < bytesToFree {
				return fmt.Errorf("unexpected: unable to free enough space for template (freed %d, needed %d)", bytesFreed, bytesToFree), nil
			}

		} else {
			logging.Debug(pluginCtx, "enough space on host to pull template", "available", bufferedAvailable, "downloadSize", downloadSize)
		}
	}

	return nil, nil
}
