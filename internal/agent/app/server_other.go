//go:build !linux

package app

import (
	"context"
	"fmt"
)

func (o *Options) Run(_ context.Context) error {
	return fmt.Errorf("clusterip-gw-agent currently supports Linux only")
}
