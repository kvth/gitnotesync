package syncer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kvth/gitnotesync/internal/frontmatter"
	"github.com/kvth/gitnotesync/internal/gitx"
	"github.com/kvth/gitnotesync/internal/noise"
)

func testConfig(root string) Config {
	return Config{
		RepoPath:          root,
		CommitSubject:     "notes: auto-sync",
		Pull:              true,
		Push:              true,
		ResolveTimestamps: true,
		Stamp: StampConfig{
			Enabled: true,
			Include: []string{"*.md"},
			Options: frontmatter.Options{
				CreatedKey:  "created",
				ModifiedKey: "modified",
				TimeFormat:  time.RFC3339,
				CreateBlock: true,
			},
			ModifiedFromMTime: true,
			BackfillCreated:   true,
			MaxSize:           1 << 20,
		},
	}
}

func newSyncer(t *testing.T, root string) *Syncer {
	t.Helper()
	r := &gitx.Runner{Dir: root, Timeout: 30 * time.Second}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(testConfig(root), r, log)
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// initRepo creates a bare "remote" plus a clone wired to it.
func initRepo(t *testing.T) (work, remote string) {
	t.Helper()
	base := t.TempDir()
	remote = filepath.Join(base, "remote.git")
	work = filepath.Join(base, "work")

	git(t, base, "init", "--bare", "--initial-branch=main", remote)
	git(t, base, "clone", remote, work)
	git(t, work, "config", "user.name", "Test")
	git(t, work, "config", "user.email", "test@example.com")
	git(t, work, "config", "commit.gpgsign", "false")

	write(t, work, "README.md", "---\ntitle: readme\n---\n\nhello\n")
	git(t, work, "add", "-A")
	git(t, work, "commit", "-m", "initial")
	git(t, work, "push", "-u", "origin", "main")
	return work, remote
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// writeAtomic drops its temp file next to the note it is replacing, so a crash
// between create and rename leaves one in the vault. It must never be
// committed: it is a half-written copy of a real note, and it is our file
// rather than something the user would have thought to gitignore.
func TestSyncDoesNotCommitLeftoverScratchFiles(t *testing.T) {
	work, _ := initRepo(t)
	write(t, work, "notes/a.md", "# A note\n")
	write(t, work, "notes/"+noise.ScratchPrefix+"a.md-123456", "half a note\n")

	s := newSyncer(t, work)
	res, err := s.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range res.Committed {
		if strings.Contains(p, noise.ScratchPrefix) {
			t.Errorf("committed our own scratch file %q", p)
		}
	}
	if tracked := git(t, work, "ls-files"); strings.Contains(tracked, noise.ScratchPrefix) {
		t.Errorf("scratch file is tracked:\n%s", tracked)
	}
	if len(res.Committed) != 1 || res.Committed[0] != "notes/a.md" {
		t.Errorf("Committed = %v, want [notes/a.md]", res.Committed)
	}
}

func TestSyncStampsCommitsAndPushes(t *testing.T) {
	work, _ := initRepo(t)
	write(t, work, "notes/new.md", "# A new note\n")

	s := newSyncer(t, work)
	res, err := s.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stamped) != 1 || res.Stamped[0] != "notes/new.md" {
		t.Errorf("Stamped = %v, want [notes/new.md]", res.Stamped)
	}
	if len(res.Committed) != 1 {
		t.Errorf("Committed = %v, want 1 file", res.Committed)
	}
	if !res.Pushed {
		t.Error("Pushed = false, want true")
	}

	got := read(t, work, "notes/new.md")
	if !strings.HasPrefix(got, "---\ncreated: ") || !strings.Contains(got, "\nmodified: ") {
		t.Errorf("frontmatter not written:\n%s", got)
	}
	if !strings.HasSuffix(got, "# A new note\n") {
		t.Errorf("body was altered:\n%s", got)
	}
	if out := git(t, work, "status", "--porcelain"); out != "" {
		t.Errorf("worktree dirty after sync:\n%s", out)
	}
}

// The stamping write must not leave the repository dirty, or the watcher would
// re-trigger forever. This is the loop-safety property, checked end to end.
func TestSyncIsIdempotent(t *testing.T) {
	work, _ := initRepo(t)
	write(t, work, "notes/a.md", "body\n")

	s := newSyncer(t, work)
	ctx := context.Background()
	if _, err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	before := git(t, work, "rev-parse", "HEAD")

	for i := 0; i < 3; i++ {
		res, err := s.Sync(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if res.DidSomething() {
			t.Fatalf("run %d did work when nothing changed: %s", i, res)
		}
	}
	if after := git(t, work, "rev-parse", "HEAD"); after != before {
		t.Errorf("HEAD moved without any change: %s -> %s", before, after)
	}
}

// An existing created value belongs to the user (or GitJournal) and must
// survive, no matter how many times the file is edited and synced.
func TestSyncPreservesCreated(t *testing.T) {
	work, _ := initRepo(t)
	write(t, work, "notes/a.md", "---\ncreated: 1999-01-01T00:00:00Z\ntags: [x]\n---\nbody\n")

	s := newSyncer(t, work)
	ctx := context.Background()
	if _, err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	write(t, work, "notes/a.md", read(t, work, "notes/a.md")+"more\n")
	if _, err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	got := read(t, work, "notes/a.md")
	if !strings.Contains(got, "created: 1999-01-01T00:00:00Z") {
		t.Errorf("created was overwritten:\n%s", got)
	}
	if !strings.Contains(got, "tags: [x]") {
		t.Errorf("unrelated key lost:\n%s", got)
	}
}

func TestSyncPullsRemoteNotes(t *testing.T) {
	work, remote := initRepo(t)

	other := filepath.Join(t.TempDir(), "other")
	git(t, filepath.Dir(other), "clone", remote, other)
	git(t, other, "config", "user.name", "Other")
	git(t, other, "config", "user.email", "other@example.com")
	write(t, other, "notes/remote.md", "from elsewhere\n")
	git(t, other, "add", "-A")
	git(t, other, "commit", "-m", "remote note")
	git(t, other, "push")

	s := newSyncer(t, work)
	res, err := s.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rebased {
		t.Error("Rebased = false, want true")
	}
	if got := read(t, work, "notes/remote.md"); got != "from elsewhere\n" {
		t.Errorf("remote note not pulled: %q", got)
	}
}

// Two machines editing the same note is the case that must fail loudly and
// leave the worktree exactly as the user left it.
func TestSyncConflictAbortsCleanly(t *testing.T) {
	work, remote := initRepo(t)

	other := filepath.Join(t.TempDir(), "other")
	git(t, filepath.Dir(other), "clone", remote, other)
	git(t, other, "config", "user.name", "Other")
	git(t, other, "config", "user.email", "other@example.com")
	write(t, other, "conflict.md", "theirs\n")
	git(t, other, "add", "-A")
	git(t, other, "commit", "-m", "theirs")
	git(t, other, "push")

	write(t, work, "conflict.md", "ours\n")

	s := newSyncer(t, work)
	_, err := s.Sync(context.Background())
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if op, _ := s.git.InProgress(context.Background()); op != "" {
		t.Errorf("left a %s in progress; the rebase should have been aborted", op)
	}
	if got := read(t, work, "conflict.md"); !strings.Contains(got, "ours") {
		t.Errorf("local content lost: %q", got)
	}
	if strings.Contains(read(t, work, "conflict.md"), "<<<<<<<") {
		t.Error("conflict markers were written into the worktree")
	}
}

// A user resolving a merge by hand must not have the daemon commit over them.
func TestSyncRefusesWhileAnOperationIsInProgress(t *testing.T) {
	work, _ := initRepo(t)
	gitDir := strings.TrimSpace(git(t, work, "rev-parse", "--absolute-git-dir"))
	head := strings.TrimSpace(git(t, work, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(gitDir, "MERGE_HEAD"), []byte(head+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, work, "notes/a.md", "body\n")

	s := newSyncer(t, work)
	_, err := s.Sync(context.Background())
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if out := git(t, work, "status", "--porcelain", "-uall"); !strings.Contains(out, "notes/a.md") {
		t.Error("the file should have been left untouched and uncommitted")
	}
}

func TestSyncRefusesOnDetachedHead(t *testing.T) {
	work, _ := initRepo(t)
	git(t, work, "checkout", "--detach", "HEAD")
	write(t, work, "notes/a.md", "body\n")

	s := newSyncer(t, work)
	if _, err := s.Sync(context.Background()); !errors.Is(err, ErrDetached) {
		t.Fatalf("err = %v, want ErrDetached", err)
	}
}

func TestSyncSkipsIgnoredAndNonNoteFiles(t *testing.T) {
	work, _ := initRepo(t)
	write(t, work, ".gitignore", "secret.md\n")
	write(t, work, "secret.md", "do not commit\n")
	write(t, work, "image.png", "\x89PNG\x00binary\n")
	write(t, work, "note.md", "body\n")

	s := newSyncer(t, work)
	res, err := s.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range res.Committed {
		if p == "secret.md" {
			t.Error("committed a gitignored file")
		}
	}
	if got := read(t, work, "secret.md"); got != "do not commit\n" {
		t.Errorf("gitignored file was stamped: %q", got)
	}
	if got := read(t, work, "image.png"); !strings.HasPrefix(got, "\x89PNG") {
		t.Errorf("non-note file was stamped: %q", got)
	}
	if !strings.Contains(read(t, work, "note.md"), "modified:") {
		t.Error("note.md was not stamped")
	}
}

// Without an upstream the tool must still be useful: commit locally, skip the
// network entirely rather than erroring on every cycle.
func TestSyncWithoutUpstreamCommitsLocally(t *testing.T) {
	work := t.TempDir()
	git(t, work, "init", "--initial-branch=main")
	git(t, work, "config", "user.name", "Test")
	git(t, work, "config", "user.email", "test@example.com")
	write(t, work, "note.md", "body\n")

	s := newSyncer(t, work)
	res, err := s.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Committed) != 1 {
		t.Errorf("Committed = %v, want 1", res.Committed)
	}
	if res.Fetched || res.Pushed {
		t.Error("touched the network with no upstream configured")
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	work, _ := initRepo(t)
	write(t, work, "note.md", "body\n")

	cfg := testConfig(work)
	cfg.DryRun = true
	cfg.Stamp.DryRun = true
	s := New(cfg, &gitx.Runner{Dir: work, Timeout: 30 * time.Second},
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	head := git(t, work, "rev-parse", "HEAD")
	if _, err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := read(t, work, "note.md"); got != "body\n" {
		t.Errorf("dry run wrote to the file: %q", got)
	}
	if got := git(t, work, "rev-parse", "HEAD"); got != head {
		t.Error("dry run created a commit")
	}
}

// A file still being written must be left for the next cycle rather than read
// half-saved and rewritten over the editor's in-flight save.
func TestStampDefersUnsettledFiles(t *testing.T) {
	work, _ := initRepo(t)
	write(t, work, "note.md", "body\n")

	cfg := testConfig(work)
	cfg.Stamp.Settle = time.Hour
	s := New(cfg, &gitx.Runner{Dir: work, Timeout: 30 * time.Second},
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	res, err := s.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stamped) != 0 {
		t.Errorf("Stamped = %v, want none", res.Stamped)
	}
	if got := read(t, work, "note.md"); got != "body\n" {
		t.Errorf("unsettled file was rewritten: %q", got)
	}
	// It is still committed: capturing the user's text matters more than the
	// timestamp, and the stamp lands on the next edit.
	if len(res.Committed) != 1 {
		t.Errorf("Committed = %v, want 1", res.Committed)
	}
}
