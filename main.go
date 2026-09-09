// Command gitnotesync keeps a git repository of notes in sync and
// maintains GitJournal-style frontmatter timestamps.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kvth/gitnotesync/cmd"
)

func main() {
	// Cancel on SIGINT/SIGTERM so a sync in flight finishes its current git
	// command rather than being killed with an index.lock left behind.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := cmd.Root()
	root.SetContext(ctx)

	if err := root.Execute(); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		if hint := cmd.Hint(err); hint != "" {
			fmt.Fprintln(os.Stderr, "hint:", hint)
		}
		// The status says which kind of failure it was, so a timer or a
		// wrapper script can react without parsing the line above.
		os.Exit(cmd.ExitCode(err))
	}
}
