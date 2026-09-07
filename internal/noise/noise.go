// Package noise filters the two kinds of path that git's ignore rules cannot
// answer for: .git itself, and the scratch files this tool writes.
//
// Everything else -- node_modules, .obsidian/workspace.json, editor scratch
// files, whatever this particular user does not want synced -- is decided by
// `git check-ignore`, because it is their .gitignore that says so and not a
// list baked into this tool. See gitx.Ignored.
package noise

import (
	"path/filepath"
	"strings"
)

// IsGitDir reports whether a repository-relative path is inside .git.
//
// This cannot come from .gitignore: git does not ignore .git, it simply treats
// it as outside the worktree, so `git check-ignore .git/index` answers "not
// ignored". Watching it would be actively harmful -- every commit, fetch and
// ref update this tool performs writes there, so each of our own syncs would
// immediately wake the watcher again.
func IsGitDir(rel string) bool {
	rel = filepath.ToSlash(rel)
	return rel == ".git" || strings.HasPrefix(rel, ".git/")
}

// ScratchPrefix marks the temp files this tool writes beside a note while
// replacing it. writeAtomic needs the temp file in the same directory as its
// target -- rename(2) does not cross filesystems -- so it necessarily lands
// inside the user's vault for the moment before the rename.
const ScratchPrefix = ".gitnotesync-"

// IsScratch reports whether a path is one of our own in-flight writes.
//
// Unlike an editor's leftovers, these are not the user's business: they are
// created by this tool, in a repository the user never asked to hold them, so
// expecting a .gitignore entry for them would be passing our own mess along.
// Committing one would be worse -- a crash between create and rename leaves a
// stray file behind, and the next cycle would push a half-written copy of a
// note into the vault.
func IsScratch(path string) bool {
	return strings.HasPrefix(filepath.Base(path), ScratchPrefix)
}
