package syncer

import (
	"context"
	"os"
	"path/filepath"

	"github.com/kvth/gitnotesync/internal/frontmatter"
)

// resolveTimestamps is the ConflictResolver handed to git rebase.
//
// The one conflict this tool reliably causes is its own: two machines stamp
// the same note and the `modified:` line disagrees, which git cannot tell
// apart from a real edit war. When that -- and nothing else -- is what a
// rebase stopped on, the timestamps are reconciled and the rebase carries on.
// Every other conflict is reported false, and the caller aborts.
func (s *Syncer) resolveTimestamps(ctx context.Context, paths []string) (bool, error) {
	if !s.cfg.Stamp.Enabled || !s.cfg.ResolveTimestamps {
		return false, nil
	}

	for _, rel := range paths {
		if !s.eligible(rel) {
			s.log.Debug("conflict is not in a note", "path", rel)
			return false, nil
		}
		abs := filepath.Join(s.cfg.RepoPath, filepath.FromSlash(rel))

		info, err := os.Stat(abs)
		if err != nil || !info.Mode().IsRegular() {
			return false, nil // deleted on one side, or not a plain file
		}
		if s.cfg.Stamp.MaxSize > 0 && info.Size() > s.cfg.Stamp.MaxSize {
			return false, nil
		}

		src, err := os.ReadFile(abs)
		if err != nil {
			return false, err
		}
		res := frontmatter.ResolveConflict(src, s.cfg.Stamp.Options)
		if !res.Resolved {
			s.log.Debug("conflict needs a human", "path", rel)
			return false, nil
		}
		if err := writeAtomic(abs, res.Content, info.Mode().Perm()); err != nil {
			return false, err
		}
		s.log.Info("resolved timestamp conflict", "path", rel, "keys", res.Keys)
	}

	// Staging every path at once keeps the index consistent with what was
	// just written, and is what lets `git rebase --continue` proceed.
	if err := s.git.AddPaths(ctx, paths); err != nil {
		return false, err
	}
	return true, nil
}
