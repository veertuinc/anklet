//go:build darwin

package anka

import (
	"context"
	"fmt"

	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/host"
	"github.com/veertuinc/anklet/internal/logging"
)

func filterEvictableTemplates(candidates []*config.TemplateUsage, excludeUUID string) []*config.TemplateUsage {
	evictable := make([]*config.TemplateUsage, 0, len(candidates))
	for _, tmpl := range candidates {
		if tmpl == nil || tmpl.UUID == excludeUUID {
			continue
		}
		evictable = append(evictable, tmpl)
	}
	return evictable
}

// EnsureSpaceForTemplate ensures there is enough disk space for a template by cleaning up
// LRU templates if needed. Insufficient-space failures wrap ErrInsufficientSpace.
func (cli *Cli) EnsureSpaceForTemplate(
	_ context.Context,
	pluginCtx context.Context,
	templateUUID, tag string,
) error {
	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		return err
	}
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return err
	}

	totalDiskSize, err := host.GetHostDiskSizeBytes(pluginCtx)
	if err != nil {
		return err
	}

	bufferSize := uint64(float64(totalDiskSize) * (pluginConfig.TemplateDiskBuffer / 100.0))

	checkSpace := func() (*AnkaPullCheckOutput, uint64, uint64, error) {
		pullArgs := []string{templateUUID, "--check-download-size"}
		if tag != "(using latest)" {
			pullArgs = append(pullArgs, "--tag", tag)
		}
		pullJson, checkErr := cli.AnkaExecutePullCommand(pluginCtx, pullArgs...)
		if checkErr != nil {
			return nil, 0, 0, checkErr
		}
		if pullJson.Status != "OK" {
			return nil, 0, 0, fmt.Errorf("error checking template size: %+v", pullJson)
		}
		pullCheckOutput := &AnkaPullCheckOutput{}
		body, ok := pullJson.Body.(map[string]any)
		if !ok {
			return nil, 0, 0, fmt.Errorf("unable to parse pull check output")
		}
		if len(body) > 0 {
			if sizeVal, exists := body["size"]; exists && sizeVal != nil {
				sizeFloat, ok := sizeVal.(float64)
				if !ok {
					logging.Warn(pluginCtx, "invalid size value type in pull check response", "value", sizeVal)
					return nil, 0, 0, fmt.Errorf("invalid size value type in pull check response")
				}
				pullCheckOutput.Size = uint64(sizeFloat)
			}
			if cachedVal, exists := body["cached"]; exists && cachedVal != nil {
				cachedFloat, ok := cachedVal.(float64)
				if !ok {
					logging.Warn(pluginCtx, "invalid cached value type in pull check response", "value", cachedVal)
					return nil, 0, 0, fmt.Errorf("invalid cached value type in pull check response")
				}
				pullCheckOutput.Cached = uint64(cachedFloat)
			}
			if availableVal, exists := body["available"]; exists && availableVal != nil {
				availableFloat, ok := availableVal.(float64)
				if !ok {
					logging.Warn(pluginCtx, "invalid available value type in pull check response", "value", availableVal)
					return nil, 0, 0, fmt.Errorf("invalid available value type in pull check response")
				}
				pullCheckOutput.Available = uint64(availableFloat)
			}
		}

		downloadSize := templateDownloadSize(pullCheckOutput.Size, pullCheckOutput.Cached)
		var usableSpace uint64
		if pullCheckOutput.Available > bufferSize {
			usableSpace = pullCheckOutput.Available - bufferSize
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
		return err
	}

	if pullCheckOutput.Size == 0 {
		return nil
	}

	if pullCheckOutput.Size >= totalDiskSize {
		return fmt.Errorf("%w: template is too big for the host (need %d, totalDiskSize %d)", ErrInsufficientSpace, pullCheckOutput.Size, totalDiskSize)
	}

	if usableSpace >= downloadSize {
		logging.Debug(pluginCtx, "enough space on host to pull template", "usableSpace", usableSpace, "downloadSize", downloadSize)
		return nil
	}

	bytesToFree := downloadSize - usableSpace
	logging.Warn(pluginCtx, "insufficient space for template, cleaning up LRU templates",
		"usableSpace", usableSpace,
		"downloadSize", downloadSize,
		"bytesToFree", bytesToFree,
	)

	if pullCheckOutput.Available < bufferSize {
		bufferPercentage := (float64(bufferSize) / float64(totalDiskSize)) * 100.0
		return fmt.Errorf("%w: template_disk_buffer (%.3f%%) is too restrictive for current disk usage - available space (%d) is less than required buffer (%d)",
			ErrInsufficientSpace, bufferPercentage, pullCheckOutput.Available, bufferSize)
	}

	evictable := filterEvictableTemplates(workerGlobals.TemplateTracker.GetLeastRecentlyUsedTemplates(), templateUUID)
	if len(evictable) == 0 {
		return fmt.Errorf("%w: no templates available for cleanup (need %d, available %d)", ErrInsufficientSpace, bytesToFree, usableSpace)
	}

	var totalFreeable uint64
	for _, lruTemplate := range evictable {
		totalFreeable += lruTemplate.ImageSize
	}
	logging.Debug(pluginCtx, "lru templates", "lruTemplates", evictable, "totalFreeable", totalFreeable, "bytesToFree", bytesToFree)

	afterFreeingUsableSpace := usableSpace + totalFreeable
	logging.Debug(pluginCtx, "adjusted usable space after freeing", "afterFreeingUsableSpace", afterFreeingUsableSpace, "downloadSize", downloadSize)
	if afterFreeingUsableSpace < downloadSize {
		return fmt.Errorf("%w: insufficient space even if we cleaned up all templates (need %d, can free %d, lruTemplateCount %d)",
			ErrInsufficientSpace, downloadSize, totalFreeable, len(evictable))
	}

	templatesDeleted := 0
	freedEnough := false
	for _, lruTemplate := range evictable {
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

		if delErr := cli.AnkaDeleteTemplate(pluginCtx, lruTemplate.UUID); delErr != nil {
			logging.Error(pluginCtx, "failed to delete LRU template", "error", delErr)
			continue
		}
		templatesDeleted++

		_, newDownloadSize, newUsableSpace, recheckErr := checkSpace()
		if recheckErr != nil {
			logging.Error(pluginCtx, "failed to re-check space after template deletion", "error", recheckErr)
			continue
		}

		logging.Debug(pluginCtx, "space after template deletion",
			"templatesDeleted", templatesDeleted,
			"newUsableSpace", newUsableSpace,
			"newDownloadSize", newDownloadSize)

		if newUsableSpace >= newDownloadSize {
			logging.Info(pluginCtx, "successfully freed enough space for template",
				"templatesDeleted", templatesDeleted,
				"usableSpace", newUsableSpace,
				"downloadSize", newDownloadSize)
			freedEnough = true
			break
		}
	}

	if freedEnough {
		return nil
	}

	_, finalDownloadSize, finalUsableSpace, err := checkSpace()
	if err != nil {
		return err
	}
	if finalUsableSpace < finalDownloadSize {
		return fmt.Errorf("%w: insufficient space after cleaning up %d templates (need %d, available %d)",
			ErrInsufficientSpace, templatesDeleted, finalDownloadSize, finalUsableSpace)
	}
	return nil
}
