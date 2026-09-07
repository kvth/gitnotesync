package cmd

import (
	"context"
	"errors"
	"time"

	"github.com/spf13/cobra"

	"github.com/kvth/gitnotesync/internal/syncer"
	"github.com/kvth/gitnotesync/internal/watcher"
)

func newWatchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "watch [repository]",
		Short: "Watch a repository and sync continuously",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runWatch,
	}
}

func runWatch(cmd *cobra.Command, args []string) error {
	s, runner, root, err := open(cmd, args)
	if err != nil {
		return err
	}
	log := logger()

	// The runner doubles as the watcher's authority on what to ignore: the
	// repository's own .gitignore decides, via git check-ignore.
	w := watcher.New(watcher.Config{
		RepoPath:     root,
		Debounce:     g.debounce,
		MaxDebounce:  g.maxDebounce,
		PollInterval: g.pollInterval,
	}, runner, log)

	// A conflict repeats on every trigger until a human resolves it. Report
	// the first occurrence and then stay quiet, rather than filling the log
	// with the same line every three seconds.
	var lastErr string
	var conflictSince time.Time

	sync := func(ctx context.Context, reason watcher.Reason) {
		res, err := s.Sync(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			msg := err.Error()
			if msg != lastErr {
				lastErr = msg
				conflictSince = time.Now()
				switch {
				case errors.Is(err, syncer.ErrConflict):
					log.Error("sync needs attention", "repo", root, "err", err)
				case errors.Is(err, syncer.ErrBusy), errors.Is(err, syncer.ErrDetached):
					log.Warn("sync paused", "repo", root, "err", err)
				default:
					log.Error("sync failed", "repo", root, "err", err)
				}
			} else {
				log.Debug("sync still failing", "since", time.Since(conflictSince), "err", err)
			}
			return
		}
		if lastErr != "" {
			log.Info("sync recovered", "repo", root)
			lastErr = ""
		}
		if res.DidSomething() {
			log.Info("synced", "reason", reason, "result", res.String())
		} else {
			log.Debug("synced", "reason", reason, "result", res.String())
		}
	}

	return w.Run(cmd.Context(), sync)
}
