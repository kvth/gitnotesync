//go:build unix

package cmd

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// onJournal reports whether f is the stream systemd handed us for the journal.
// systemd sets JOURNAL_STREAM to the "device:inode" of that stream; comparing
// it with f tells us apart from a run whose stderr was redirected elsewhere.
func onJournal(f *os.File) bool {
	spec := os.Getenv("JOURNAL_STREAM")
	if spec == "" {
		return false
	}
	devText, inoText, ok := strings.Cut(spec, ":")
	if !ok {
		return false
	}
	dev, err := strconv.ParseUint(devText, 10, 64)
	if err != nil {
		return false
	}
	ino, err := strconv.ParseUint(inoText, 10, 64)
	if err != nil {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return uint64(sys.Dev) == dev && uint64(sys.Ino) == ino
}
