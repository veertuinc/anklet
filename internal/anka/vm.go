package anka

import (
	"context"
	"fmt"

	"github.com/veertuinc/anklet/internal/config"
)

type VM struct {
	Name string
}

func GetAnkaVmFromContext(ctx context.Context) (*VM, error) {
	ankaVm, ok := ctx.Value(config.ContextKey("ankavm")).(*VM)
	if !ok {
		return nil, fmt.Errorf("GetAnkaVmFromContext failed")
	}
	return ankaVm, nil
}
