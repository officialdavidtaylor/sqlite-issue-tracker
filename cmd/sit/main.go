// Command sit provides local issue tracking backed by SQLite.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/officialdavidtaylor/sqlite-issue-tracker/internal/cli"
)

func main() {
	if err := cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "sit: %v\n", err)
		os.Exit(1)
	}
}
