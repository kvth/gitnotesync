// Package syncer performs one commit/pull/push cycle over a notes repository.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/kvth/gitnotesync/internal/gitx"
	"github.com/kvth/gitnotesync/internal/noise"
)

// Config is everything one Syncer needs.
type Config struct {
	RepoPath string
	Stamp    StampConfig
	// CommitSubject is the first line of generated commit messages. It is
	// what CommitTemplate's default renders as its subject.
	CommitSubject string
	// CommitTemplate renders the whole commit message. A nil template means
	// the built-in one; see ParseCommitTemplate.
	CommitTemplate *template.Template
	// Pull and Push independently disable the network halves of a cycle.
	Pull, Push bool
	// RunHooks lets the repository's git hooks run on commit.
	RunHooks bool
	// ResolveTimestamps lets a rebase that stopped only on conflicting
	// frontmatter timestamps be resolved and continued, rather than aborted.
	ResolveTimestamps bool
	// DryRun does everything except write files and run mutating git commands.
	DryRun bool
}

// Syncer runs sync cycles against one repository.
type Syncer struct {
	cfg Config
	git *gitx.Runner
	log *slog.Logger
}

// New builds a Syncer.
func New(cfg Config, git *gitx.Runner, log *slog.Logger) *Syncer {
	return &Syncer{cfg: cfg, git: git, log: log}
}

// Result summarises one cycle.
type Result struct {
	Stamped   []string
	Committed []string
	Fetched   bool
	Rebased   bool
	Pushed    bool
}

func (r Result) DidSomething() bool {
	return len(r.Committed) > 0 || r.Rebased || r.Pushed
}

func (r Result) String() string {
	var parts []string
	if n := len(r.Stamped); n > 0 {
		parts = append(parts, fmt.Sprintf("stamped %d", n))
	}
	if n := len(r.Committed); n > 0 {
		parts = append(parts, fmt.Sprintf("committed %d", n))
	}
	if r.Rebased {
		parts = append(parts, "rebased")
	}
	if r.Pushed {
		parts = append(parts, "pushed")
	}
	if len(parts) == 0 {
		return "nothing to do"
	}
	return strings.Join(parts, ", ")
}

// ErrBusy reports that the repository has a half-finished operation and the
// user is presumably in the middle of resolving something by hand.
var ErrBusy = errors.New("repository has an operation in progress")

// ErrDetached reports a detached HEAD, where committing and pushing would
// either be lost or land somewhere the user did not intend.
var ErrDetached = errors.New("HEAD is detached")

// ErrConflict reports that upstream and local history diverged in a way that
// needs a human. The worktree is left untouched.
var ErrConflict = errors.New("sync stopped on a conflict")

// Sync runs one full cycle: stamp, commit, fetch, rebase, push.
func (s *Syncer) Sync(ctx context.Context) (Result, error) {
	var res Result

	// Before anything else, refuse to act on a repository the user has left
	// mid-operation. Staging files during a conflicted merge or a paused
	// rebase would commit conflict markers over their work.
	if op, err := s.git.InProgress(ctx); err != nil {
		return res, err
	} else if op != "" {
		return res, fmt.Errorf("%w: %s", ErrBusy, op)
	}

	st, err := s.git.Status(ctx)
	if err != nil {
		return res, err
	}
	if len(st.Unmerged) > 0 {
		return res, fmt.Errorf("%w: %d unmerged path(s)", ErrBusy, len(st.Unmerged))
	}
	if st.Detached {
		return res, ErrDetached
	}

	now := time.Now()
	if s.cfg.Stamp.Enabled {
		stamped, err := s.stampFiles(ctx, st.Changed, now)
		if err != nil {
			return res, err
		}
		res.Stamped = stamped
	}

	committed, err := s.commit(ctx, st)
	if err != nil {
		return res, err
	}
	res.Committed = committed

	if !st.HasUpstream() {
		s.log.Debug("no upstream configured, staying local")
		return res, nil
	}

	remotes, err := s.git.Remotes(ctx)
	if err != nil {
		return res, err
	}
	remote, branch := gitx.SplitUpstream(st.Upstream, remotes)
	if remote == "" {
		s.log.Warn("cannot resolve upstream, staying local", "upstream", st.Upstream)
		return res, nil
	}

	if s.cfg.Pull {
		if err := s.pull(ctx, &res, st.Upstream, remote); err != nil {
			return res, err
		}
	}

	if s.cfg.Push {
		if err := s.push(ctx, &res, st.Upstream, remote, branch); err != nil {
			return res, err
		}
	}
	return res, nil
}

