// Package main starts the clusterip-gw-controller command.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/grosskur/clusterip-gw/internal/controller/app"
)

func main() {
	if err := app.Execute(context.Background(), os.Args[1:], os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
