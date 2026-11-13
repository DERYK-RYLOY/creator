// Command creator-tui is Creator's full-screen terminal front-end.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/creatorcli/creator/internal/tui"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := tui.Run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if ee, ok := err.(*tui.ExitError); ok {
			os.Exit(ee.Code)
		}
		fmt.Fprintf(os.Stderr, "creator-tui: %v\n", err)
		os.Exit(1)
	}
}
