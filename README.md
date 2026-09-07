# gitnotesync

Watches a git repository of notes: commits your edits automatically, pulls
notes written on other machines, and maintains GitJournal-style `created:` /
`modified:` timestamps in each note's YAML frontmatter.

```bash
gitnotesync ~/notes
```

## Install

```bash
go install github.com/kvth/gitnotesync@latest
```

Or from a checkout of [kvth/gitnotesync](https://github.com/kvth/gitnotesync),
`make install` (builds with a version stamp and installs into `~/.local/bin`;
override with `PREFIX=`).

## Commands

| Command | What it does |
| --- | --- |
| `gitnotesync <repo>` | Same as `watch` |
| `watch <repo>` | Watch and sync continuously |
| `sync <repo>` | One cycle, then exit — for cron or a systemd timer |

Try it read-only first:

```bash
gitnotesync sync ~/notes --dry-run -v
```

## The sync cycle

1. **Refuse** if a rebase/merge/cherry-pick is half-finished, if any path is
   unmerged, or if HEAD is detached.
2. **Stamp** frontmatter on changed notes.
3. **Commit** everything git reports as changed.
4. **Fetch**, and **rebase** onto upstream if behind.
5. **Push** if ahead.

A sync is triggered by a filesystem change (after a quiet period), by the poll
timer, or when the machine wakes from sleep.

## Is the frontmatter rewriting safe?

That is the part of this tool that writes to your notes, so the design is built
around four properties.

**It cannot loop.** Only files git already reports as dirty are ever opened, and
anything rewritten is committed in the same cycle. The write does wake the
watcher once more; that pass finds a clean worktree and stops. Verified in
`TestSyncIsIdempotent`.

**It will not clobber a save in progress.** A file is skipped unless it has been
quiet for `--settle` (2s). Before writing, its mtime and size are re-checked
against what was read — if the editor touched it in between, the file is left for
the next cycle. Writes go to a temp file and `rename(2)`, so no reader ever sees
a half-written note.

**It will not reformat your YAML.** The frontmatter is edited as lines of text,
never parsed and re-serialised. Key order, quoting style, comments, anchors,
block scalars and CRLF endings all survive, because nothing but the one
timestamp line is touched. A nested `modified:`, one inside a `|` block, one
inside a multi-line quoted string, or one in the body past the closing `---` are
all correctly ignored. A file that opens with `---` and never closes it is left
completely alone rather than guessed at.

**It will not overwrite `created:`.** An existing non-empty value is never
touched. A missing one is backfilled from the file's first commit, so notes that
predate this tool get their real date rather than today's.

The residual risks, honestly:

- **An editor with a stale in-memory buffer** can overwrite the added
  frontmatter on its next save. Obsidian, VS Code and vim all reload on external
  change; an editor that does not will lose the stamp until the next edit. No
  external writer can prevent this.
- **`modified:` conflicts on rebase.** Two machines editing one note make that
  line conflict. It is an ordinary git conflict on top of a real one — the
  rebase is aborted and your worktree is left untouched.
- **Frontmatter blocks are added to every note you edit.** On a vault that has
  never had frontmatter, that is a large first diff. Run
  `sync --dry-run -v` first, or pass `--no-create-frontmatter` to maintain
  only the notes that already have a block.

## Configuration

Flags can be persisted per repository in `.git/config`; explicit flags win.

```ini
[gitnotesync]
    poll = 5m
    include = *.md
    noCreateFrontmatter = true
```

Key names are matched ignoring case and dashes, so `noCreateFrontmatter` and
`no-create-frontmatter` both reach `--no-create-frontmatter`. A bare key with
no `=` means true, so `noCreateFrontmatter` on a line of its own does the same. Values keep their newlines, so a multi-line
commit template can be quoted the way git expects:

```ini
[gitnotesync]
    commitMessage = "vault: {{.Count}} note(s)\n\n{{range .Files}}- {{.}}\n{{end}}"
```

Selected flags:

| Flag | Default | |
| --- | --- | --- |
| `--poll` | `10m` | How often to sync anyway, for remote notes |
| `--debounce` | `3s` | Quiet period after the last edit before syncing |
| `--max-debounce` | `60s` | Longest continuous editing may postpone a sync |
| `--settle` | `2s` | How long a file must be quiet before it is rewritten |
| `--include` | `*.md,*.markdown` | Which files to stamp |
| `--set-created` / `--set-modified` | `created` / `modified` | Frontmatter key for each timestamp; empty disables that one |
| `--time-format` | RFC3339 | Go time layout |
| `--no-modified-from-mtime` | | Record when the daemon woke, not when you saved |
| `--no-backfill-created` | | Leave a missing `created` missing |
| `--no-create-frontmatter` | | Leave notes that have no block alone |
| `--commit-subject` | `notes: auto-sync` | First line of generated commits |
| `--commit-message` | built-in | Go template for the whole message (see below) |
| `--no-pull` / `--no-push` | | Switch off either network half |
| `--no-frontmatter` | | Pure auto-sync, no note rewriting (same as emptying both keys) |
| `--dry-run` | | Change nothing |

### Commit messages

`--commit-subject` changes the first line. `--commit-message` replaces the
message entirely with a Go template, executed against:

| Field | |
| --- | --- |
| `.Subject` | Whatever `--commit-subject` is set to |
| `.Count` | How many files the commit touches |
| `.Files` | The repository-relative paths, sorted |
| `.Changes` | The same files as `M path` status labels |
| `.Time` | When the commit is made, a `time.Time` |

The built-in template is the subject, a blank line, then one label per file:

```
{{.Subject}} ({{.Count}} file(s))

{{range .Changes}}{{.}}
{{end}}
```

`\n` and `\t` in the flag value become real characters, so a multi-line
template still fits in one argument — inside `{{...}}` they are left alone:

```bash
gitnotesync sync ~/notes --commit-message 'vault: {{.Count}} note(s)\n\n{{range .Files}}- {{.}}\n{{end}}'
```

A template that fails to parse is reported before any git command runs, so a
typo cannot wedge a running watcher.

Commit signing is **always off**, and git hooks are off by default: in a
background daemon a signing passphrase prompt has nowhere to appear and would
hang forever, and a failing `pre-commit` hook would wedge syncing with no
visible cause. Hooks can be re-enabled with `--run-hooks`; signing cannot, so
commit these notes by hand if you need them signed.

## Running as a service

[`contrib/systemd/gitnotesync@.service`](contrib/systemd/gitnotesync@.service)
is a template unit: the instance name is the vault's path, so one file serves
every vault you have.

```bash
install -Dm644 contrib/systemd/gitnotesync@.service ~/.config/systemd/user/gitnotesync@.service
```

```bash
systemctl --user enable --now "gitnotesync@$(systemd-escape --path ~/notes)"
```

Each vault is then a service of its own — one stuck on a conflict neither stops
the others nor buries their logs:

```bash
journalctl --user -u "gitnotesync@$(systemd-escape --path ~/notes)" -f
```

The daemon inherits its environment from the systemd user manager, so
credential helpers work. Two things it may not inherit: `SSH_AUTH_SOCK`, if
pushes fail with `Permission denied (publickey)`, and `PATH`, which is how you
point it at a different git — both are commented examples in the unit file.
`GIT_TERMINAL_PROMPT=0` and SSH `BatchMode=yes` are forced regardless, so a
missing credential fails fast and loudly instead of hanging.

## What gets ignored

Your `.gitignore`. That is the whole mechanism — there is no parallel skip list
in this tool to keep in sync with it. Ignore questions are put to
`git check-ignore`, so per-directory `.gitignore` files, `.git/info/exclude`,
`core.excludesFile`, negations (`!keep.log`), `**`, directory-only patterns and
their precedence all behave exactly as they do for `git status`. Tracked files
are correctly never treated as ignored, and changing a `.gitignore` takes effect
on the next cycle with nothing to invalidate.

This is precise in a way a list of directory names cannot be: if you gitignore
`.obsidian/workspace.json`, that one churning file stops waking the daemon while
the rest of `.obsidian/` keeps syncing. Whether your vault config is synced is
your decision to record in your `.gitignore`, not this tool's to guess.

The watcher asks once per debounce window, not once per event, so a burst of
edits costs a single subprocess — and a window in which everything was ignored
costs no sync at all. At startup the tree is walked breadth-first with one
check-ignore call per depth level, so a gitignored `node_modules` is never
descended into.

Exactly two things are decided without asking git, and both are this tool's
own doing rather than yours:

- **`.git/` is always excluded.** git does not *ignore* `.git`; it is simply
  outside the worktree, so `git check-ignore .git/index` answers "not ignored".
  Watching it would be self-defeating — every commit, fetch and ref update this
  tool performs writes there, so each sync would immediately trigger the next.
- **Our own scratch files** (`.gitnotesync-*`) are skipped. Rewriting a note
  goes through a temp file in the same directory and a `rename(2)`, so one
  exists in your vault for an instant on every stamp — and a crash in between
  leaves one behind. Committing that would push half a note into your notes.

Editor scratch files (`.swp`, `4913`, `foo~`, `#foo#`, `.goutputstream-*`,
`.part`) get no such treatment — they are yours. If your editor drops them in
the vault, gitignore them, the same as any other file you do not want
committed:

```gitignore
*~
*.sw[a-p]
.#*
\#*#
```

## Why subprocess git, not go-git

Every git operation shells out to the real binary. That keeps credential
helpers, the SSH agent, signing, hooks and `.gitignore` semantics identical to
what you get on the command line — and `rebase`, which the core loop depends on,
does not exist in go-git at all. Using `git status --porcelain=v2` and
`git check-ignore` as the source of truth also means ignore rules never need
reimplementing: a hand-rolled matcher that is 95% correct silently refuses to
sync a note, and you find out months later.

Requires git ≥ 2.25.

## Development

```bash
go test ./...
```

The tests run against real temporary repositories with a real remote, covering
conflict abort, refusal during a half-finished merge, detached HEAD, dry run,
loop safety and frontmatter round-tripping.
