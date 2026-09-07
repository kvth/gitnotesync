package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Toplevel resolves path to the root of the repository containing it.
func Toplevel(ctx context.Context, path string) (string, error) {
	r := &Runner{Dir: path, Timeout: 30 * time.Second}
	out, err := r.RunRead(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// GitDir returns the absolute path of the repository's .git directory.
func (r *Runner) GitDir(ctx context.Context) (string, error) {
	out, err := r.RunRead(ctx, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// InProgress names a multi-step operation the user has left half-finished,
// e.g. "rebase" or "merge". It is the empty string when the repository is idle.
//
// This guard is the single most important safety check in the whole tool:
// staging and committing during a half-finished rebase or a conflicted merge
// would commit conflict markers and destroy the user's in-progress resolution.
func (r *Runner) InProgress(ctx context.Context) (string, error) {
	dir, err := r.GitDir(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range []struct{ name, path string }{
		{"rebase", "rebase-merge"},
		{"rebase", "rebase-apply"},
		{"merge", "MERGE_HEAD"},
		{"cherry-pick", "CHERRY_PICK_HEAD"},
		{"revert", "REVERT_HEAD"},
		{"bisect", "BISECT_LOG"},
	} {
		if exists(filepath.Join(dir, c.path)) {
			return c.name, nil
		}
	}
	return "", nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// HasAuthor reports whether user.name and user.email are both resolvable.
// git refuses to commit without them, with an error most users never see
// because their daemon logs scroll past.
func (r *Runner) HasAuthor(ctx context.Context) error {
	for _, key := range []string{"user.name", "user.email"} {
		out, err := r.RunRead(ctx, "config", "--get", key)
		if err != nil || strings.TrimSpace(out) == "" {
			return errors.New("git " + key + " is not set for this repository")
		}
	}
	return nil
}

// Remotes lists the configured remote names.
func (r *Runner) Remotes(ctx context.Context) ([]string, error) {
	out, err := r.RunRead(ctx, "remote")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	return names, nil
}

// AddedAt returns the author date of the commit that first introduced path,
// used to backfill a `created:` timestamp for a note that predates this tool.
// The zero time is returned when the path has no history.
func (r *Runner) AddedAt(ctx context.Context, path string) (time.Time, error) {
	out, err := r.RunRead(ctx, "log", "--follow", "--diff-filter=A",
		"--format=%aI", "-1", "--", path)
	if err != nil {
		return time.Time{}, err
	}
	line := strings.TrimSpace(out)
	if line == "" {
		return time.Time{}, nil
	}
	// --follow can emit several lines across a rename chain; the last is oldest.
	lines := strings.Split(line, "\n")
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(lines[len(lines)-1]))
	if err != nil {
		return time.Time{}, nil
	}
	return t, nil
}

// AddPaths stages the given paths.
//
// Paths are fed through --pathspec-from-file rather than argv so that a vault
// with thousands of changed notes cannot blow the command-line length limit,
// and so that paths containing newlines or globs are taken literally.
func (r *Runner) AddPaths(ctx context.Context, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	var b strings.Builder
	for _, p := range paths {
		b.WriteString(p)
		b.WriteByte(0)
	}
	_, err := r.RunStdin(ctx, []byte(b.String()),
		"add", "--all", "--pathspec-from-file=-", "--pathspec-file-nul")
	return err
}

// CommitOptions tunes a single commit.
type CommitOptions struct {
	Message  string
	RunHooks bool
}

// Commit records the staged changes.
func (r *Runner) Commit(ctx context.Context, opt CommitOptions) error {
	// Signing is always off: a passphrase prompt has no terminal to appear
	// on in a daemon and would hang the sync forever.
	args := []string{"-c", "commit.gpgsign=false", "commit", "-m", opt.Message}
	if !opt.RunHooks {
		// A failing pre-commit hook would otherwise wedge the daemon
		// permanently, with no way for the user to see why.
		args = append(args, "--no-verify")
	}
	_, err := r.Run(ctx, args...)
	return err
}

// Fetch updates the given remote.
func (r *Runner) Fetch(ctx context.Context, remote string) error {
	_, err := r.Run(ctx, "fetch", "--prune", remote)
	return err
}

// ErrRebaseConflict reports that a rebase stopped on a conflict and was
// aborted, leaving the worktree exactly as it was.
var ErrRebaseConflict = errors.New("rebase stopped on a conflict")

// Rebase replays local commits onto upstream. On conflict it aborts and
// returns ErrRebaseConflict, so the worktree is never left mid-rebase for a
// user who has no idea the daemon touched anything.
func (r *Runner) Rebase(ctx context.Context, upstream string) error {
	_, err := r.Run(ctx, "rebase", "--autostash", upstream)
	if err == nil {
		return nil
	}

	inProgress, perr := r.InProgress(ctx)
	if perr != nil {
		return err
	}
	if inProgress != "rebase" {
		return err
	}
	if _, aerr := r.Run(ctx, "rebase", "--abort"); aerr != nil {
		return errors.Join(ErrRebaseConflict, aerr)
	}
	return ErrRebaseConflict
}

// Push publishes the current branch to its upstream.
func (r *Runner) Push(ctx context.Context, remote, branch string) error {
	_, err := r.Run(ctx, "push", remote, "HEAD:"+branch)
	return err
}

// IsRejected reports whether err is a push rejected because the remote moved
// on after our fetch. The cure is another fetch+rebase, not a retry.
func IsRejected(err error) bool {
	s := Stderr(err)
	return strings.Contains(s, "rejected") ||
		strings.Contains(s, "non-fast-forward") ||
		strings.Contains(s, "fetch first")
}

// SplitUpstream splits "origin/main" into its remote and branch parts, using
// the configured remote names so that a branch containing a slash still works.
func SplitUpstream(upstream string, remotes []string) (remote, branch string) {
	for _, rem := range remotes {
		if strings.HasPrefix(upstream, rem+"/") {
			return rem, strings.TrimPrefix(upstream, rem+"/")
		}
	}
	if i := strings.Index(upstream, "/"); i > 0 {
		return upstream[:i], upstream[i+1:]
	}
	return "", ""
}
