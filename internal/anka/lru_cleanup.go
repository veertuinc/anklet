//go:build darwin

package anka

import (
	"context"
	"fmt"

	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/host"
	"github.com/veertuinc/anklet/internal/logging"
)

// EnsureSpaceForTemplateOnDarwin ensures there's enough space for a new template by cleaning up LRU templates if needed
func (cli *Cli) EnsureSpaceForTemplateOnDarwin(
	workerCtx context.Context,
	pluginCtx context.Context,
	templateUUID, tag string,
) (error, error) { // ensureSpaceError, genericError
	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}

	var originalBufferedAvailable uint64
	var originalBufferSize uint64

	// Helper function to check download size and available space
	checkSpace := func() (*AnkaPullCheckOutput, uint64, uint64, error) {
		pullJson, err := cli.AnkaExecutePullCommand(pluginCtx, templateUUID, "--tag", tag, "--check-download-size")
		if err != nil {
			return nil, 0, 0, err
		}
		if pullJson.Status != "OK" {
			return nil, 0, 0, fmt.Errorf("error checking template size: %+v", pullJson)
		}
		pullCheckOutput := &AnkaPullCheckOutput{}
		if body, ok := pullJson.Body.(map[string]any); ok {
			pullCheckOutput.Size = uint64(body["size"].(float64))
			pullCheckOutput.Cached = uint64(body["cached"].(float64))
			pullCheckOutput.Available = uint64(body["available"].(float64))
		} else {
			return nil, 0, 0, fmt.Errorf("unable to parse pull check output")
		}

		downloadSize := pullCheckOutput.Size - pullCheckOutput.Cached

		var bufferedAvailable uint64
		if originalBufferedAvailable == 0 {
			// First time - calculate buffer based on original available space
			originalBufferSize = uint64(float64(pullCheckOutput.Available) * (pluginConfig.TemplateDiskBuffer / 100.0))
			originalBufferedAvailable = pullCheckOutput.Available - originalBufferSize
			bufferedAvailable = originalBufferedAvailable
		} else {
			// Subsequent calls - use original buffer threshold, just add freed space
			bufferedAvailable = originalBufferedAvailable + (pullCheckOutput.Available - (originalBufferedAvailable + originalBufferSize))
		}

		logging.Debug(pluginCtx, "pull check output", "pullCheckOutput", map[string]any{
			"pullCheckOutput.Size":      pullCheckOutput.Size,
			"pullCheckOutput.Cached":    pullCheckOutput.Cached,
			"pullCheckOutput.Available": pullCheckOutput.Available,
			"downloadSize":              downloadSize,
			"originalBufferSize":        originalBufferSize,
			"originalBufferedAvailable": originalBufferedAvailable,
			"bufferedAvailable":         bufferedAvailable,
		})

		return pullCheckOutput, downloadSize, bufferedAvailable, nil
	}

	pullCheckOutput, downloadSize, bufferedAvailable, err := checkSpace()
	if err != nil {
		return nil, err
	}

	if pullCheckOutput.Size > 0 { // check that it's not empty {"status":"OK","code":0,"body":{},"message":"6c14r: available locally"}

		// if the template is just too big for the host, fail
		hostDiskSizeBytes, err := host.GetHostDiskSizeBytes(pluginCtx)
		if err != nil {
			return nil, err
		}
		if pullCheckOutput.Size > hostDiskSizeBytes {
			return fmt.Errorf("template is too big for the host (need %d, available %d)", pullCheckOutput.Size, hostDiskSizeBytes), nil
		}

		if bufferedAvailable <= downloadSize {
			// if not enough space, try to free up space by deleting the LRU templates
			bytesToFree := downloadSize - bufferedAvailable

			logging.Warn(pluginCtx, "insufficient space for template, cleaning up LRU templates",
				"bufferedAvailable", bufferedAvailable,
				"downloadSize", downloadSize,
				"bytesToFree", bytesToFree,
			)

			// Get LRU templates to delete
			lruTemplates := workerGlobals.TemplateTracker.GetLeastRecentlyUsedTemplates()

			if len(lruTemplates) == 0 {
				return fmt.Errorf("insufficient space on host and no templates available for cleanup (need %d, available %d)", bytesToFree, bufferedAvailable), nil
			}

			var totalFreeable uint64
			for _, lruTemplate := range lruTemplates {
				// Skip deleting the template if it matches the one being pulled
				if lruTemplate.UUID == templateUUID {
					continue
				}
				totalFreeable += lruTemplate.ImageSize
			}

			logging.Debug(pluginCtx, "lru templates", "lruTemplates", lruTemplates, "totalFreeable", totalFreeable, "bytesToFree", bytesToFree)

			// If even deleting all LRU templates wouldn't free enough space, don't delete anything
			afterFreeingBytes := (originalBufferedAvailable + totalFreeable)
			logging.Debug(pluginCtx, "adjusted available space", "afterFreeingBytes", afterFreeingBytes, "downloadSize", downloadSize)
			if afterFreeingBytes <= bytesToFree {
				return fmt.Errorf("insufficient space on host even if we cleaned up all templates (need %d, can free %d, lruTemplateCount %d)", bytesToFree, totalFreeable, len(lruTemplates)), nil
			}

			// Delete templates one by one and re-check space after each deletion
			templatesDeleted := 0
			for _, lruTemplate := range lruTemplates {
				// Skip deleting the template if it matches the one being pulled
				if lruTemplate.UUID == templateUUID {
					continue
				}
				logging.Info(pluginCtx, "deleting LRU template to free space",
					"lruTemplate", map[string]any{
						"uuid":         lruTemplate.UUID,
						"name":         lruTemplate.Name,
						"tag":          lruTemplate.Tag,
						"size":         lruTemplate.ImageSize,
						"lastAccessed": lruTemplate.LastAccessed,
						"usageCount":   lruTemplate.UsageCount,
					},
				)

				err := cli.AnkaDeleteTemplate(pluginCtx, lruTemplate.UUID)
				if err != nil {
					logging.Error(pluginCtx, "failed to delete LRU template", "error", err)
					continue
				}
				templatesDeleted++

				// Template tracker is automatically updated by AnkaDeleteTemplate

				// Re-check available space after deletion
				_, newDownloadSize, newBufferedAvailable, err := checkSpace()
				if err != nil {
					logging.Error(pluginCtx, "failed to re-check space after template deletion", "error", err)
					continue
				}

				logging.Debug(pluginCtx, "space after template deletion",
					"templatesDeleted", templatesDeleted,
					"newBufferedAvailable", newBufferedAvailable,
					"newDownloadSize", newDownloadSize)

				// Check if we now have enough space
				if newBufferedAvailable > newDownloadSize {
					logging.Info(pluginCtx, "successfully freed enough space for template",
						"templatesDeleted", templatesDeleted,
						"bufferedAvailable", newBufferedAvailable,
						"downloadSize", newDownloadSize)
					break
				}
			}

			// Final check to ensure we have enough space
			_, finalDownloadSize, finalBufferedAvailable, err := checkSpace()
			if err != nil {
				return nil, err
			}

			// this should not happen, but just in case
			if finalBufferedAvailable <= finalDownloadSize {
				return fmt.Errorf("insufficient space on host even after cleaning up %d templates (need %d, available %d)", templatesDeleted, finalDownloadSize-finalBufferedAvailable, finalBufferedAvailable), nil
			}

		} else {
			logging.Debug(pluginCtx, "enough space on host to pull template", "available", bufferedAvailable, "downloadSize", downloadSize)
		}
	}

	return nil, nil
}
