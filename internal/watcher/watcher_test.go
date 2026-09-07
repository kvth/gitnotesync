package watcher

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/kvth/gitnotesync/internal/gitx"
	"github.com/kvth/gitnotesync/internal/noise"
)

func repo(t *testing.T, gitignore string) (*gitx.Runner, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", "--initial-branch=main")
	run("config", "user.name", "T")
	run("config", "user.email", "t@e.com")
	mkfile(t, dir, ".gitignore", gitignore)
	return &gitx.Runner{Dir: dir, Timeout: 30 * time.Second}, dir
}

func mkfile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// start runs a watcher and returns a channel of the reasons it fired,
// excluding the unconditional startup sync.
func start(t *testing.T, r *gitx.Runner, dir string) chan Reason {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	w := New(Config{
		RepoPath:     dir,
		Debounce:     150 * time.Millisecond,
		MaxDebounce:  2 * time.Second,
		PollInterval: time.Hour, // never, in tests
	}, r, slog.New(slog.NewTextHandler(io.Discard, nil)))

	fired := make(chan Reason, 32)
	started := make(chan struct{})
	go w.Run(ctx, func(_ context.Context, reason Reason) {
		if reason == ReasonStart {
			close(started)
			return
		}
		fired <- reason
	})
	<-started
	time.Sleep(100 * time.Millisecond) // let the initial watches settle
	return fired
}

func expectSync(t *testing.T, fired chan Reason, why string) {
	t.Helper()
	select {
	case <-fired:
	case <-time.After(3 * time.Second):
		t.Fatalf("no sync triggered: %s", why)
	}
}

func expectNoSync(t *testing.T, fired chan Reason, why string) {
	t.Helper()
	select {
	case r := <-fired:
		t.Fatalf("unexpected sync (%s): %s", r, why)
	case <-time.After(1 * time.Second):
	}
}

func TestWatcherSyncsOnRealChange(t *testing.T) {
	r, dir := repo(t, "*.log\n")
	fired := start(t, r, dir)

	mkfile(t, dir, "note.md", "hello\n")
	expectSync(t, fired, "a tracked-able note changed")
}

// A window in which every path is gitignored must not cost a sync at all.
func TestWatcherIgnoresGitignoredChanges(t *testing.T) {
	r, dir := repo(t, "*.log\nbuild/\n")
	fired := start(t, r, dir)

	mkfile(t, dir, "debug.log", "noise\n")
	mkfile(t, dir, "build/out.js", "noise\n")
	expectNoSync(t, fired, "only gitignored paths changed")

	// ...but a real note in the same repository still syncs.
	mkfile(t, dir, "note.md", "hello\n")
	expectSync(t, fired, "a note changed after the ignored ones")
}

// The regression this rewrite is about: a hardcoded skip list refused to sync
// .obsidian at all. Many people deliberately track their vault config, so the
// only thing that may exclude it is the user's own .gitignore.
func TestWatcherSyncsObsidianConfigUnlessGitignored(t *testing.T) {
	t.Run("tracked by default", func(t *testing.T) {
		r, dir := repo(t, "")
		fired := start(t, r, dir)
		mkfile(t, dir, ".obsidian/app.json", `{"x":1}`)
		expectSync(t, fired, ".obsidian is not gitignored, so it must sync")
	})

	t.Run("excluded when the user says so", func(t *testing.T) {
		r, dir := repo(t, ".obsidian/workspace.json\n")
		// Create the folder before starting, so the test isolates the write
		// to the ignored file. This is also the real-world shape: the folder
		// has existed for months and Obsidian rewrites workspace.json on
		// every pane focus.
		mkfile(t, dir, ".obsidian/app.json", `{"x":0}`)
		fired := start(t, r, dir)

		for i := 0; i < 3; i++ {
			mkfile(t, dir, ".obsidian/workspace.json", `{"panes":[]}`)
			time.Sleep(50 * time.Millisecond)
		}
		expectNoSync(t, fired, "workspace.json is gitignored")

		// The precision a directory-name skip list cannot express: the same
		// folder's other files still sync.
		mkfile(t, dir, ".obsidian/app.json", `{"x":1}`)
		expectSync(t, fired, "app.json in the same folder is not gitignored")
	})
}

// Our own git operations write to .git constantly. git does not report .git as
// ignored -- it is outside the worktree -- so it has to be excluded
// structurally, or every sync would immediately trigger the next one.
func TestWatcherIgnoresGitDirectory(t *testing.T) {
	r, dir := repo(t, "")
	fired := start(t, r, dir)

	mkfile(t, dir, ".git/COMMIT_EDITMSG", "a commit message\n")
	mkfile(t, dir, ".git/refs/heads/main", "deadbeef\n")
	expectNoSync(t, fired, "writes inside .git must never wake the watcher")
}

// Stamping a note writes a temp file beside it. If that woke the watcher, every
// sync would schedule another one.
func TestWatcherIgnoresOwnScratchFiles(t *testing.T) {
	r, dir := repo(t, "")
	fired := start(t, r, dir)

	mkfile(t, dir, noise.ScratchPrefix+"note.md-123456", "half a note\n")
	expectNoSync(t, fired, "our own scratch writes must not wake the watcher")
}

// A gitignored directory should not be walked or watched at all.
func TestWatcherDoesNotWatchIgnoredDirectories(t *testing.T) {
	r, dir := repo(t, "build/\n")
	mkfile(t, dir, "build/deep/nested/file.txt", "x")
	mkfile(t, dir, "notes/deep/a.md", "x")

	w := New(Config{RepoPath: dir}, r, slog.New(slog.NewTextHandler(io.Discard, nil)))
	fsw := newTestFsWatcher(t)
	n := w.addTree(context.Background(), fsw, "")

	// root, notes, notes/deep -- and neither build nor anything under it.
	if n != 3 {
		t.Errorf("watched %d directories, want 3 (root, notes, notes/deep)", n)
	}
	for _, p := range fsw.WatchList() {
		if strings.Contains(p, "build") {
			t.Errorf("watched a gitignored directory: %s", p)
		}
		if strings.Contains(p, ".git"+string(filepath.Separator)) {
			t.Errorf("watched inside .git: %s", p)
		}
	}
}

// A directory created after startup must get its own watch, since inotify is
// not recursive.
func TestWatcherPicksUpNewDirectories(t *testing.T) {
	r, dir := repo(t, "")
	fired := start(t, r, dir)

	mkfile(t, dir, "projects/alpha/plan.md", "# Plan\n")
	expectSync(t, fired, "a note in a brand new folder")

	// The new folder is now watched in its own right.
	mkfile(t, dir, "projects/alpha/notes.md", "more\n")
	expectSync(t, fired, "a second note in the folder created after startup")
}

func newTestFsWatcher(t *testing.T) *fsnotify.Watcher {
	t.Helper()
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fsw.Close() })
	return fsw
}
