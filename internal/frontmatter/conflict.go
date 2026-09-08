package frontmatter

import (
	"bytes"
	"strings"
	"time"
)

// Conflict markers, as git writes them. The base section only appears under
// the diff3 and zdiff3 conflict styles.
const (
	markerOurs   = "<<<<<<<"
	markerBase   = "|||||||"
	markerTheirs = "======="
	markerEnd    = ">>>>>>>"
)

// ConflictResult describes an attempt to resolve a conflicted note.
type ConflictResult struct {
	// Content is the merged file, valid only when Resolved is true.
	Content []byte
	// Resolved reports that every conflicting line was a timestamp this
	// tool maintains, and that all of them have been reconciled.
	Resolved bool
	// Keys names the frontmatter keys that had to be reconciled, in the
	// order they appear in the block.
	Keys []string
}

// ResolveConflict merges a note that git left with conflict markers, but only
// when the disagreement is entirely about the timestamps this tool writes
// itself. Two machines stamping the same note produce two `modified:` values
// that are not a real conflict: nobody wrote them, and neither is more
// correct than the other. The later one wins, since it describes the newer of
// the two edits. `created:` goes the other way -- the earlier of the two is
// when the note actually came into being -- and an empty value loses to a
// real one.
//
// Anything else is left alone. A body that conflicts, a key with different
// content on the two sides, a timestamp that will not parse, a block that
// gained or lost a line: all of those are real conflicts, and Resolved is
// false so the caller can back out and leave them to a human.
func ResolveConflict(src []byte, o Options) ConflictResult {
	var res ConflictResult

	prefix := []byte{}
	body := src
	if bytes.HasPrefix(src, bom) {
		prefix, body = src[:len(bom)], src[len(bom):]
	}

	ours, theirs, ok := splitConflict(splitLines(body))
	if !ok {
		return res
	}

	oStart, oEnd, okOurs := findBlock(ours)
	tStart, tEnd, okTheirs := findBlock(theirs)
	if !okOurs || !okTheirs {
		return res
	}

	// Every conflict has to sit inside the frontmatter block, which is the
	// same as saying the two sides agree everywhere outside it.
	if !sameLines(ours[:oStart], theirs[:tStart]) || !sameLines(ours[oEnd:], theirs[tEnd:]) {
		return res
	}
	if oEnd-oStart != tEnd-tStart {
		return res
	}

	merged := append([]line(nil), ours...)
	for i := 0; i < oEnd-oStart; i++ {
		a, b := ours[oStart+i], theirs[tStart+i]
		if a.text == b.text && a.end == b.end {
			continue
		}
		key, winner, ok := reconcile(a, b, o)
		if !ok {
			return res
		}
		merged[oStart+i] = winner
		res.Keys = append(res.Keys, key)
	}
	if len(res.Keys) == 0 {
		// Markers with nothing actually differing inside the block; leave
		// that oddity to git rather than rewriting the file.
		return res
	}

	res.Content = append(append([]byte{}, prefix...), joinLines(merged)...)
	res.Resolved = true
	return res
}

// splitConflict projects a file with conflict markers onto its two sides.
// It fails on markers it does not understand rather than guessing, since a
// wrong projection would silently discard one side's work.
func splitConflict(lines []line) (ours, theirs []line, ok bool) {
	const (
		common = iota
		inOurs
		inBase
		inTheirs
	)
	state := common
	saw := false

	for _, l := range lines {
		text := trimRight(l.text)
		switch {
		case strings.HasPrefix(text, markerOurs):
			if state != common {
				return nil, nil, false
			}
			state, saw = inOurs, true
		case strings.HasPrefix(text, markerBase) && state == inOurs:
			state = inBase
		case text == markerTheirs && (state == inOurs || state == inBase):
			state = inTheirs
		case strings.HasPrefix(text, markerEnd) && state == inTheirs:
			state = common
		default:
			switch state {
			case common:
				ours, theirs = append(ours, l), append(theirs, l)
			case inOurs:
				ours = append(ours, l)
			case inTheirs:
				theirs = append(theirs, l)
			}
		}
	}
	if state != common || !saw {
		return nil, nil, false
	}
	return ours, theirs, true
}

// reconcile picks the winning line for one differing key.
func reconcile(a, b line, o Options) (key string, winner line, ok bool) {
	ka, kb := keyOf(a), keyOf(b)
	if ka == "" || !strings.EqualFold(ka, kb) {
		return "", line{}, false
	}

	var newest bool
	switch {
	case o.setModified() && strings.EqualFold(ka, o.ModifiedKey):
		newest = true
	case o.setCreated() && strings.EqualFold(ka, o.CreatedKey):
		newest = false
	default:
		return "", line{}, false // a key this tool does not own
	}

	va, vb := valueOf(a), valueOf(b)
	switch {
	case isEmptyValue(va) && isEmptyValue(vb):
		return ka, a, true
	case isEmptyValue(va):
		return ka, b, true
	case isEmptyValue(vb):
		return ka, a, true
	}

	ta, err := parseStamp(va, o)
	if err != nil {
		return "", line{}, false
	}
	tb, err := parseStamp(vb, o)
	if err != nil {
		return "", line{}, false
	}

	// The winning line is taken whole, so its quoting, spacing and any
	// trailing comment survive exactly as its author wrote them.
	if ta.After(tb) == newest {
		return ka, a, true
	}
	return ka, b, true
}

// parseStamp reads a timestamp written in the configured layout, falling back
// to RFC3339 for notes stamped before --time-format was changed.
func parseStamp(v string, o Options) (time.Time, error) {
	v = strings.TrimSpace(v)
	// A quoted scalar keeps whatever follows it -- valueOf leaves a trailing
	// comment in place rather than risk cutting at a `#` inside the string.
	if len(v) > 0 && (v[0] == '"' || v[0] == '\'') {
		if i := strings.IndexByte(v[1:], v[0]); i >= 0 {
			v = v[1 : 1+i]
		}
	}
	v = strings.TrimSpace(v)
	t, err := time.Parse(o.layout(), v)
	if err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, v)
}

// keyOf returns the key a line defines, unquoted, or "".
func keyOf(l line) string {
	m := keyPattern.FindStringSubmatch(l.text)
	if m == nil {
		return ""
	}
	return strings.Trim(m[1], `"'`)
}

func sameLines(a, b []line) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].text != b[i].text || a[i].end != b[i].end {
			return false
		}
	}
	return true
}
