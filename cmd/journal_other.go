//go:build !unix

package cmd

import "os"

// onJournal is always false where there is no journal to write to.
func onJournal(*os.File) bool { return false }
