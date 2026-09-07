// Package frontmatter updates GitJournal-style `created:` / `modified:` keys
// in a Markdown file's YAML frontmatter.
//
// It deliberately does NOT parse and re-serialise the YAML. Round-tripping
// through a YAML library reorders keys, rewrites quoting, drops comments and
// normalises anchors -- every one of which shows up as a spurious diff in the
// user's notes, and any of which can lose information. Instead the block is
// treated as lines of text and exactly the one line holding a timestamp is
// rewritten. Everything the tool does not understand it does not touch.
package frontmatter

import (
	"bytes"
	"regexp"
	"strings"
	"time"
)

var bom = []byte{0xEF, 0xBB, 0xBF}

// Options configures a stamping pass.
type Options struct {
	// CreatedKey and ModifiedKey are the frontmatter keys to maintain.
	// Matching against the file is case-insensitive. An empty name turns
	// that key off: there is no key to write, so nothing is written.
	CreatedKey  string
	ModifiedKey string
	// TimeFormat is a Go reference layout; defaults to time.RFC3339.
	TimeFormat string
	// Quote wraps written timestamps in double quotes.
	Quote bool
	// CreateBlock allows inserting a frontmatter block into a file that has
	// none. When false, such files are left completely alone.
	CreateBlock bool
}

func (o Options) setCreated() bool  { return o.CreatedKey != "" }
func (o Options) setModified() bool { return o.ModifiedKey != "" }

func (o Options) layout() string {
	if o.TimeFormat == "" {
		return time.RFC3339
	}
	return o.TimeFormat
}

func (o Options) format(t time.Time) string {
	v := t.Format(o.layout())
	if o.Quote {
		return `"` + v + `"`
	}
	return v
}

// Result describes what a stamping pass did.
type Result struct {
	Content []byte
	// Changed is false when the output is byte-identical to the input. This
	// is what makes repeated runs idempotent, and what keeps the tool from
	// committing a file whose only "change" is a timestamp it just wrote.
	Changed bool
	// HadBlock reports whether the input already had frontmatter.
	HadBlock bool
	// AddedBlock reports that a new frontmatter block was inserted.
	AddedBlock bool
	// WroteCreated and WroteModified report which keys were written.
	WroteCreated, WroteModified bool
	// Malformed reports a file that opens with `---` but never closes the
	// block. Nothing is written in that case: the file is more likely to be
	// a horizontal rule or a work in progress than broken frontmatter, and
	// guessing where the block ends risks corrupting real content.
	Malformed bool
}

// keyPattern matches a top-level `key:` at the start of a line. The key is
// restricted to plain YAML scalars, which is what every real note uses; a
// complex key is simply not recognised and therefore not touched.
var keyPattern = regexp.MustCompile(`^([A-Za-z_"'][^:#]*?)[ \t]*:([ \t].*|)$`)

// Apply rewrites the created/modified keys of src and returns the new content.
//
// created is only written when the key is absent or empty: a timestamp the
// user (or another tool) already recorded is never overwritten. modified is
// always written when enabled.
func Apply(src []byte, created, modified time.Time, o Options) Result {
	res := Result{Content: src}

	// Both keys off means there is nothing this pass could write, so the file
	// is not touched at all -- in particular no empty block is created for a
	// note that has none.
	if !o.setCreated() && !o.setModified() {
		return res
	}

	prefix := []byte{}
	body := src
	if bytes.HasPrefix(src, bom) {
		prefix, body = src[:len(bom)], src[len(bom):]
	}

	lines := splitLines(body)
	eol := "\n"
	if len(lines) > 0 && lines[0].end != "" {
		eol = lines[0].end
	}

	start, end, ok := findBlock(lines)
	res.HadBlock = ok

	if !ok {
		if openedButUnclosed(lines) {
			res.Malformed = true
			return res
		}
		if !o.CreateBlock {
			return res
		}
		out := newBlock(created, modified, o, eol)
		res.Content = append(append(append([]byte{}, prefix...), out...), body...)
		res.Changed = !bytes.Equal(res.Content, src)
		res.AddedBlock = res.Changed
		res.WroteCreated = o.setCreated()
		res.WroteModified = o.setModified()
		return res
	}

	inner := lines[start:end]
	createdIdx := findKey(inner, o.CreatedKey)
	modifiedIdx := findKey(inner, o.ModifiedKey)

	if o.setModified() {
		if modifiedIdx >= 0 {
			inner[modifiedIdx] = setValue(inner[modifiedIdx], o.format(modified))
			res.WroteModified = true
		}
	}
	if o.setCreated() {
		if createdIdx >= 0 {
			if isEmptyValue(valueOf(inner[createdIdx])) {
				inner[createdIdx] = setValue(inner[createdIdx], o.format(created))
				res.WroteCreated = true
			}
		}
	}

	// Insertions happen after replacements so the indices above stay valid.
	if o.setCreated() && createdIdx < 0 {
		inner = insertAt(inner, 0, line{text: o.CreatedKey + ": " + o.format(created), end: eol})
		createdIdx = 0
		if modifiedIdx >= 0 {
			modifiedIdx++
		}
		res.WroteCreated = true
	}
	if o.setModified() && modifiedIdx < 0 {
		at := 0
		if createdIdx >= 0 {
			at = createdIdx + 1
		}
		inner = insertAt(inner, at, line{text: o.ModifiedKey + ": " + o.format(modified), end: eol})
		res.WroteModified = true
	}

	out := make([]line, 0, len(lines)+2)
	out = append(out, lines[:start]...)
	out = append(out, inner...)
	out = append(out, lines[end:]...)

	res.Content = append(append([]byte{}, prefix...), joinLines(out)...)
	res.Changed = !bytes.Equal(res.Content, src)
	if !res.Changed {
		res.WroteCreated, res.WroteModified = false, false
	}
	return res
}

