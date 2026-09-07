package frontmatter

import (
	"testing"
	"time"
)

var (
	tCreated  = time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	tModified = time.Date(2025, 6, 7, 8, 9, 10, 0, time.UTC)
)

func opts() Options {
	return Options{
		CreatedKey:  "created",
		ModifiedKey: "modified",
		TimeFormat:  time.RFC3339,
		CreateBlock: true,
	}
}

func TestApply(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		opt  func(*Options)
	}{
		{
			name: "updates existing keys in place",
			in:   "---\ncreated: 2020-01-01T00:00:00Z\nmodified: 2020-01-01T00:00:00Z\n---\n\nbody\n",
			want: "---\ncreated: 2020-01-01T00:00:00Z\nmodified: 2025-06-07T08:09:10Z\n---\n\nbody\n",
		},
		{
			name: "never overwrites a non-empty created",
			in:   "---\ncreated: 1999-12-31T23:59:59Z\n---\nbody\n",
			want: "---\ncreated: 1999-12-31T23:59:59Z\nmodified: 2025-06-07T08:09:10Z\n---\nbody\n",
		},
		{
			name: "fills in an empty created",
			in:   "---\ncreated:\ntitle: x\n---\nbody\n",
			want: "---\ncreated: 2024-01-02T03:04:05Z\nmodified: 2025-06-07T08:09:10Z\ntitle: x\n---\nbody\n",
		},
		{
			name: "preserves unrelated keys, order, quoting and comments",
			in:   "---\ntags: [a, b]   # my tags\ntitle: 'Hello: World'\nmodified: old\naliases:\n  - one\n  - two\n---\nbody\n",
			want: "---\ncreated: 2024-01-02T03:04:05Z\ntags: [a, b]   # my tags\ntitle: 'Hello: World'\nmodified: 2025-06-07T08:09:10Z\naliases:\n  - one\n  - two\n---\nbody\n",
		},
		{
			name: "keeps a trailing comment on a rewritten key",
			in:   "---\nmodified: old # when\n---\nbody\n",
			want: "---\ncreated: 2024-01-02T03:04:05Z\nmodified: 2025-06-07T08:09:10Z # when\n---\nbody\n",
		},
		{
			name: "preserves the key's original spelling",
			in:   "---\nModified: old\n---\nbody\n",
			want: "---\ncreated: 2024-01-02T03:04:05Z\nModified: 2025-06-07T08:09:10Z\n---\nbody\n",
		},
		{
			name: "creates a block when there is none",
			in:   "# Title\n\nbody\n",
			want: "---\ncreated: 2024-01-02T03:04:05Z\nmodified: 2025-06-07T08:09:10Z\n---\n\n# Title\n\nbody\n",
		},
		{
			name: "leaves a file alone when block creation is off",
			in:   "# Title\n",
			want: "# Title\n",
			opt:  func(o *Options) { o.CreateBlock = false },
		},
		{
			name: "ignores a nested key of the same name",
			in:   "---\nmeta:\n  modified: nested\n---\nbody\n",
			want: "---\ncreated: 2024-01-02T03:04:05Z\nmodified: 2025-06-07T08:09:10Z\nmeta:\n  modified: nested\n---\nbody\n",
		},
		{
			name: "ignores a key inside a block scalar",
			in:   "---\nnote: |\n  modified: not a key\ntitle: x\n---\nbody\n",
			want: "---\ncreated: 2024-01-02T03:04:05Z\nmodified: 2025-06-07T08:09:10Z\nnote: |\n  modified: not a key\ntitle: x\n---\nbody\n",
		},
		{
			name: "ignores a key inside a multi-line quoted scalar",
			in:   "---\ndesc: \"line one\nmodified: not a key\"\ntitle: x\n---\nbody\n",
			want: "---\ncreated: 2024-01-02T03:04:05Z\nmodified: 2025-06-07T08:09:10Z\ndesc: \"line one\nmodified: not a key\"\ntitle: x\n---\nbody\n",
		},
		{
			name: "closing ... terminates the block",
			in:   "---\nmodified: old\n...\nbody\n",
			want: "---\ncreated: 2024-01-02T03:04:05Z\nmodified: 2025-06-07T08:09:10Z\n...\nbody\n",
		},
		{
			name: "does not touch a body key past the closing delimiter",
			in:   "---\ntitle: x\n---\nmodified: this is prose\n",
			want: "---\ncreated: 2024-01-02T03:04:05Z\nmodified: 2025-06-07T08:09:10Z\ntitle: x\n---\nmodified: this is prose\n",
		},
		{
			name: "preserves CRLF line endings",
			in:   "---\r\nmodified: old\r\n---\r\nbody\r\n",
			want: "---\r\ncreated: 2024-01-02T03:04:05Z\r\nmodified: 2025-06-07T08:09:10Z\r\n---\r\nbody\r\n",
		},
		{
			name: "preserves a missing trailing newline",
			in:   "---\nmodified: old\n---\nbody",
			want: "---\ncreated: 2024-01-02T03:04:05Z\nmodified: 2025-06-07T08:09:10Z\n---\nbody",
		},
		{
			name: "preserves a UTF-8 BOM",
			in:   "\ufeff---\nmodified: old\n---\nbody\n",
			want: "\ufeff---\ncreated: 2024-01-02T03:04:05Z\nmodified: 2025-06-07T08:09:10Z\n---\nbody\n",
		},
		{
			name: "quotes values when asked",
			in:   "---\nmodified: old\n---\n",
			want: "---\ncreated: \"2024-01-02T03:04:05Z\"\nmodified: \"2025-06-07T08:09:10Z\"\n---\n",
			opt:  func(o *Options) { o.Quote = true },
		},
		{
			name: "modified only, created untouched",
			in:   "# Title\n",
			want: "---\nmodified: 2025-06-07T08:09:10Z\n---\n\n# Title\n",
			opt:  func(o *Options) { o.CreatedKey = "" },
		},
		{
			// Both keys off is the way to turn stamping off, so it must not
			// leave an empty block behind on a note that had none.
			name: "no keys, no block",
			in:   "# Title\n",
			want: "# Title\n",
			opt:  func(o *Options) { o.CreatedKey, o.ModifiedKey = "", "" },
		},
		{
			name: "no keys, existing block untouched",
			in:   "---\nmodified: old\ntitle: t\n---\n\nbody\n",
			want: "---\nmodified: old\ntitle: t\n---\n\nbody\n",
			opt:  func(o *Options) { o.CreatedKey, o.ModifiedKey = "", "" },
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := opts()
			if tc.opt != nil {
				tc.opt(&o)
			}
			got := Apply([]byte(tc.in), tCreated, tModified, o)
			if string(got.Content) != tc.want {
				t.Errorf("content mismatch\n got: %q\nwant: %q", got.Content, tc.want)
			}
			if want := tc.in != tc.want; got.Changed != want {
				t.Errorf("Changed = %v, want %v", got.Changed, want)
			}
		})
	}
}

// A file opening with --- but never closing it is ambiguous: it could be a
// horizontal rule, or a half-typed block. Rewriting it could corrupt content,
// so nothing is written.
func TestApplyMalformedBlockIsUntouched(t *testing.T) {
	in := "---\nthis never closes\nand keeps going\n"
	got := Apply([]byte(in), tCreated, tModified, opts())
	if got.Changed || string(got.Content) != in {
		t.Fatalf("malformed block was modified: %q", got.Content)
	}
	if !got.Malformed {
		t.Error("Malformed = false, want true")
	}
}

// Idempotency is the property that keeps the watcher from looping: stamping an
// already-stamped file with the same timestamp must be a no-op.
func TestApplyIsIdempotent(t *testing.T) {
	for _, in := range []string{
		"---\ntitle: x\n---\nbody\n",
		"# no frontmatter\n",
		"---\ncreated: 2020-01-01T00:00:00Z\nmodified: old\n---\nbody\n",
	} {
		first := Apply([]byte(in), tCreated, tModified, opts())
		second := Apply(first.Content, tCreated, tModified, opts())
		if second.Changed {
			t.Errorf("second pass changed the file\n in: %q\n 1st: %q\n 2nd: %q",
				in, first.Content, second.Content)
		}
	}
}
