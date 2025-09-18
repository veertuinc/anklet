//go:build darwin
// +build darwin

package anka

import (
	"context"
	"fmt"

	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/host"
	"github.com/veertuinc/anklet/internal/logging"
)

// EnsureSpaceForTemplate ensures there's enough space for a new template by cleaning up LRU templates if needed
func (cli *Cli) EnsureSpaceForTemplate(
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

	var totalDiskSize uint64
	var bufferSize uint64

	// Get total disk size once at the beginning
	totalDiskSize, err = host.GetHostDiskSizeBytes(pluginCtx)
	if err != nil {
		return nil, err
	}

	// Calculate buffer size based on total disk size and template_disk_buffer percentage
	bufferSize = uint64(float64(totalDiskSize) * (pluginConfig.TemplateDiskBuffer / 100.0))

	// Helper function to check download size and available space
	checkSpace := func() (*AnkaPullCheckOutput, uint64, uint64, error) {
		var args []string
		args = append(args, "--check-download-size")
		if tag != "(using latest)" {
			args = append(args, "--tag", tag)
		}
		// Prepare the arguments correctly for AnkaExecutePullCommand
		pullArgs := append([]string{templateUUID}, args...)
		pullJson, err := cli.AnkaExecutePullCommand(pluginCtx, pullArgs...)
		if err != nil {
			return nil, 0, 0, err
		}
		if pullJson.Status != "OK" {
			return nil, 0, 0, fmt.Errorf("error checking template size: %+v", pullJson)
		}
		pullCheckOutput := &AnkaPullCheckOutput{}
		if body, ok := pullJson.Body.(map[string]any); ok {
			// Handle empty body case (template already available locally)
			if len(body) == 0 {
				// Template is already available locally, no download needed
				// Set all values to 0 - the later logic will handle this case appropriately
				pullCheckOutput.Size = 0
				pullCheckOutput.Cached = 0
				pullCheckOutput.Available = 0
			} else {
				// Safely extract values from non-empty body
				if sizeVal, exists := body["size"]; exists && sizeVal != nil {
					if sizeFloat, ok := sizeVal.(float64); ok {
						pullCheckOutput.Size = uint64(sizeFloat)
					} else {
						logging.Warn(pluginCtx, "invalid size value type in pull check response", "value", sizeVal)
					}
				}

				if cachedVal, exists := body["cached"]; exists && cachedVal != nil {
					if cachedFloat, ok := cachedVal.(float64); ok {
						pullCheckOutput.Cached = uint64(cachedFloat)
					} else {
						logging.Warn(pluginCtx, "invalid cached value type in pull check response", "value", cachedVal)
					}
				}

				if availableVal, exists := body["available"]; exists && availableVal != nil {
					if availableFloat, ok := availableVal.(float64); ok {
						pullCheckOutput.Available = uint64(availableFloat)
					} else {
						logging.Warn(pluginCtx, "invalid available value type in pull check response", "value", availableVal)
					}
				}
			}
		} else {
			return nil, 0, 0, fmt.Errorf("unable to parse pull check output")
		}

		downloadSize := pullCheckOutput.Size - pullCheckOutput.Cached

		// Calculate how much space we can actually use (available space minus required buffer)
		var usableSpace uint64
		if pullCheckOutput.Available > bufferSize {
			usableSpace = pullCheckOutput.Available - bufferSize
		} else {
			// if the available space is less than the buffer size, we can't use any of it
			usableSpace = 0
		}

		logging.Debug(pluginCtx, "pull check output", "pullCheckOutput", map[string]any{
			"downloadSize":    downloadSize,
			"pullCheckOutput": pullCheckOutput,
			"totalDiskSize":   totalDiskSize,
			"bufferSize":      bufferSize,
			"usableSpace":     usableSpace,
		})

		return pullCheckOutput, downloadSize, usableSpace, nil
	}

	pullCheckOutput, downloadSize, usableSpace, err := checkSpace()
	if err != nil {
		return nil, err
	}

	if pullCheckOutput.Size > 0 { // check that it's not empty {"status":"OK","code":0,"body":{},"message":"6c14r: available locally"}

		// if the template is just too big for the host, fail
		if pullCheckOutput.Size >= totalDiskSize {
			return fmt.Errorf("template is too big for the host (need %d, totalDiskSize %d)", pullCheckOutput.Size, totalDiskSize), nil
		}

		// determine if we have enough space to pull the template
		if usableSpace < downloadSize {

			// try to free up space by deleting the LRU templates
			bytesToFree := downloadSize - usableSpace

			logging.Warn(pluginCtx, "insufficient space for template, cleaning up LRU templates",
				"usableSpace", usableSpace,
				"downloadSize", downloadSize,
				"bytesToFree", bytesToFree,
			)

			// Check if the buffer configuration is too restrictive
			// If available space is less than bufferSize, then the buffer requirement
			// is impossible to satisfy regardless of template cleanup
			if pullCheckOutput.Available < bufferSize {
				bufferPercentage := (float64(bufferSize) / float64(totalDiskSize)) * 100.0
				return fmt.Errorf("template_disk_buffer (%.3f%%) is too restrictive for current disk usage - available space (%d) is less than required buffer (%d)",
					bufferPercentage, pullCheckOutput.Available, bufferSize), nil
			}

			// Get LRU templates to delete
			lruTemplates := workerGlobals.TemplateTracker.GetLeastRecentlyUsedTemplates()

			// Remove the template being pulled from the count of LRU templates available for cleanup
			countOfLruTemplates := len(lruTemplates)
			// TODO: later on, we need to support checking the delta between the current
			// template's tag and the incoming, and if using --shrink for pull would clear up
			// enough space, we can proceed with the pull. Anka doesn't support this yet though as we don't clean up the old tag first.
			if workerGlobals.TemplateTracker.Contains(templateUUID) {
				countOfLruTemplates--
			}
			if countOfLruTemplates <= 0 {
				return fmt.Errorf("insufficient space on host and no templates available for cleanup (need %d, available %d)", bytesToFree, usableSpace), nil
			}

			// If even deleting all LRU templates wouldn't free enough space, don't delete anything
			var totalFreeable uint64
			for _, lruTemplate := range lruTemplates {
				// Skip deleting the template if it matches the one being pulled
				if lruTemplate.UUID == templateUUID {
					continue
				}
				totalFreeable += lruTemplate.ImageSize
			}
			logging.Debug(pluginCtx, "lru templates", "lruTemplates", lruTemplates, "totalFreeable", totalFreeable, "bytesToFree", bytesToFree)
			afterFreeingUsableSpace := usableSpace + totalFreeable
			logging.Debug(pluginCtx, "adjusted usable space after freeing", "afterFreeingUsableSpace", afterFreeingUsableSpace, "downloadSize", downloadSize)
			if afterFreeingUsableSpace < downloadSize {
				return fmt.Errorf("insufficient space on host even if we cleaned up all templates (need %d, can free %d, lruTemplateCount %d)", downloadSize, totalFreeable, len(lruTemplates)), nil
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

				// Re-check available space after deletion
				_, newDownloadSize, newUsableSpace, err := checkSpace()
				if err != nil {
					logging.Error(pluginCtx, "failed to re-check space after template deletion", "error", err)
					continue
				}

				logging.Debug(pluginCtx, "space after template deletion",
					"templatesDeleted", templatesDeleted,
					"newUsableSpace", newUsableSpace,
					"newDownloadSize", newDownloadSize)

				// Check if we now have enough space
				if newUsableSpace >= newDownloadSize {
					logging.Info(pluginCtx, "successfully freed enough space for template",
						"templatesDeleted", templatesDeleted,
						"usableSpace", newUsableSpace,
						"downloadSize", newDownloadSize)
					break
				}
			}

			// Final check to ensure we have enough space
			_, finalDownloadSize, finalUsableSpace, err := checkSpace()
			if err != nil {
				return nil, err
			}

			// this should not happen, but just in case
			if finalUsableSpace < finalDownloadSize {
				return fmt.Errorf("insufficient space on host even after cleaning up %d templates (need %d, available %d)", templatesDeleted, finalDownloadSize, finalUsableSpace), nil
			}

		} else {
			logging.Debug(pluginCtx, "enough space on host to pull template", "usableSpace", usableSpace, "downloadSize", downloadSize)
		}
	}

	return nil, nil
}
