// Package gitx runs git as a subprocess.
//
// Everything goes through the real git binary rather than a pure-Go
// implementation, so that credential helpers, the SSH agent, signing,
// .gitignore semantics and `rebase` all behave exactly as they do when the
// user runs git by hand.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Runner executes git commands against a single repository.
type Runner struct {
	// Dir is the working directory: the repository root.
	Dir string
	// Env holds extra "KEY=VALUE" entries layered on top of the parent
	// environment. Later entries win.
	Env []string
	// Timeout bounds a single git invocation. Zero means no timeout.
	Timeout time.Duration
}

// Error carries the exit status and captured output of a failed git command.
type Error struct {
	Args   []string
	Code   int
	Stdout string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := e.Stderr
	if msg == "" {
		msg = e.Stdout
	}
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.Args, " "), e.Err, strings.TrimSpace(msg))
}

func (e *Error) Unwrap() error { return e.Err }

// ExitCode reports the exit status of err if it came from a git command, or -1.
func ExitCode(err error) int {
	var gerr *Error
	if errors.As(err, &gerr) {
		return gerr.Code
	}
	return -1
}

// Stderr returns the captured stderr of a failed git command, or "".
func Stderr(err error) string {
	var gerr *Error
	if errors.As(err, &gerr) {
		return gerr.Stderr
	}
	return ""
}

type runOpts struct {
	stdin    []byte
	readOnly bool
}

// Run executes a git command and returns its stdout.
func (r *Runner) Run(ctx context.Context, args ...string) (string, error) {
	return r.run(ctx, runOpts{}, args)
}

// RunRead executes a read-only git command. It sets GIT_OPTIONAL_LOCKS=0 so
// that inspecting the repository never takes index.lock, which would otherwise
// race against a git command the user runs at the same time.
func (r *Runner) RunRead(ctx context.Context, args ...string) (string, error) {
	return r.run(ctx, runOpts{readOnly: true}, args)
}

// RunStdin executes a git command, feeding it stdin.
func (r *Runner) RunStdin(ctx context.Context, stdin []byte, args ...string) (string, error) {
	return r.run(ctx, runOpts{stdin: stdin}, args)
}

// RunReadStdin executes a read-only git command, feeding it stdin.
func (r *Runner) RunReadStdin(ctx context.Context, stdin []byte, args ...string) (string, error) {
	return r.run(ctx, runOpts{stdin: stdin, readOnly: true}, args)
}

func (r *Runner) run(ctx context.Context, opts runOpts, args []string) (string, error) {
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}

	full := append([]string{"--no-pager"}, args...)

	var stdout, stderr bytes.Buffer
	// git is resolved from $PATH, so a different git is a matter of the
	// environment the tool is started in rather than a flag of its own.
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = r.Dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = r.environ(opts.readOnly)
	if opts.stdin != nil {
		cmd.Stdin = bytes.NewReader(opts.stdin)
	}

	err := cmd.Run()
	if err != nil {
		return stdout.String(), &Error{
			Args:   args,
			Code:   cmd.ProcessState.ExitCode(),
			Stdout: stdout.String(),
			Stderr: stderr.String(),
			Err:    err,
		}
	}
	return stdout.String(), nil
}

// environ builds the child environment.
//
// Unlike a bare exec, this deliberately inherits the parent environment: a
// sync daemon that drops SSH_AUTH_SOCK or the user's PATH will fail to push
// with an error that looks nothing like the real cause. On top of that it
// forces non-interactivity, because a git subprocess that decides to prompt
// for a passphrase has no terminal to prompt on and would hang the daemon
// forever.
func (r *Runner) environ(readOnly bool) []string {
	env := os.Environ()

	set := func(k, v string) {
		prefix := k + "="
		for i, e := range env {
			if strings.HasPrefix(e, prefix) {
				env[i] = prefix + v
				return
			}
		}
		env = append(env, prefix+v)
	}
	lookup := func(k string) (string, bool) {
		prefix := k + "="
		for _, e := range env {
			if strings.HasPrefix(e, prefix) {
				return e[len(prefix):], true
			}
		}
		return "", false
	}

	// Never block on a credential or passphrase prompt.
	set("GIT_TERMINAL_PROMPT", "0")
	set("SSH_ASKPASS_REQUIRE", "never")
	if _, ok := lookup("GIT_SSH_COMMAND"); !ok {
		set("GIT_SSH_COMMAND", "ssh -o BatchMode=yes")
	}
	// Stable, parseable output regardless of the user's locale.
	set("LC_ALL", "C")

	if readOnly {
		set("GIT_OPTIONAL_LOCKS", "0")
	}

	env = append(env, r.Env...)
	return env
}