func (s *Syncer) commit(ctx context.Context, st gitx.Status) ([]string, error) {
	// git status has already applied the repository's ignore rules, so the
	// only filtering left is our own scratch, which a crash mid-write can
	// leave behind and which is not the user's to gitignore.
	var paths, lines []string
	for _, e := range st.Changed {
		if noise.IsScratch(e.Path) {
			s.log.Debug("skipping leftover scratch file", "path", e.Path)
			continue
		}
		paths = append(paths, e.Path)
		lines = append(lines, e.Label())
	}
	if len(paths) == 0 {
		return nil, nil
	}
	sort.Strings(lines)

	if s.cfg.DryRun {
		s.log.Info("would commit", "files", len(paths))
		return paths, nil
	}

	if err := s.git.HasAuthor(ctx); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	if err := s.git.AddPaths(ctx, paths); err != nil {
		return nil, err
	}

	// Re-check: `git add` on a path whose only change was an ignored or
	// unchanged attribute can leave the index identical to HEAD, and
	// `git commit` errors out on an empty commit.
	after, err := s.git.Status(ctx)
	if err != nil {
		return nil, err
	}
	staged := false
	for _, e := range after.Changed {
		if e.X != '.' && e.X != '?' {
			staged = true
			break
		}
	}
	if !staged {
		s.log.Debug("nothing staged after add, skipping commit")
		return nil, nil
	}

	msg, err := s.commitMessage(paths, lines)
	if err != nil {
		return nil, err
	}
	if err := s.git.Commit(ctx, gitx.CommitOptions{
		Message:  msg,
		RunHooks: s.cfg.RunHooks,
	}); err != nil {
		return nil, err
	}
	s.log.Info("committed", "files", len(paths))
	return paths, nil
}

func (s *Syncer) pull(ctx context.Context, res *Result, upstream, remote string) error {
	if s.cfg.DryRun {
		s.log.Info("would fetch and rebase", "upstream", upstream)
		return nil
	}
	if err := s.git.Fetch(ctx, remote); err != nil {
		return fmt.Errorf("%w: fetch %s: %w", ErrRemote, remote, err)
	}
	res.Fetched = true

	st, err := s.git.Status(ctx)
	if err != nil {
		return err
	}
	if st.Behind == 0 {
		return nil
	}

	s.log.Info("rebasing onto upstream", "upstream", upstream, "behind", st.Behind)
	if err := s.git.Rebase(ctx, upstream, s.resolveTimestamps); err != nil {
		if errors.Is(err, gitx.ErrRebaseConflict) {
			return fmt.Errorf("%w: %s and local history diverged; resolve by hand", ErrConflict, upstream)
		}
		return err
	}
	res.Rebased = true
	return nil
}

func (s *Syncer) push(ctx context.Context, res *Result, upstream, remote, branch string) error {
	st, err := s.git.Status(ctx)
	if err != nil {
		return err
	}
	if st.Ahead == 0 {
		return nil
	}
	if s.cfg.DryRun {
		s.log.Info("would push", "upstream", upstream, "ahead", st.Ahead)
		return nil
	}

	err = s.git.Push(ctx, remote, branch)
	if err == nil {
		res.Pushed = true
		s.log.Info("pushed", "upstream", upstream, "commits", st.Ahead)
		return nil
	}
	if !gitx.IsRejected(err) {
		return fmt.Errorf("%w: push %s: %w", ErrRemote, upstream, err)
	}

	// Someone pushed between our fetch and our push. Catch up once and retry;
	// a blind retry would be rejected identically.
	s.log.Info("push rejected, refetching and retrying")
	if err := s.pull(ctx, res, upstream, remote); err != nil {
		return err
	}
	if err := s.git.Push(ctx, remote, branch); err != nil {
		return fmt.Errorf("%w: push %s: %w", ErrRemote, upstream, err)
	}
	res.Pushed = true
	return nil
}

// DefaultCommitMessage is the template used when none is configured. It
// renders the subject, a blank line, and one status label per file:
//
//	notes: auto-sync (2 file(s))
//
//	M inbox/today.md
//	A refs/git.md
const DefaultCommitMessage = "{{.Subject}} ({{.Count}} file(s))\n\n{{range .Changes}}{{.}}\n{{end}}"

// CommitData is what a commit-message template is executed against.
type CommitData struct {
	// Subject is the configured commit subject.
	Subject string
	// Count is how many files the commit touches.
	Count int
	// Files are the repository-relative paths, sorted.
	Files []string
	// Changes are the same files as "M path" status labels, sorted.
	Changes []string
	// Time is when the commit is being made.
	Time time.Time
}

// ParseCommitTemplate compiles a commit-message template. An empty text gives
// the built-in one, so callers can pass a flag value straight through.
func ParseCommitTemplate(text string) (*template.Template, error) {
	if strings.TrimSpace(text) == "" {
		text = DefaultCommitMessage
	}
	return template.New("commit").Parse(text)
}

var defaultCommitTemplate = template.Must(ParseCommitTemplate(""))

// commitMessage renders the configured template. A template that produces
// nothing is an error rather than a commit git would reject.
func (s *Syncer) commitMessage(paths, labels []string) (string, error) {
	t := s.cfg.CommitTemplate
	if t == nil {
		t = defaultCommitTemplate
	}

	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)

	var b strings.Builder
	if err := t.Execute(&b, CommitData{
		Subject: s.cfg.CommitSubject,
		Count:   len(paths),
		Files:   sorted,
		Changes: labels,
		Time:    time.Now(),
	}); err != nil {
		return "", fmt.Errorf("%w: commit message template: %w", ErrConfig, err)
	}
	msg := b.String()
	if strings.TrimSpace(msg) == "" {
		return "", fmt.Errorf("%w: commit message template produced an empty message", ErrConfig)
	}
	return msg, nil
}
