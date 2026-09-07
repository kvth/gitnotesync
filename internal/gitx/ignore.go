package gitx

import (
	"context"
	"strings"
)

// Ignored reports which of the given repository-relative paths git's ignore
// rules exclude.
//
// The question is delegated to `git check-ignore` rather than answered by
// parsing .gitignore, for the same reason every other operation shells out:
// git's ignore rules are a real language. Per-directory files, $GIT_DIR/info/
// exclude, core.excludesFile, negation, "**", trailing-slash directory-only
// patterns and precedence between them are all easy to reimplement almost
// correctly, and "almost" here means silently refusing to sync a note.
//
// check-ignore also consults the index, so a tracked file is correctly
// reported as not ignored even when a pattern matches it, and it answers for
// paths that no longer exist -- which matters because filesystem events arrive
// for deletions.
//
// A directory must be given a trailing slash to be matched reliably against a
// directory-only pattern such as "build/": without one, git decides whether
// the path is a directory by looking at the filesystem, which gives the wrong
// answer for a directory that has just been deleted or does not exist yet.
// IgnoredDirs handles that.
//
// All paths are answered in a single subprocess.
func (r *Runner) Ignored(ctx context.Context, paths []string) (map[string]bool, error) {
	ignored := make(map[string]bool, len(paths))
	if len(paths) == 0 {
		return ignored, nil
	}

	var stdin strings.Builder
	for _, p := range paths {
		stdin.WriteString(p)
		stdin.WriteByte(0)
	}

	out, err := r.RunReadStdin(ctx, []byte(stdin.String()), "check-ignore", "-z", "--stdin")
	if err != nil {
		// Exit 1 is check-ignore's answer for "none of these are ignored",
		// not a failure.
		if ExitCode(err) == 1 {
			return ignored, nil
		}
		return nil, err
	}
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			ignored[p] = true
		}
	}
	return ignored, nil
}

// Unignored returns the paths git would not ignore, preserving order.
func (r *Runner) Unignored(ctx context.Context, paths []string) ([]string, error) {
	ignored, err := r.Ignored(ctx, paths)
	if err != nil {
		return nil, err
	}
	keep := make([]string, 0, len(paths))
	for _, p := range paths {
		if !ignored[p] {
			keep = append(keep, p)
		}
	}
	return keep, nil
}

// UnignoredDirs returns the directory paths git would not ignore, given
// without trailing slashes and returned the same way.
//
// The slash is added for the query so that a directory-only pattern matches
// even when the directory is absent from disk, and stripped again from the
// result, since git echoes each input path back verbatim.
func (r *Runner) UnignoredDirs(ctx context.Context, dirs []string) ([]string, error) {
	if len(dirs) == 0 {
		return nil, nil
	}
	withSlash := make([]string, len(dirs))
	for i, d := range dirs {
		withSlash[i] = d + "/"
	}
	kept, err := r.Unignored(ctx, withSlash)
	if err != nil {
		return nil, err
	}
	for i, d := range kept {
		kept[i] = strings.TrimSuffix(d, "/")
	}
	return kept, nil
}
