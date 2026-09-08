package frontmatter

import (
	"strings"
	"testing"
)

func conflicted(ours, theirs string) []byte {
	return []byte("<<<<<<< HEAD\n" + ours + "=======\n" + theirs + ">>>>>>> 1a2b3c4 (notes: auto-sync)\n")
}

func TestResolveModifiedTakesLater(t *testing.T) {
	src := []byte("---\ncreated: 2026-01-01T00:00:00Z\n" +
		string(conflicted(
			"modified: 2026-09-07T10:00:00Z\n",
			"modified: 2026-09-07T11:30:00Z\n")) +
		"---\n\nnote body\n")

	res := ResolveConflict(src, opts())
	if !res.Resolved {
		t.Fatal("expected resolution")
	}
	got := string(res.Content)
	if !strings.Contains(got, "modified: 2026-09-07T11:30:00Z") {
		t.Errorf("wanted the later timestamp, got:\n%s", got)
	}
	if strings.Contains(got, "<<<") || strings.Contains(got, "10:00:00") {
		t.Errorf("markers or losing side left behind:\n%s", got)
	}
	if strings.Join(res.Keys, ",") != "modified" {
		t.Errorf("Keys = %v", res.Keys)
	}
}

func TestResolveCreatedTakesEarlier(t *testing.T) {
	src := []byte("---\n" +
		string(conflicted(
			"created: 2026-05-05T09:00:00Z\n",
			"created: 2026-01-01T09:00:00Z\n")) +
		"modified: 2026-09-07T10:00:00Z\n---\n\nbody\n")

	res := ResolveConflict(src, opts())
	if !res.Resolved {
		t.Fatal("expected resolution")
	}
	if !strings.Contains(string(res.Content), "created: 2026-01-01T09:00:00Z") {
		t.Errorf("wanted the earlier created:\n%s", res.Content)
	}
}

func TestResolveBothKeysAtOnce(t *testing.T) {
	src := []byte("---\n" +
		string(conflicted(
			"created: 2026-05-05T09:00:00Z\nmodified: 2026-09-07T10:00:00Z\n",
			"created: 2026-01-01T09:00:00Z\nmodified: 2026-09-07T11:00:00Z\n")) +
		"tags: [a]\n---\n\nbody\n")

	res := ResolveConflict(src, opts())
	if !res.Resolved {
		t.Fatal("expected resolution")
	}
	got := string(res.Content)
	if !strings.Contains(got, "created: 2026-01-01T09:00:00Z") || !strings.Contains(got, "modified: 2026-09-07T11:00:00Z") {
		t.Errorf("wrong winners:\n%s", got)
	}
	if strings.Join(res.Keys, ",") != "created,modified" {
		t.Errorf("Keys = %v", res.Keys)
	}
}

func TestRefuseBodyConflict(t *testing.T) {
	src := []byte("---\nmodified: 2026-09-07T10:00:00Z\n---\n\n" +
		string(conflicted("mine\n", "theirs\n")))
	if res := ResolveConflict(src, opts()); res.Resolved {
		t.Errorf("body conflict must not be resolved:\n%s", res.Content)
	}
}

func TestRefuseOtherKeyConflict(t *testing.T) {
	src := []byte("---\n" +
		string(conflicted("title: Mine\n", "title: Theirs\n")) +
		"---\n\nbody\n")
	if res := ResolveConflict(src, opts()); res.Resolved {
		t.Error("a title conflict is a real one")
	}
}

func TestRefuseMixedHunk(t *testing.T) {
	src := []byte("---\n" +
		string(conflicted(
			"modified: 2026-09-07T10:00:00Z\ntitle: Mine\n",
			"modified: 2026-09-07T11:00:00Z\ntitle: Theirs\n")) +
		"---\n\nbody\n")
	if res := ResolveConflict(src, opts()); res.Resolved {
		t.Error("a hunk that also touches title must be left alone")
	}
}

func TestRefuseUnparsableTimestamp(t *testing.T) {
	src := []byte("---\n" +
		string(conflicted(
			"modified: yesterday\n",
			"modified: 2026-09-07T11:00:00Z\n")) +
		"---\n\nbody\n")
	if res := ResolveConflict(src, opts()); res.Resolved {
		t.Error("an unparsable timestamp is not ours to pick between")
	}
}

func TestRefuseUnbalancedMarkers(t *testing.T) {
	src := []byte("---\nmodified: 2026-09-07T10:00:00Z\n---\n\n<<<<<<< HEAD\nmine\n")
	if res := ResolveConflict(src, opts()); res.Resolved {
		t.Error("half a conflict must not be resolved")
	}
}

func TestNoMarkersIsNotResolved(t *testing.T) {
	src := []byte("---\nmodified: 2026-09-07T10:00:00Z\n---\n\nbody\n")
	if res := ResolveConflict(src, opts()); res.Resolved {
		t.Error("a clean file has nothing to resolve")
	}
}

func TestEmptyValueLosesToRealOne(t *testing.T) {
	src := []byte("---\n" +
		string(conflicted("created:\n", "created: 2026-01-01T09:00:00Z\n")) +
		"---\n\nbody\n")
	res := ResolveConflict(src, opts())
	if !res.Resolved || !strings.Contains(string(res.Content), "created: 2026-01-01T09:00:00Z") {
		t.Errorf("resolved=%v content:\n%s", res.Resolved, res.Content)
	}
}

func TestDiff3StyleAndCRLF(t *testing.T) {
	src := []byte("---\r\n<<<<<<< HEAD\r\nmodified: 2026-09-07T10:00:00Z\r\n" +
		"||||||| base\r\nmodified: 2026-09-06T10:00:00Z\r\n" +
		"=======\r\nmodified: 2026-09-07T12:00:00Z\r\n>>>>>>> theirs\r\n---\r\n\r\nbody\r\n")
	res := ResolveConflict(src, opts())
	if !res.Resolved {
		t.Fatal("diff3 style should still resolve")
	}
	got := string(res.Content)
	if !strings.Contains(got, "modified: 2026-09-07T12:00:00Z\r\n") {
		t.Errorf("CRLF not preserved or wrong winner:\n%q", got)
	}
	if strings.Contains(got, "2026-09-06") {
		t.Errorf("base section leaked into the merge:\n%q", got)
	}
}

func TestQuotedAndCommentedLineSurvivesWhole(t *testing.T) {
	o := opts()
	o.Quote = true
	src := []byte("---\n" +
		string(conflicted(
			"modified: \"2026-09-07T10:00:00Z\" # mine\n",
			"modified: \"2026-09-07T12:00:00Z\" # theirs\n")) +
		"---\n\nbody\n")
	res := ResolveConflict(src, o)
	if !res.Resolved {
		t.Fatal("expected resolution")
	}
	if !strings.Contains(string(res.Content), `modified: "2026-09-07T12:00:00Z" # theirs`) {
		t.Errorf("winning line was not taken whole:\n%s", res.Content)
	}
}
