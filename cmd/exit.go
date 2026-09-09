package cmd

import "github.com/kvth/gitnotesync/internal/syncer"

// Exit statuses.
//
// The point of splitting them up is that a caller -- a systemd timer's
// OnFailure=, a cron wrapper, a status bar -- can tell a failure that needs a
// person from one that will have cleared by the next run, without scraping
// stderr for a message this tool is free to reword.
//
// 2 is left out on purpose: it is conventionally a usage error, and flag
// parsing does not come through here.
const (
	ExitOK       = 0
	ExitFailed   = 1 // unclassified: a git failure this tool has no opinion on
	ExitBusy     = 3 // mid-rebase, unmerged paths, detached HEAD
	ExitConflict = 4 // local and upstream history diverged
	ExitConfig   = 5 // no committer identity, bad template, not a repository
	ExitRemote   = 6 // fetch or push did not complete
)

// ExitCode maps a command error onto the process's exit status.
func ExitCode(err error) int {
	switch syncer.Classify(err) {
	case syncer.KindNone:
		return ExitOK
	case syncer.KindBusy:
		return ExitBusy
	case syncer.KindConflict:
		return ExitConflict
	case syncer.KindConfig:
		return ExitConfig
	case syncer.KindRemote:
		return ExitRemote
	}
	return ExitFailed
}

// Hint is a line of remediation to print under err, or "" when the error
// message already says everything worth saying.
func Hint(err error) string { return syncer.Hint(err) }
