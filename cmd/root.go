// Package cmd wires the CLI.
package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/kvth/gitnotesync/internal/frontmatter"
	"github.com/kvth/gitnotesync/internal/gitx"
	"github.com/kvth/gitnotesync/internal/syncer"
)

// Version is set at build time.
var Version = "dev"

type globals struct {
	// git
	timeout time.Duration

	// sync behaviour
	commitSubject string
	commitMessage string
	pull, push    bool
	runHooks      bool
	dryRun        bool

	// frontmatter
	stamp             bool
	include           []string
	createdKey        string
	modifiedKey       string
	timeFormat        string
	quote             bool
	createBlock       bool
	modifiedFromMTime bool
	backfillCreated   bool
	settle            time.Duration
	maxSize           int64

	// watch
	debounce     time.Duration
	maxDebounce  time.Duration
	pollInterval time.Duration

	verbose bool
}

// g holds the defaults for everything that is on unless switched off; the
// --no-* flags below only ever turn these false.
var g = globals{
	pull:              true,
	push:              true,
	stamp:             true,
	createBlock:       true,
	modifiedFromMTime: true,
	backfillCreated:   true,
}

// Root builds the root command.
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:   "gitnotesync [repository]",
		Short: "Auto-commit, pull and push a git notes repository, maintaining GitJournal frontmatter",
		Long: `gitnotesync watches a git repository of notes.

Local edits are committed automatically, the remote is polled for notes written
elsewhere, and each changed Markdown file's YAML frontmatter gets GitJournal-style
created/modified timestamps.

Running it with just a path is the same as ` + "`gitnotesync watch <path>`" + `.`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return runWatch(cmd, args)
		},
	}

	f := root.PersistentFlags()
	f.DurationVar(&g.timeout, "git-timeout", 5*time.Minute, "timeout for a single git command")

	f.StringVar(&g.commitSubject, "commit-subject", "notes: auto-sync", "first line of generated commit messages")
	f.StringVar(&g.commitMessage, "commit-message", "", `Go template for the whole commit message, with .Subject, .Count, .Files, .Changes and .Time (empty for the built-in one; \n and \t are expanded)`)
	noFlag(f, &g.pull, "no-pull", "do not fetch and rebase onto the upstream branch")
	noFlag(f, &g.push, "no-push", "do not push local commits to the upstream branch")
	f.BoolVar(&g.runHooks, "run-hooks", false, "let git hooks run on commit (a failing hook will block syncing)")
	f.BoolVarP(&g.dryRun, "dry-run", "n", false, "report what would happen without changing anything")

	noFlag(f, &g.stamp, "no-frontmatter", "do not maintain created/modified frontmatter timestamps")
	f.StringSliceVar(&g.include, "include", []string{"*.md", "*.markdown"}, "filename patterns to stamp")
	f.StringVar(&g.createdKey, "set-created", "created", "frontmatter key to record the creation time in (empty to leave it alone)")
	f.StringVar(&g.modifiedKey, "set-modified", "modified", "frontmatter key to record the modification time in (empty to leave it alone)")
	f.StringVar(&g.timeFormat, "time-format", time.RFC3339, "Go time layout for written timestamps")
	f.BoolVar(&g.quote, "quote-timestamps", false, "wrap written timestamps in double quotes")
	noFlag(f, &g.createBlock, "no-create-frontmatter", "leave notes that have no frontmatter block alone")
	noFlag(f, &g.modifiedFromMTime, "no-modified-from-mtime", "take modified from the clock rather than the file's mtime")
	noFlag(f, &g.backfillCreated, "no-backfill-created", "do not fill a missing created from the file's first commit")
	f.DurationVar(&g.settle, "settle", 2*time.Second, "how long a file must be quiet before it is rewritten")
	f.Int64Var(&g.maxSize, "max-size", 4<<20, "skip files larger than this many bytes")

	f.DurationVar(&g.debounce, "debounce", 3*time.Second, "quiet period after the last change before syncing")
	f.DurationVar(&g.maxDebounce, "max-debounce", 60*time.Second, "longest that continuous edits may postpone a sync")
	f.DurationVar(&g.pollInterval, "poll", 10*time.Minute, "how often to sync anyway, to pick up remote notes")

	f.BoolVarP(&g.verbose, "verbose", "v", false, "verbose logging")

	root.AddCommand(newWatchCmd(), newSyncCmd())
	return root
}

