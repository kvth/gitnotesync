package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRepo(t *testing.T, gitignore string) (*Runner, string) {
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

	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(gitignore), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Runner{Dir: dir, Timeout: 30 * time.Second}, dir
}

func TestIgnored(t *testing.T) {
	r, dir := testRepo(t, "node_modules/\n*.log\n!keep.log\n.obsidian/workspace.json\nbuild/\n")

	// These exist on disk, which is how git knows an unslashed path is a
	// directory and so matches a directory-only pattern.
	for _, d := range []string{"node_modules/x", "build", ".obsidian", "notes"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// A tracked file matching an ignore pattern is not ignored: gitignore does
	// not apply to files already in the index, and check-ignore knows that.
	if err := os.WriteFile(filepath.Join(dir, "tracked.log"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-f", "tracked.log"}, {"commit", "-qm", "x"}} {
		if _, err := r.Run(context.Background(), args...); err != nil {
			t.Fatal(err)
		}
	}

	paths := []string{
		"node_modules", "node_modules/x/a.js", "a.log", "keep.log",
		"notes/b.md", ".obsidian", ".obsidian/workspace.json", ".obsidian/app.json",
		"tracked.log", "build", "missing/gone.log",
	}
	want := map[string]bool{
		"node_modules":             true,
		"node_modules/x/a.js":      true,
		"a.log":                    true,
		".obsidian/workspace.json": true,
		"build":                    true,
		"missing/gone.log":         true, // check-ignore answers for deleted paths too
	}

	got, err := r.Ignored(context.Background(), paths)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if got[p] != want[p] {
			t.Errorf("Ignored(%q) = %v, want %v", p, got[p], want[p])
		}
	}
}

// Exit status 1 is check-ignore's way of saying "none of these are ignored".
// Treating it as a failure would make the watcher fall back to syncing on
// every event.
func TestIgnoredNoneIsNotAnError(t *testing.T) {
	r, _ := testRepo(t, "*.log\n")
	got, err := r.Ignored(context.Background(), []string{"a.md", "b.md"})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("Ignored = %v, want empty", got)
	}
}

func TestIgnoredEmptyInputMakesNoCall(t *testing.T) {
	// A directory that does not exist: any attempt to actually run git here
	// would fail to chdir, so a nil error proves no subprocess was spawned.
	r := &Runner{Dir: filepath.Join(t.TempDir(), "does-not-exist")}
	got, err := r.Ignored(context.Background(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("Ignored(nil) = %v, %v; want empty, nil", got, err)
	}
}

func TestUnignoredPreservesOrder(t *testing.T) {
	r, _ := testRepo(t, "*.log\n")
	got, err := r.Unignored(context.Background(), []string{"z.md", "a.log", "a.md", "b.log", "m.md"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"z.md", "a.md", "m.md"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Unignored = %v, want %v", got, want)
	}
}

// A directory-only pattern such as "build/" is matched by an unslashed path
// only when git can stat it and see a directory. A watcher asks about
// directories that may have just been deleted, so UnignoredDirs adds the
// slash rather than depending on what happens to be on disk.
func TestUnignoredDirsMatchesAbsentDirectories(t *testing.T) {
	r, _ := testRepo(t, "build/\nnode_modules/\n")

	// Nothing was created on disk.
	got, err := r.UnignoredDirs(context.Background(), []string{"build", "notes", "node_modules"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "notes" {
		t.Errorf("UnignoredDirs = %v, want [notes]", got)
	}

	// Without the slash, git cannot tell they are directories and reports
	// them as not ignored -- the behaviour UnignoredDirs exists to avoid.
	plain, err := r.Unignored(context.Background(), []string{"build", "notes"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != 2 {
		t.Logf("unslashed absent dirs: %v (git matched a directory-only pattern anyway)", plain)
	}
}