func newBlock(created, modified time.Time, o Options, eol string) []byte {
	var b strings.Builder
	b.WriteString("---" + eol)
	if o.setCreated() {
		b.WriteString(o.CreatedKey + ": " + o.format(created) + eol)
	}
	if o.setModified() {
		b.WriteString(o.ModifiedKey + ": " + o.format(modified) + eol)
	}
	b.WriteString("---" + eol + eol)
	return []byte(b.String())
}

// findBlock locates the frontmatter body, returning the half-open line range
// between the opening and closing delimiters.
func findBlock(lines []line) (start, end int, ok bool) {
	if len(lines) == 0 || trimRight(lines[0].text) != "---" {
		return 0, 0, false
	}
	for i := 1; i < len(lines); i++ {
		if t := trimRight(lines[i].text); t == "---" || t == "..." {
			return 1, i, true
		}
	}
	return 0, 0, false
}

func openedButUnclosed(lines []line) bool {
	return len(lines) > 0 && trimRight(lines[0].text) == "---"
}

// findKey returns the index of the top-level line defining key, or -1.
//
// Only column-zero lines are considered, which naturally skips nested mappings
// and the indented content of block scalars. Lines that continue an unbalanced
// double-quoted scalar are skipped explicitly, since those can legally start
// at column zero and would otherwise be mistaken for keys.
func findKey(lines []line, key string) int {
	if key == "" {
		return -1
	}
	inQuoted := false
	for i, l := range lines {
		text := l.text
		if inQuoted {
			if oddQuotes(text) {
				inQuoted = false
			}
			continue
		}
		if text == "" || text[0] == ' ' || text[0] == '\t' || text[0] == '#' || text[0] == '-' {
			continue
		}
		m := keyPattern.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		if oddQuotes(text) {
			inQuoted = true
			continue
		}
		if strings.EqualFold(strings.Trim(m[1], `"'`), key) {
			return i
		}
	}
	return -1
}

// oddQuotes reports an unbalanced count of unescaped double quotes, meaning a
// quoted scalar continues onto the next line.
func oddQuotes(s string) bool {
	n := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			n++
		}
	}
	return n%2 == 1
}

func valueOf(l line) string {
	m := keyPattern.FindStringSubmatch(l.text)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(stripComment(m[2]))
}

func isEmptyValue(v string) bool {
	switch strings.ToLower(v) {
	case "", "~", "null", `""`, "''":
		return true
	}
	return false
}

// setValue rewrites a key line's value, preserving the key exactly as the user
// spelled it (including case and quoting) and keeping any trailing comment.
func setValue(l line, value string) line {
	m := keyPattern.FindStringSubmatch(l.text)
	if m == nil {
		return l
	}
	text := m[1] + ": " + value
	if c := commentOf(m[2]); c != "" {
		text += " " + c
	}
	return line{text: text, end: l.end}
}

// commentOf extracts a trailing `# ...` comment. A value that begins with a
// quote is skipped: the `#` could be inside the string, and losing a character
// of the user's data is worse than losing a comment.
func commentOf(raw string) string {
	v := strings.TrimSpace(raw)
	if strings.HasPrefix(v, `"`) || strings.HasPrefix(v, "'") {
		return ""
	}
	if i := strings.Index(v, "#"); i >= 0 {
		return strings.TrimSpace(v[i:])
	}
	return ""
}

func stripComment(raw string) string {
	v := strings.TrimSpace(raw)
	if strings.HasPrefix(v, `"`) || strings.HasPrefix(v, "'") {
		return v
	}
	if i := strings.Index(v, "#"); i >= 0 {
		return v[:i]
	}
	return v
}

func trimRight(s string) string { return strings.TrimRight(s, " \t") }

// line keeps a line's terminator alongside its text so that CRLF files, and
// files with no trailing newline, round-trip byte for byte.
type line struct {
	text string
	end  string
}

func splitLines(b []byte) []line {
	var out []line
	s := string(b)
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, line{text: s})
			break
		}
		text, end := s[:i], "\n"
		if strings.HasSuffix(text, "\r") {
			text, end = text[:len(text)-1], "\r\n"
		}
		out = append(out, line{text: text, end: end})
		s = s[i+1:]
	}
	return out
}

func joinLines(lines []line) []byte {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l.text)
		b.WriteString(l.end)
	}
	return []byte(b.String())
}

func insertAt(lines []line, at int, l line) []line {
	if at > len(lines) {
		at = len(lines)
	}
	out := make([]line, 0, len(lines)+1)
	out = append(out, lines[:at]...)
	out = append(out, l)
	out = append(out, lines[at:]...)
	return out
}