// logger writes to stderr. Under systemd that stream is the journal, and the
// journal wants the level as a "<N>" prefix rather than as a timestamped line
// of its own, so the handler changes to suit.
func logger() *slog.Logger {
	level := slog.LevelInfo
	if g.verbose {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}
	if onJournal(os.Stderr) {
		return slog.New(newJournalHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

// open resolves the repository, applies its git-config defaults and builds the
// runner and syncer.
func open(cmd *cobra.Command, args []string) (*syncer.Syncer, *gitx.Runner, string, error) {
	path := "."
	if len(args) > 0 {
		path = args[0]
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, "", err
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return nil, nil, "", fmt.Errorf("%s is not a directory", path)
	}

	ctx := cmd.Context()
	root, err := gitx.Toplevel(ctx, abs)
	if err != nil {
		return nil, nil, "", fmt.Errorf("%s is not inside a git repository", path)
	}

	runner := &gitx.Runner{Dir: root, Timeout: g.timeout}
	applyGitConfig(ctx, cmd, runner)

	// Parse here rather than at commit time: in watch mode a bad template
	// would otherwise only show up on the first edit, hours later.
	tmpl, err := syncer.ParseCommitTemplate(expandEscapes(g.commitMessage))
	if err != nil {
		return nil, nil, "", fmt.Errorf("--commit-message: %w", err)
	}

	s := syncer.New(syncer.Config{
		RepoPath:       root,
		CommitSubject:  g.commitSubject,
		CommitTemplate: tmpl,
		Pull:           g.pull,
		Push:           g.push,
		RunHooks:       g.runHooks,
		DryRun:         g.dryRun,
		Stamp: syncer.StampConfig{
			Enabled: g.stamp,
			Include: g.include,
			Options: frontmatter.Options{
				CreatedKey:  g.createdKey,
				ModifiedKey: g.modifiedKey,
				TimeFormat:  g.timeFormat,
				Quote:       g.quote,
				CreateBlock: g.createBlock,
			},
			ModifiedFromMTime: g.modifiedFromMTime,
			BackfillCreated:   g.backfillCreated,
			Settle:            g.settle,
			MaxSize:           g.maxSize,
			DryRun:            g.dryRun,
		},
	}, runner, logger())

	return s, runner, root, nil
}

// applyGitConfig lets a repository carry its own settings in .git/config:
//
//	[gitnotesync]
//	    poll = 5m
//	    include = *.md
//
// Explicit flags always win, so a value in config can be overridden per run.
func applyGitConfig(ctx context.Context, cmd *cobra.Command, r *gitx.Runner) {
	// --null keeps a value that contains newlines -- a multi-line commit
	// template, say -- in one piece: git emits "key\nvalue\0" records.
	out, err := r.RunRead(ctx, "config", "--null", "--get-regexp", `^gitnotesync\.`)
	if err != nil {
		return // section absent; git exits 1
	}
	for _, record := range strings.Split(out, "\x00") {
		if record == "" {
			continue
		}
		key, value, hasValue := strings.Cut(record, "\n")
		name := strings.TrimPrefix(key, "gitnotesync.")
		flag := findFlag(cmd, name)
		if flag == nil || flag.Changed {
			continue
		}
		if !hasValue {
			// A key with no "=" means true, as it does elsewhere in git
			// config. For anything but a bool there is nothing to infer.
			if flag.Value.Type() != "bool" {
				continue
			}
			value = "true"
		}
		if err := flag.Value.Set(normalise(flag, value)); err != nil {
			fmt.Fprintf(os.Stderr, "warning: git config %s: %v\n", key, err)
		}
	}
}

// findFlag resolves a config key against the command's flags. git lowercases
// config keys, so "maxdebounce" has to match "max-debounce".
func findFlag(cmd *cobra.Command, name string) *pflag.Flag {
	flags := cmd.Flags()
	if f := flags.Lookup(name); f != nil {
		return f
	}
	var found *pflag.Flag
	flat := strings.ToLower(strings.ReplaceAll(name, "-", ""))
	flags.VisitAll(func(f *pflag.Flag) {
		if strings.ToLower(strings.ReplaceAll(f.Name, "-", "")) == flat {
			found = f
		}
	})
	return found
}

// noFlag registers a "--no-x" switch for a behaviour that is on by default.
// It takes no argument, and the rest of the code goes on thinking in
// positives: the flag stores the negation of what it is given.
func noFlag(f *pflag.FlagSet, p *bool, name, usage string) {
	f.Var(negBool{p}, name, usage)
	f.Lookup(name).NoOptDefVal = "true"
}

// negBool is the pflag.Value behind a --no-x flag.
type negBool struct{ p *bool }

func (n negBool) Set(s string) error {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return err
	}
	*n.p = !v
	return nil
}

// String reports the state of the flag itself, which is the negation of the
// behaviour it guards.
func (n negBool) String() string { return strconv.FormatBool(!*n.p) }

// Type is "bool" so that pflag renders it as a switch and a valueless key in
// .git/config is read as true.
func (n negBool) Type() string { return "bool" }

// expandEscapes turns the escapes a shell will not have expanded into real
// characters, so that a multi-line commit template can be given as one
// argument. git's own config parser already does this inside a quoted value.
//
// Text inside a {{...}} action is left alone: a template may legitimately
// contain `{{"\n"}}`, and rewriting that would break its quoting.
func expandEscapes(s string) string {
	unescape := strings.NewReplacer(`\n`, "\n", `\t`, "\t", `\\`, `\`)

	var b strings.Builder
	for {
		start := strings.Index(s, "{{")
		if start < 0 {
			b.WriteString(unescape.Replace(s))
			return b.String()
		}
		b.WriteString(unescape.Replace(s[:start]))
		end := strings.Index(s[start:], "}}")
		if end < 0 {
			b.WriteString(s[start:]) // unterminated; let the parser report it
			return b.String()
		}
		b.WriteString(s[start : start+end+2])
		s = s[start+end+2:]
	}
}

// normalise accepts a bare number of seconds for duration flags, so that a
// plain `poll = 300` in .git/config means what its author expected.
func normalise(f *pflag.Flag, value string) string {
	if f.Value.Type() == "duration" {
		if n, err := strconv.Atoi(value); err == nil {
			return (time.Duration(n) * time.Second).String()
		}
	}
	return value
}
