package syncer

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/kvth/gitnotesync/internal/frontmatter"
	"github.com/kvth/gitnotesync/internal/gitx"
	"github.com/kvth/gitnotesync/internal/noise"
)

// StampConfig controls frontmatter maintenance.
type StampConfig struct {
	// Enabled turns the whole feature on.
	Enabled bool
	// Include is a list of filepath.Match patterns matched against the base
	// name, e.g. "*.md".
	Include []string
	// Options are passed through to the frontmatter rewriter.
	Options frontmatter.Options
	// ModifiedFromMTime takes `modified` from the file's mtime rather than
	// the current time, so the recorded value is the moment the user saved
	// rather than the moment the daemon woke up.
	ModifiedFromMTime bool
	// BackfillCreated looks up a tracked file's first commit to fill in a
	// missing `created`, instead of pretending the note was made today.
	BackfillCreated bool
	// Settle is how long a file must have been quiet before it is rewritten.
	// Rewriting a file an editor is still mid-save on can lose the save.
	Settle time.Duration
	// MaxSize skips files above this size; notes are small and a multi-
	// megabyte match is far more likely to be an asset than a note.
	MaxSize int64
	// DryRun reports what would change without writing.
	DryRun bool
}

// stampFiles rewrites frontmatter for the eligible entries and returns the
// paths it changed.
//
// Only paths git already reports as dirty are considered. That single
// restriction is what makes the feature safe to run on a watcher: a file the
// user is not editing is never opened, and a file that is rewritten is
// committed in the very same cycle, so the write cannot re-trigger work.
func (s *Syncer) stampFiles(ctx context.Context, entries []gitx.Entry, now time.Time) ([]string, error) {
	var changed []string
	for _, e := range entries {
		if e.Deleted() || !s.eligible(e.Path) {
			continue
		}
		ok, err := s.stampFile(ctx, e, now)
		if err != nil {
			// One unreadable note must not stop the sync; the rest of the
			// vault still needs to reach the remote.
			s.log.Warn("frontmatter skipped", "path", e.Path, "err", err)
			continue
		}
		if ok {
			changed = append(changed, e.Path)
		}
	}
	return changed, nil
}

func (s *Syncer) eligible(rel string) bool {
	base := filepath.Base(rel)
	for _, pat := range s.cfg.Stamp.Include {
		if ok, err := filepath.Match(pat, base); err == nil && ok {
			return true
		}
	}
	return false
}

func (s *Syncer) stampFile(ctx context.Context, e gitx.Entry, now time.Time) (bool, error) {
	abs := filepath.Join(s.cfg.RepoPath, filepath.FromSlash(e.Path))

	before, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil // deleted between status and now
		}
		return false, err
	}
	if !before.Mode().IsRegular() {
		return false, nil
	}
	if before.Size() == 0 {
		// An editor that truncates before writing leaves a zero-byte file for
		// a few milliseconds. Committing that snapshot would record an empty
		// note; skipping it costs nothing, as the next cycle sees the real one.
		return false, nil
	}
	if s.cfg.Stamp.MaxSize > 0 && before.Size() > s.cfg.Stamp.MaxSize {
		return false, nil
	}
	if age := now.Sub(before.ModTime()); age < s.cfg.Stamp.Settle {
		s.log.Debug("frontmatter deferred, file still settling", "path", e.Path, "age", age)
		return false, nil
	}

	src, err := os.ReadFile(abs)
	if err != nil {
		return false, err
	}
	if bytes.IndexByte(src[:min(len(src), 8000)], 0) >= 0 {
		return false, nil // binary
	}

	created := now
	if s.cfg.Stamp.BackfillCreated && !e.Untracked() {
		if t, err := s.git.AddedAt(ctx, e.Path); err == nil && !t.IsZero() {
			created = t
		}
	}
	modified := now
	if s.cfg.Stamp.ModifiedFromMTime {
		modified = before.ModTime()
	}
	created, modified = created.Local().Truncate(time.Second), modified.Local().Truncate(time.Second)

	res := frontmatter.Apply(src, created, modified, s.cfg.Stamp.Options)
	if res.Malformed {
		s.log.Warn("unterminated frontmatter, left untouched", "path", e.Path)
		return false, nil
	}
	if !res.Changed {
		return false, nil
	}
	if s.cfg.Stamp.DryRun {
		s.log.Info("would stamp frontmatter", "path", e.Path)
		return false, nil
	}

	// Re-stat immediately before writing. If the file moved underneath us
	// between the read and now, the user's editor is mid-save and our copy is
	// stale: writing it would silently discard their save. Skip; the next
	// cycle picks it up.
	after, err := os.Stat(abs)
	if err != nil {
		return false, err
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		s.log.Debug("file changed while stamping, retrying next cycle", "path", e.Path)
		return false, nil
	}

	if err := writeAtomic(abs, res.Content, before.Mode().Perm()); err != nil {
		return false, err
	}
	s.log.Debug("stamped frontmatter", "path", e.Path,
		"created", res.WroteCreated, "modified", res.WroteModified, "new_block", res.AddedBlock)
	return true, nil
}

// writeAtomic replaces path in a single rename.
//
// A truncate-and-write would leave the note empty or half-written if the
// process died, or if an editor read the file at that instant. A rename means
// every observer sees either the old file or the new one.
func writeAtomic(path string, data []byte, perm fs.FileMode) error {
	dir, base := filepath.Dir(path), filepath.Base(path)
	tmp, err := os.CreateTemp(dir, noise.ScratchPrefix+base+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if perm != 0 {
		if err := os.Chmod(name, perm); err != nil {
			return err
		}
	}
	return os.Rename(name, path)
}
