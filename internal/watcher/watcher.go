// Package watcher turns filesystem activity, a poll timer and wake-from-sleep
// into a stream of coalesced sync triggers.
package watcher

import (
	"context"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/kvth/gitnotesync/internal/noise"
)

// Reason explains why a sync was triggered, for logging.
type Reason string

const (
	ReasonStart  Reason = "startup"
	ReasonChange Reason = "file change"
	ReasonPoll   Reason = "poll"
	ReasonWake   Reason = "wake from sleep"
)

// Ignorer answers which repository-relative paths git's ignore rules exclude.
// It is satisfied by *gitx.Runner.
type Ignorer interface {
	Unignored(ctx context.Context, paths []string) ([]string, error)
	UnignoredDirs(ctx context.Context, dirs []string) ([]string, error)
}

// Config tunes the watcher.
type Config struct {
	RepoPath string
	// Debounce is the quiet period after the last filesystem event before a
	// sync runs. It must comfortably exceed how long an editor takes to
	// finish a save, or the tool will read half-written notes.
	Debounce time.Duration
	// MaxDebounce caps how long continuous activity can postpone a sync, so
	// that a long editing session still syncs periodically.
	MaxDebounce time.Duration
	// PollInterval is how often to sync anyway, to pick up commits pushed
	// from another machine.
	PollInterval time.Duration
}

// Watcher coalesces triggers for one repository.
type Watcher struct {
	cfg     Config
	ignorer Ignorer
	log     *slog.Logger
}

// New builds a Watcher.
func New(cfg Config, ignorer Ignorer, log *slog.Logger) *Watcher {
	return &Watcher{cfg: cfg, ignorer: ignorer, log: log}
}

// Run blocks, calling sync for each coalesced trigger. sync runs on Run's own
// goroutine, so two cycles never overlap; events that arrive during a slow
// push are buffered and collapse into the next trigger.
func (w *Watcher) Run(ctx context.Context, sync func(context.Context, Reason)) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fsw.Close()

	n := w.addTree(ctx, fsw, "")
	w.log.Info("watching", "path", w.cfg.RepoPath, "directories", n)

	// Drain fsnotify's channel into a buffered one immediately. fsnotify does
	// not buffer, and a consumer busy running `git push` for ten seconds would
	// otherwise stall the kernel's event queue and drop events outright.
	events := make(chan fsnotify.Event, 4096)
	go func() {
		for {
			select {
			case ev, ok := <-fsw.Events:
				if !ok {
					return
				}
				select {
				case events <- ev:
				default: // full: the pending sync will catch everything anyway
				}
			case err, ok := <-fsw.Errors:
				if !ok {
					return
				}
				w.log.Warn("watch error", "err", err)
			case <-ctx.Done():
				return
			}
		}
	}()

	sync(ctx, ReasonStart)

	poll := newTicker(w.cfg.PollInterval)
	defer poll.Stop()
	wake := w.wakeChan(ctx)

	debounce := time.NewTimer(time.Hour)
	stopTimer(debounce)
	var pendingSince time.Time
	pending := map[string]bool{} // repo-relative paths seen this window
	created := map[string]bool{} // of those, ones that may be new directories

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case ev := <-events:
			rel, ok := w.candidate(ev)
			if !ok {
				continue
			}
			pending[rel] = true
			if ev.Has(fsnotify.Create) {
				created[rel] = true
			}
			if pendingSince.IsZero() {
				pendingSince = time.Now()
			}
			wait := w.cfg.Debounce
			if cap := w.cfg.MaxDebounce - time.Since(pendingSince); cap < wait {
				wait = max(cap, 0)
			}
			stopTimer(debounce)
			debounce.Reset(wait)

		case <-debounce.C:
			pendingSince = time.Time{}
			w.absorb(events, pending, created)

			// One check-ignore call decides the whole window. Asking per
			// event would mean a subprocess per keystroke; asking here costs
			// one per sync, and a window in which everything was ignored
			// costs no sync at all.
			live := w.unignored(ctx, keys(pending))
			for _, rel := range live {
				if created[rel] {
					w.watchIfDir(ctx, fsw, rel)
				}
			}
			clear(pending)
			clear(created)

			if len(live) == 0 {
				w.log.Debug("all changed paths are ignored, not syncing")
				continue
			}
			sync(ctx, ReasonChange)

		case <-poll.C:
			sync(ctx, ReasonPoll)

		case <-wake:
			sync(ctx, ReasonWake)
		}
	}
}

