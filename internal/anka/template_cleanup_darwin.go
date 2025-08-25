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
	template, tag string,
	requiredSizeBytes uint64,
) error {
	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		return err
	}

	// Get current disk usage (only available on Darwin/macOS)
	hostDiskUsed, err := host.GetHostDiskUsedBytes(pluginCtx)
	if err != nil {
		logging.Warn(pluginCtx, "unable to get host disk usage, skipping space check", "error", err)
		return nil // Continue without space check if we can't get disk info
	}

	hostDiskTotal, err := host.GetHostDiskTotalBytes(pluginCtx)
	if err != nil {
		logging.Warn(pluginCtx, "unable to get host disk total, skipping space check", "error", err)
		return nil // Continue without space check if we can't get disk info
	}

	availableSpace := hostDiskTotal - hostDiskUsed

	// Get the configured disk buffer percentage
	bufferPercentage, err := config.GetEffectiveTemplateDiskBuffer(pluginCtx)
	if err != nil {
		return fmt.Errorf("unable to get effective template disk buffer: %w", err)
	}

	// Check if we have enough space (leave configured buffer)
	bufferBytes := uint64(float64(hostDiskTotal) * (bufferPercentage / 100.0))
	requiredSpaceWithBuffer := requiredSizeBytes + bufferBytes

	if availableSpace >= requiredSpaceWithBuffer {
		logging.Debug(pluginCtx, "sufficient space available for template",
			"availableSpace", availableSpace,
			"requiredSpace", requiredSpaceWithBuffer,
			"bufferPercentage", bufferPercentage)
		return nil
	}

	logging.Info(pluginCtx, "insufficient space for template, cleaning up LRU templates",
		"availableSpace", availableSpace,
		"requiredSpace", requiredSpaceWithBuffer,
		"bufferPercentage", bufferPercentage,
		"needToFree", requiredSpaceWithBuffer-availableSpace)

	// Get LRU templates for cleanup
	lruTemplates := workerGlobals.TemplateTracker.GetLeastRecentlyUsedTemplates()
	spaceToFree := requiredSpaceWithBuffer - availableSpace

	// First, calculate total space that could potentially be freed
	var totalFreeable uint64
	for _, templateUsage := range lruTemplates {
		totalFreeable += templateUsage.ImageSize
	}

	// If even deleting all LRU templates wouldn't free enough space, don't delete anything
	if totalFreeable < spaceToFree {
		logging.Warn(pluginCtx, "insufficient space even if we deleted all LRU templates, returning job to queue",
			"spaceToFree", spaceToFree,
			"totalFreeable", totalFreeable,
			"lruTemplateCount", len(lruTemplates))
		return fmt.Errorf("insufficient space on host even after cleanup (need %d, can free %d)", spaceToFree, totalFreeable)
	}

	// Now proceed with actual deletion since we know it's possible to free enough space
	var freedSpace uint64
	for _, templateUsage := range lruTemplates {
		if freedSpace >= spaceToFree {
			break
		}

		logging.Info(pluginCtx, "deleting LRU template to free space",
			"template", templateUsage.Template,
			"tag", templateUsage.Tag,
			"size", templateUsage.ImageSize,
			"lastUsed", templateUsage.LastUsed,
			"usageCount", templateUsage.UsageCount)

		panic("test")

		err := cli.AnkaDeleteTemplate(pluginCtx, templateUsage.Template, templateUsage.Tag)
		if err != nil {
			logging.Error(pluginCtx, "failed to delete LRU template", "error", err)
			continue
		}

		// Template tracker is automatically updated by AnkaDeleteTemplate
		freedSpace += templateUsage.ImageSize
	}

	// This should not happen since we pre-calculated, but safety check
	if freedSpace < spaceToFree {
		return fmt.Errorf("unexpected: unable to free enough space for template (freed %d, needed %d)", freedSpace, spaceToFree)
	}

	logging.Info(pluginCtx, "successfully freed space for template", "freedSpace", freedSpace)
	return nil
}
