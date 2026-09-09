package syncer

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// clone makes a second working copy of the same remote, standing in for the
// user's other machine.
func clone(t *testing.T, remote string) string {
	t.Helper()
	other := filepath.Join(t.TempDir(), "other")
	git(t, filepath.Dir(other), "clone", remote, other)
	git(t, other, "config", "user.name", "Other")
	git(t, other, "config", "user.email", "other@example.com")
	git(t, other, "config", "commit.gpgsign", "false")
	return other
}

func note(modified, body string) string {
	return "---\ncreated: 2026-01-01T00:00:00Z\nmodified: " + modified + "\n---\n\n" + body + "\n"
}

// The conflict this tool causes itself: both machines stamped the same note,
// so `modified:` disagrees while the notes themselves merge fine.
func TestSyncResolvesTimestampOnlyConflict(t *testing.T) {
	work, remote := initRepo(t)
	write(t, work, "note.md", note("2026-09-01T00:00:00Z", "base body"))
	git(t, work, "add", "-A")
	git(t, work, "commit", "-m", "note")
	git(t, work, "push")

	// The other machine edits the end of the note and stamps it later.
	other := clone(t, remote)
	write(t, other, "note.md", note("2026-09-08T09:00:00Z", "base body\n\nadded there"))
	git(t, other, "commit", "-am", "theirs")
	git(t, other, "push")

	// This one edits the start and stamps it earlier.
	write(t, work, "note.md", note("2026-09-08T08:00:00Z", "added here\n\nbase body"))
	git(t, work, "commit", "-am", "ours")

	s := newSyncer(t, work)
	res, err := s.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync() = %v, want the timestamps to be merged", err)
	}
	if !res.Rebased {
		t.Error("Rebased = false, want true")
	}
	if op, _ := s.git.InProgress(context.Background()); op != "" {
		t.Errorf("left a %s in progress", op)
	}

	got := read(t, work, "note.md")
	if strings.Contains(got, "<<<<<<<") {
		t.Fatalf("conflict markers left in the worktree:\n%s", got)
	}
	// The later stamp wins, and neither machine's words are lost.
	if !strings.Contains(got, "modified: 2026-09-08T09:00:00Z") {
		t.Errorf("wanted the later timestamp:\n%s", got)
	}
	if !strings.Contains(got, "added here") || !strings.Contains(got, "added there") {
		t.Errorf("an edit was lost:\n%s", got)
	}
}

// The same disagreement on top of a real one is still a real one.
func TestSyncLeavesRealConflictAlone(t *testing.T) {
	work, remote := initRepo(t)
	write(t, work, "note.md", note("2026-09-01T00:00:00Z", "base body"))
	git(t, work, "add", "-A")
	git(t, work, "commit", "-m", "note")
	git(t, work, "push")

	other := clone(t, remote)
	write(t, other, "note.md", note("2026-09-08T09:00:00Z", "their wording"))
	git(t, other, "commit", "-am", "theirs")
	git(t, other, "push")

	write(t, work, "note.md", note("2026-09-08T08:00:00Z", "our wording"))
	git(t, work, "commit", "-am", "ours")

	s := newSyncer(t, work)
	if _, err := s.Sync(context.Background()); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if op, _ := s.git.InProgress(context.Background()); op != "" {
		t.Errorf("left a %s in progress", op)
	}
	got := read(t, work, "note.md")
	if !strings.Contains(got, "our wording") || strings.Contains(got, "<<<<<<<") {
		t.Errorf("worktree was not left as the user had it:\n%s", got)
	}
}

// With resolution switched off, even the timestamp-only case is a conflict.
func TestSyncResolveTimestampsCanBeDisabled(t *testing.T) {
	work, remote := initRepo(t)
	write(t, work, "note.md", note("2026-09-01T00:00:00Z", "base body"))
	git(t, work, "add", "-A")
	git(t, work, "commit", "-m", "note")
	git(t, work, "push")

	other := clone(t, remote)
	write(t, other, "note.md", note("2026-09-08T09:00:00Z", "base body"))
	git(t, other, "commit", "-am", "theirs")
	git(t, other, "push")

	write(t, work, "note.md", note("2026-09-08T08:00:00Z", "base body"))
	git(t, work, "commit", "-am", "ours")

	cfg := testConfig(work)
	cfg.ResolveTimestamps = false
	s := New(cfg, newSyncer(t, work).git, newSyncer(t, work).log)

	if _, err := s.Sync(context.Background()); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if got := read(t, work, "note.md"); !strings.Contains(got, "modified: 2026-09-08T08:00:00Z") {
		t.Errorf("worktree was modified:\n%s", got)
	}
}

// Two local commits, each conflicting only on the timestamp: the rebase has
// to stop, be resolved, continue, and stop again.
func TestSyncResolvesAcrossSeveralRebaseStops(t *testing.T) {
	work, remote := initRepo(t)
	write(t, work, "a.md", note("2026-09-01T00:00:00Z", "a base"))
	write(t, work, "b.md", note("2026-09-01T00:00:00Z", "b base"))
	git(t, work, "add", "-A")
	git(t, work, "commit", "-m", "notes")
	git(t, work, "push")

	other := clone(t, remote)
	write(t, other, "a.md", note("2026-09-08T09:00:00Z", "a base\n\nthere"))
	write(t, other, "b.md", note("2026-09-08T09:00:00Z", "b base\n\nthere"))
	git(t, other, "commit", "-am", "theirs")
	git(t, other, "push")

	// two separate local commits, each touching one note
	write(t, work, "a.md", note("2026-09-08T08:00:00Z", "here\n\na base"))
	git(t, work, "commit", "-am", "ours a")
	write(t, work, "b.md", note("2026-09-08T10:00:00Z", "here\n\nb base"))
	git(t, work, "commit", "-am", "ours b")

	s := newSyncer(t, work)
	_, err := s.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync() = %v", err)
	}
	if op, _ := s.git.InProgress(context.Background()); op != "" {
		t.Errorf("left a %s in progress", op)
	}
	a, b := read(t, work, "a.md"), read(t, work, "b.md")
	if strings.Contains(a, "<<<") || strings.Contains(b, "<<<") {
		t.Fatal("markers left behind")
	}
	if !strings.Contains(a, "modified: 2026-09-08T09:00:00Z") {
		t.Errorf("a.md: wanted the later stamp (09:00)")
	}
	if !strings.Contains(b, "modified: 2026-09-08T10:00:00Z") {
		t.Errorf("b.md: wanted the later stamp (10:00)")
	}
	for _, f := range []string{a, b} {
		if !strings.Contains(f, "here") || !strings.Contains(f, "there") {
			t.Errorf("an edit was lost:\n%s", f)
		}
	}
}