// candidate converts an event into a repository-relative path, dropping the
// ones that never need git's opinion.
func (w *Watcher) candidate(ev fsnotify.Event) (string, bool) {
	if ev.Op == fsnotify.Chmod {
		// Chmod also fires for a bare mtime touch, which git does not care
		// about.
		return "", false
	}
	rel, err := filepath.Rel(w.cfg.RepoPath, ev.Name)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == ".." {
		return "", false
	}
	if noise.IsGitDir(rel) || noise.IsScratch(rel) {
		return "", false
	}
	return rel, true
}

// unignored asks git which paths matter. On failure it keeps everything: a
// spurious sync is wasted work, but a dropped one loses a note.
func (w *Watcher) unignored(ctx context.Context, paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	live, err := w.ignorer.Unignored(ctx, paths)
	if err != nil {
		w.log.Warn("cannot check ignore rules, assuming nothing is ignored", "err", err)
		return paths
	}
	return live
}

// unignoredDirs is unignored for directories, which need a trailing slash to
// match a directory-only pattern such as "build/".
func (w *Watcher) unignoredDirs(ctx context.Context, dirs []string) []string {
	if len(dirs) == 0 {
		return nil
	}
	live, err := w.ignorer.UnignoredDirs(ctx, dirs)
	if err != nil {
		w.log.Warn("cannot check ignore rules, watching everything", "err", err)
		return dirs
	}
	return live
}

// absorb pulls any events that arrived while the timer was firing into the
// same window, so they are covered by this cycle's ignore check.
func (w *Watcher) absorb(events chan fsnotify.Event, pending, created map[string]bool) {
	for {
		select {
		case ev := <-events:
			if rel, ok := w.candidate(ev); ok {
				pending[rel] = true
				if ev.Has(fsnotify.Create) {
					created[rel] = true
				}
			}
		default:
			return
		}
	}
}

func (w *Watcher) watchIfDir(ctx context.Context, fsw *fsnotify.Watcher, rel string) {
	st, err := os.Stat(filepath.Join(w.cfg.RepoPath, filepath.FromSlash(rel)))
	if err != nil || !st.IsDir() {
		return
	}
	// inotify is not recursive and a vault gains folders all the time. Any
	// file created in the gap before this watch existed is still caught,
	// because the sync about to run reads the whole worktree via git status.
	if n := w.addTree(ctx, fsw, rel); n > 0 {
		w.log.Debug("watching new directory", "path", rel, "directories", n)
	}
}

// addTree registers root and every directory beneath it, breadth-first.
//
// Each depth level is checked against git's ignore rules in a single
// check-ignore call, so an ignored directory is never descended into at all --
// a repository with a gitignored node_modules costs one subprocess, not a walk
// of a hundred thousand files.
func (w *Watcher) addTree(ctx context.Context, fsw *fsnotify.Watcher, root string) int {
	count := 0
	level := []string{root}

	for len(level) > 0 {
		var children []string
		for _, rel := range level {
			abs := w.cfg.RepoPath
			if rel != "" {
				abs = filepath.Join(abs, filepath.FromSlash(rel))
			}
			if err := fsw.Add(abs); err != nil {
				w.log.Warn("cannot watch directory", "path", abs, "err", err)
				continue
			}
			count++

			entries, err := os.ReadDir(abs)
			if err != nil {
				// A directory that vanished mid-walk, or one we may not read,
				// is not worth aborting over.
				w.log.Debug("cannot read directory", "path", abs, "err", err)
				continue
			}
			for _, e := range entries {
				// DirEntry reports a symlink as a symlink, so links are never
				// followed and a link loop cannot hang startup.
				if !e.IsDir() {
					continue
				}
				child := path.Join(rel, e.Name())
				if noise.IsGitDir(child) {
					continue
				}
				children = append(children, child)
			}
		}
		sort.Strings(children)
		level = w.unignoredDirs(ctx, children)
	}
	return count
}

// wakeChan reports when the machine appears to have resumed from sleep.
//
// A laptop that suspends for an hour leaves timers unfired and the working
// copy hours behind the remote. Rather than three platform-specific power
// APIs, this watches for wall-clock time jumping further than the tick should
// have allowed, which is what suspend looks like from userspace everywhere.
func (w *Watcher) wakeChan(ctx context.Context) <-chan struct{} {
	const tick = 30 * time.Second
	out := make(chan struct{}, 1)
	go func() {
		last := time.Now()
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				if now.Sub(last) > 3*tick {
					select {
					case out <- struct{}{}:
					default:
					}
				}
				last = now
			}
		}
	}()
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func newTicker(d time.Duration) *time.Ticker {
	if d <= 0 {
		// A stopped ticker still needs a valid channel to select on.
		t := time.NewTicker(time.Hour)
		t.Stop()
		return t
	}
	return time.NewTicker(d)
}

func stopTimer(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}
