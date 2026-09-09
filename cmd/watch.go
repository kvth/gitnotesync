package cmd

import (
	"context"
	"errors"
	"fmt"
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
	var failingSince time.Time

	// The status line, unlike the log, is not a stream: it is one string that
	// systemd shows for as long as it stands, so it is only worth resending
	// when it has actually changed.
	var lastStatus string
	status := func(line string) {
		if line == lastStatus {
			return
		}
		lastStatus = line
		sdNotify("STATUS=" + line)
	}

	ready := false

	sync := func(ctx context.Context, reason watcher.Reason) {
		if !ready {
			// The watcher has registered its inotify watches by the time it
			// asks for the first sync, which is the earliest moment the daemon
			// is honestly up. Saying so before that first cycle rather than
			// after it keeps a slow initial push from tripping systemd's
			// start timeout.
			ready = true
			lastStatus = "watching " + root
			sdNotify("READY=1\nSTATUS=" + lastStatus)
		}

		res, err := s.Sync(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			kind := syncer.Classify(err)
			msg := err.Error()
			if msg != lastErr {
				lastErr = msg
				failingSince = time.Now()
				log.Log(ctx, kind.Level(), failureMessage(kind),
					"repo", root, "kind", string(kind), "err", err)
			} else {
				log.Debug("sync still failing", "since", time.Since(failingSince), "err", err)
			}
			status(failureStatus(kind, err, failingSince))
			return
		}
		if lastErr != "" {
			log.Info("sync recovered", "repo", root)
			lastErr = ""
			failingSince = time.Time{}
		}
		status(fmt.Sprintf("watching %s; last sync %s: %s",
			root, time.Now().Format(statusTime), res))
		if res.DidSomething() {
			log.Info("synced", "reason", reason, "result", res.String())
		} else {
			log.Debug("synced", "reason", reason, "result", res.String())
		}
	}

	err = w.Run(cmd.Context(), sync)
	sdNotify("STOPPING=1")
	return err
}

// statusTime is compact but unambiguous over the weeks a stalled sync can go
// unnoticed, which "15:04" on its own would not be.
const statusTime = "Jan 2 15:04"

// failureStatus renders the line `systemctl status` shows. For anyone not
// reading the journal it is the only place a stopped sync becomes visible, so
// it has to carry what went wrong and since when -- not merely that the unit
// is still running, which it will be either way.
func failureStatus(kind syncer.Kind, err error, since time.Time) string {
	verb := "degraded"
	if kind.NeedsHuman() {
		verb = "BLOCKED"
	}
	return fmt.Sprintf("%s since %s: %v", verb, since.Format(statusTime), err)
}

// failureMessage is the log line for a kind of failure. They are worded apart
// so that a repository the user is deliberately mid-rebase in does not read
// like an outage.
func failureMessage(kind syncer.Kind) string {
	switch kind {
	case syncer.KindBusy:
		return "sync paused"
	case syncer.KindConflict, syncer.KindConfig:
		return "sync needs attention"
	}
	return "sync failed"
}
