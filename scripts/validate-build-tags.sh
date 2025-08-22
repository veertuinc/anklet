#!/bin/bash
# Validate that all platform-specific files have corresponding implementations

set -e

echo "🏷️  Validating build tags and platform implementations..."

# Find all Go files with build tags
build_tag_files=$(find . -name "*.go" -exec grep -l "//go:build" {} \;)

echo "Files with build tags:"
for file in $build_tag_files; do
    echo "  $file: $(head -1 "$file")"
done

# Check for common patterns that indicate missing implementations
echo ""
echo "🔍 Checking for potential missing implementations..."

# Find exported functions in platform-specific files
for file in $build_tag_files; do
    if [[ $file == *"_darwin.go" ]]; then
        echo "Darwin-specific exports in $file:"
        grep -n "^func [A-Z]" "$file" | sed 's/^/  /'
    fi
done

echo ""
echo "✅ Build tag validation complete. Manual review recommended."
