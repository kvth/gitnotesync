package gitx

import (
	"context"
	"strconv"
	"strings"
)

// Entry is one changed path reported by `git status`.
type Entry struct {
	// Path is relative to the repository root, slash-separated.
	Path string
	// X is the staged status char, Y the worktree status char, using
	// porcelain-v2 spelling ('.' for unmodified). Untracked entries use "??".
	X, Y byte
}

func (e Entry) Untracked() bool { return e.X == '?' }
func (e Entry) Deleted() bool   { return e.X == 'D' || e.Y == 'D' }

// Label renders the entry the way `git status --short` would, e.g. " M note.md".
func (e Entry) Label() string {
	x, y := e.X, e.Y
	if x == '.' {
		x = ' '
	}
	if y == '.' {
		y = ' '
	}
	return string([]byte{x, y}) + " " + e.Path
}

// Status is a parsed snapshot of the worktree.
type Status struct {
	Branch   string
	Upstream string
	Ahead    int
	Behind   int
	Detached bool
	// Unborn is true on a fresh repository with no commits yet.
	Unborn bool

	Changed  []Entry
	Unmerged []Entry
}

func (s Status) HasChanges() bool  { return len(s.Changed) > 0 }
func (s Status) HasUpstream() bool { return s.Upstream != "" }

// Status parses `git status --porcelain=v2`.
//
// Using git's own status is what lets this tool skip reimplementing
// .gitignore: ignored files simply never appear here.
func (r *Runner) Status(ctx context.Context) (Status, error) {
	out, err := r.RunRead(ctx,
		"status", "--porcelain=v2", "--branch",
		"--untracked-files=all", "--no-renames", "-z")
	if err != nil {
		return Status{}, err
	}
	return parseStatus(out), nil
}

func parseStatus(out string) Status {
	var st Status
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		switch rec[0] {
		case '#':
			parseStatusHeader(&st, rec)
		case '1':
			// 1 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path>
			if f := strings.SplitN(rec, " ", 9); len(f) == 9 && len(f[1]) == 2 {
				st.Changed = append(st.Changed, Entry{Path: f[8], X: f[1][0], Y: f[1][1]})
			}
		case 'u':
			// u <XY> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path>
			if f := strings.SplitN(rec, " ", 11); len(f) == 11 && len(f[1]) == 2 {
				st.Unmerged = append(st.Unmerged, Entry{Path: f[10], X: f[1][0], Y: f[1][1]})
			}
		case '?':
			st.Changed = append(st.Changed, Entry{Path: rec[2:], X: '?', Y: '?'})
		case '!':
			// ignored; never requested, never acted on
		}
	}
	return st
}

func parseStatusHeader(st *Status, rec string) {
	f := strings.SplitN(rec, " ", 3)
	if len(f) < 3 {
		return
	}
	switch f[1] {
	case "branch.oid":
		st.Unborn = f[2] == "(initial)"
	case "branch.head":
		if f[2] == "(detached)" {
			st.Detached = true
		} else {
			st.Branch = f[2]
		}
	case "branch.upstream":
		st.Upstream = f[2]
	case "branch.ab":
		for _, part := range strings.Fields(f[2]) {
			n, err := strconv.Atoi(strings.TrimLeft(part, "+-"))
			if err != nil {
				continue
			}
			switch part[0] {
			case '+':
				st.Ahead = n
			case '-':
				st.Behind = n
			}
		}
	}
}
