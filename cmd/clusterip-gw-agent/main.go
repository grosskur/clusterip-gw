// Package main wires the clusterip-gw-agent CLI entrypoint.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/grosskur/clusterip-gw/internal/agent/app"
)

func main() {
	if err := app.Execute(context.Background(), os.Args[1:], os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
