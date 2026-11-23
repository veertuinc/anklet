package host

import (
	"context"
	"testing"
)

func TestHostResourceQueries(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cpus, err := GetHostCPUCount(ctx)
	if err != nil || cpus <= 0 {
		t.Fatalf("GetHostCPUCount() = (%d, %v), want > 0", cpus, err)
	}
	memBytes, err := GetHostMemoryBytes(ctx)
	if err != nil || memBytes == 0 {
		t.Fatalf("GetHostMemoryBytes() = (%d, %v), want > 0", memBytes, err)
	}
	diskBytes, err := GetHostDiskSizeBytes(ctx)
	if err != nil || diskBytes == 0 {
		t.Fatalf("GetHostDiskSizeBytes() = (%d, %v), want > 0", diskBytes, err)
	}
}
