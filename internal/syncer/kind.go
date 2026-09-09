package syncer

import (
	"errors"
	"log/slog"
)

// ErrConfig reports a setup that cannot work as given: no committer identity,
// an unparseable commit template, a path that is not a repository. Nothing
// about it will change until the user changes something.
var ErrConfig = errors.New("configuration error")

// ErrRemote reports a fetch or push that did not complete. Unlike the other
// failures this one is usually weather -- a closed laptop, an SSH agent that
// is not running yet, a remote being restarted -- and clears up on its own.
var ErrRemote = errors.New("remote operation failed")

// Kind classifies a failed sync.
//
// The judgement is made once, here, and read by everything that has to react
// to a failure: `watch` picks a log level and a service-manager status line
// from it, `sync` turns it into an exit status. Re-deriving it at each of
// those points is how the three of them would drift apart.
type Kind string

const (
	// KindNone is the absence of a failure.
	KindNone Kind = ""
	// KindConflict is history that diverged in a way only a human can
	// untangle. Nothing syncs until they do.
	KindConflict Kind = "conflict"
	// KindBusy is a repository left mid-operation, or on a detached HEAD.
	// Also permanent until attended to, but expected: it usually means the
	// user is in the middle of doing something deliberate by hand.
	KindBusy Kind = "busy"
	// KindConfig is a setup that cannot work at all.
	KindConfig Kind = "config"
	// KindRemote is a fetch or push that failed.
	KindRemote Kind = "remote"
	// KindUnknown is everything else: a git failure this tool has no opinion
	// about, or a bug.
	KindUnknown Kind = "unknown"
)

// Classify names the kind of failure err represents. A nil error is KindNone.
//
// The order matters: a push rejected because the remote moved on is retried
// through a pull, so the conflict that pull may hit has to win over the
// remote failure that led to it.
func Classify(err error) Kind {
	switch {
	case err == nil:
		return KindNone
	case errors.Is(err, ErrConflict):
		return KindConflict
	case errors.Is(err, ErrBusy), errors.Is(err, ErrDetached):
		return KindBusy
	case errors.Is(err, ErrConfig):
		return KindConfig
	case errors.Is(err, ErrRemote):
		return KindRemote
	}
	return KindUnknown
}

// NeedsHuman reports whether the failure will persist until someone attends to
// the repository. It is the line between "tell them now" and "try again in ten
// minutes"; a daemon that cries wolf over a suspended laptop gets ignored on
// the day it has something to say.
func (k Kind) NeedsHuman() bool {
	switch k {
	case KindConflict, KindBusy, KindConfig:
		return true
	}
	return false
}

// Level is how loudly to log this kind. KindBusy needs a human but is only a
// warning: the human in question is already at the keyboard finishing a rebase
// of their own, and does not need to be told about it in red.
func (k Kind) Level() slog.Level {
	switch k {
	case KindBusy, KindRemote:
		return slog.LevelWarn
	}
	return slog.LevelError
}

// Hint is a line of remediation to print under an error whose message does not
// already say what to do about it, or "" when there is nothing useful to add.
//
// It reads the error rather than its Kind, because the fix is narrower than
// the classification: a detached HEAD and a half-finished rebase are both
// KindBusy, and telling someone to abort an operation that is not running
// would send them looking for something that is not there.
func Hint(err error) string {
	switch {
	case errors.Is(err, ErrConflict):
		return "resolve it by hand (git pull --rebase, then git push); syncing resumes on its own once the branches agree"
	case errors.Is(err, ErrDetached):
		return "check out a branch (git switch -) to resume syncing"
	case errors.Is(err, ErrBusy):
		return "finish or abort the operation in progress (git status says which); syncing resumes on its own"
	}
	return ""
}
