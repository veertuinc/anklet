package anka

import (
	"context"

	"github.com/veertuinc/anklet/internal/config"
)

type VM struct {
	Name string
}

func GetAnkaVmFromContext(ctx context.Context) *VM {
	ankaVm, ok := ctx.Value(config.ContextKey("ankavm")).(*VM)
	if !ok {
		panic("function GetAnkaVmFromContext failed")
	}
	return ankaVm
}
